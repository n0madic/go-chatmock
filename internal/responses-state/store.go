package responsesstate

import (
	"container/list"
	"sort"
	"sync"
	"time"

	"github.com/n0madic/go-chatmock/internal/types"
)

const (
	// DefaultTTL controls how long a response's context and function calls are
	// retained. 60 minutes matches the typical access token lifetime; once the
	// token expires and is refreshed, any in-progress conversation will also
	// require a fresh context anyway.
	DefaultTTL = 60 * time.Minute
	// DefaultCapacity is a safety ceiling to prevent unbounded memory growth in
	// long-running server instances. LRU eviction keeps the most recently used
	// entries within this limit.
	DefaultCapacity = 10000
	// cleanupTick is the interval between background expired-entry sweeps.
	cleanupTick = 30 * time.Second
)

// FunctionCall stores a function_call or custom_tool_call item so it can be replayed in a future request.
type FunctionCall struct {
	Type      string // "function_call" or "custom_tool_call"; defaults to "function_call" if empty
	CallID    string
	Name      string
	Arguments string
}

// lruKey identifies either an entry or a conversation link in the LRU list.
type lruKey struct {
	id     string
	isConv bool
}

type entry struct {
	calls        map[string]FunctionCall
	context      []types.ResponsesInputItem
	instructions string
	lastAccess   time.Time
	listElem     *list.Element
}

type conversationLink struct {
	responseID string
	lastAccess time.Time
	listElem   *list.Element
}

// Store keeps per-response function_call state for local previous_response_id polyfill.
// It exists because the upstream (ChatGPT backend) rejects store=true, so the
// server-side conversation memory that previous_response_id depends on is never
// created there. This store is the client-side substitute: after each response
// we save the full conversation snapshot, and on the next request we prepend it
// so the model sees a coherent multi-turn history.
type Store struct {
	mu       sync.Mutex
	entries  map[string]*entry
	conv     map[string]*conversationLink
	lru      *list.List
	ttl      time.Duration
	capacity int
	stopCh   chan struct{}
	done     chan struct{}
}

// NewStore creates an in-memory state store with TTL and capacity limits.
// The caller must call Close to stop the background cleanup goroutine.
func NewStore(ttl time.Duration, capacity int) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	s := &Store{
		entries:  make(map[string]*entry),
		conv:     make(map[string]*conversationLink),
		lru:      list.New(),
		ttl:      ttl,
		capacity: capacity,
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

// Close stops the background cleanup goroutine and waits for it to finish.
func (s *Store) Close() {
	close(s.stopCh)
	<-s.done
}

func (s *Store) cleanupLoop() {
	defer close(s.done)
	ticker := time.NewTicker(cleanupTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			s.cleanupExpiredLocked(now)
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

// Put saves function calls for a response id.
func (s *Store) Put(responseID string, calls []FunctionCall) {
	if responseID == "" || len(calls) == 0 {
		return
	}

	callMap := buildCallMap(calls)
	if len(callMap) == 0 {
		return
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.putCallsLocked(responseID, callMap, now)
	s.evictIfNeededLocked()
}

// PutContext saves reconstructed input context for a response id.
func (s *Store) PutContext(responseID string, context []types.ResponsesInputItem) {
	if responseID == "" {
		return
	}

	ctxCopy := cloneInputItems(context)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.putContextLocked(responseID, ctxCopy, now)
	s.evictIfNeededLocked()
}

// PutSnapshot saves both context and function calls for a response id atomically.
func (s *Store) PutSnapshot(responseID string, context []types.ResponsesInputItem, calls []FunctionCall) {
	if responseID == "" {
		return
	}

	ctxCopy := cloneInputItems(context)
	callMap := buildCallMap(calls)

	if len(ctxCopy) == 0 && len(callMap) == 0 {
		return
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(ctxCopy) > 0 {
		s.putContextLocked(responseID, ctxCopy, now)
	}
	if len(callMap) > 0 {
		s.putCallsLocked(responseID, callMap, now)
	}
	s.evictIfNeededLocked()
}

// PutInstructions saves effective instructions for a response id.
func (s *Store) PutInstructions(responseID string, instructions string) {
	if responseID == "" {
		return
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[responseID]
	if !ok {
		e = &entry{}
		s.entries[responseID] = e
	}
	e.instructions = instructions
	e.lastAccess = now
	s.touchLRU(responseID, false, e)
	s.evictIfNeededLocked()
}

// Get returns stored function calls for a response id.
func (s *Store) Get(responseID string) ([]FunctionCall, bool) {
	if responseID == "" {
		return nil, false
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[responseID]
	if !ok {
		return nil, false
	}
	e.lastAccess = now
	s.touchLRU(responseID, false, e)

	keys := make([]string, 0, len(e.calls))
	for id := range e.calls {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	out := make([]FunctionCall, 0, len(keys))
	for _, id := range keys {
		c := e.calls[id]
		out = append(out, FunctionCall{
			Type:      c.Type,
			CallID:    c.CallID,
			Name:      c.Name,
			Arguments: c.Arguments,
		})
	}

	return out, true
}

// GetContext returns stored reconstructed input context for a response id.
func (s *Store) GetContext(responseID string) ([]types.ResponsesInputItem, bool) {
	if responseID == "" {
		return nil, false
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[responseID]
	if !ok {
		return nil, false
	}
	e.lastAccess = now
	s.touchLRU(responseID, false, e)

	return cloneInputItems(e.context), true
}

// GetInstructions returns stored effective instructions for a response id.
func (s *Store) GetInstructions(responseID string) (string, bool) {
	if responseID == "" {
		return "", false
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[responseID]
	if !ok {
		return "", false
	}
	e.lastAccess = now
	s.touchLRU(responseID, false, e)

	return e.instructions, true
}

// Exists reports whether response id is present and not expired.
func (s *Store) Exists(responseID string) bool {
	if responseID == "" {
		return false
	}

	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	if e, ok := s.entries[responseID]; ok {
		e.lastAccess = now
		s.touchLRU(responseID, false, e)
		return true
	}
	return false
}

// PutConversationLatest stores latest response id by conversation id.
func (s *Store) PutConversationLatest(conversationID, responseID string) {
	if conversationID == "" || responseID == "" {
		return
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	link, ok := s.conv[conversationID]
	if !ok {
		link = &conversationLink{}
		s.conv[conversationID] = link
	}
	link.responseID = responseID
	link.lastAccess = now
	s.touchConvLRU(conversationID, link)
	s.evictIfNeededLocked()
}

// GetConversationLatest returns latest response id for a conversation id.
func (s *Store) GetConversationLatest(conversationID string) (string, bool) {
	if conversationID == "" {
		return "", false
	}

	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	link, ok := s.conv[conversationID]
	if !ok || link.responseID == "" {
		return "", false
	}
	link.lastAccess = now
	s.touchConvLRU(conversationID, link)
	return link.responseID, true
}

// Len returns current entry count (for tests).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func buildCallMap(calls []FunctionCall) map[string]FunctionCall {
	callMap := make(map[string]FunctionCall)
	for _, c := range calls {
		if c.CallID == "" || c.Name == "" {
			continue
		}
		callMap[c.CallID] = FunctionCall{
			Type:      c.Type,
			CallID:    c.CallID,
			Name:      c.Name,
			Arguments: c.Arguments,
		}
	}
	return callMap
}

func (s *Store) putCallsLocked(responseID string, callMap map[string]FunctionCall, now time.Time) {
	e, ok := s.entries[responseID]
	if !ok {
		e = &entry{}
		s.entries[responseID] = e
	}
	e.calls = callMap
	e.lastAccess = now
	s.touchLRU(responseID, false, e)
}

func (s *Store) putContextLocked(responseID string, ctxCopy []types.ResponsesInputItem, now time.Time) {
	e, ok := s.entries[responseID]
	if !ok {
		e = &entry{}
		s.entries[responseID] = e
	}
	e.context = ctxCopy
	e.lastAccess = now
	s.touchLRU(responseID, false, e)
}

// touchLRU moves or inserts an entry's element to the front of the LRU list.
func (s *Store) touchLRU(id string, isConv bool, e *entry) {
	if e.listElem != nil {
		s.lru.MoveToFront(e.listElem)
	} else {
		e.listElem = s.lru.PushFront(lruKey{id: id, isConv: isConv})
	}
}

// touchConvLRU moves or inserts a conversation link's element to the front.
func (s *Store) touchConvLRU(id string, link *conversationLink) {
	if link.listElem != nil {
		s.lru.MoveToFront(link.listElem)
	} else {
		link.listElem = s.lru.PushFront(lruKey{id: id, isConv: true})
	}
}

func (s *Store) cleanupExpiredLocked(now time.Time) {
	for responseID, e := range s.entries {
		if now.Sub(e.lastAccess) > s.ttl {
			if e.listElem != nil {
				s.lru.Remove(e.listElem)
			}
			delete(s.entries, responseID)
		}
	}
	for conversationID, c := range s.conv {
		if now.Sub(c.lastAccess) > s.ttl {
			if c.listElem != nil {
				s.lru.Remove(c.listElem)
			}
			delete(s.conv, conversationID)
		}
	}
}

func (s *Store) evictIfNeededLocked() {
	for len(s.entries)+len(s.conv) > s.capacity {
		back := s.lru.Back()
		if back == nil {
			return
		}
		key := back.Value.(lruKey)
		s.lru.Remove(back)
		if key.isConv {
			if link, ok := s.conv[key.id]; ok {
				link.listElem = nil
				delete(s.conv, key.id)
			}
		} else {
			if e, ok := s.entries[key.id]; ok {
				e.listElem = nil
				delete(s.entries, key.id)
			}
		}
	}
}

func cloneInputItems(items []types.ResponsesInputItem) []types.ResponsesInputItem {
	if len(items) == 0 {
		return nil
	}

	out := make([]types.ResponsesInputItem, len(items))
	copy(out, items)

	for i := range out {
		if len(items[i].Content) == 0 {
			continue
		}
		contentCopy := make([]types.ResponsesContent, len(items[i].Content))
		copy(contentCopy, items[i].Content)
		out[i].Content = contentCopy
	}

	return out
}
