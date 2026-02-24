package responsesstate

import (
	"sort"
	"sync"
	"time"

	"github.com/n0madic/go-chatmock/internal/types"
)

const (
	DefaultTTL      = 60 * time.Minute
	DefaultCapacity = 10000
)

// FunctionCall stores a function_call item so it can be replayed in a future request.
type FunctionCall struct {
	CallID    string
	Name      string
	Arguments string
}

type entry struct {
	calls        map[string]FunctionCall
	context      []types.ResponsesInputItem
	instructions string
	lastAccess   time.Time
}

// Store keeps per-response function_call state for local previous_response_id polyfill.
type Store struct {
	mu       sync.Mutex
	entries  map[string]*entry
	conv     map[string]conversationLink
	ttl      time.Duration
	capacity int
}

type conversationLink struct {
	responseID string
	lastAccess time.Time
}

// NewStore creates an in-memory state store with TTL and capacity limits.
func NewStore(ttl time.Duration, capacity int) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Store{
		entries:  make(map[string]*entry),
		conv:     make(map[string]conversationLink),
		ttl:      ttl,
		capacity: capacity,
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

	s.cleanupExpiredLocked(now)
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

	s.cleanupExpiredLocked(now)
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

	s.cleanupExpiredLocked(now)
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

	s.cleanupExpiredLocked(now)
	e, ok := s.entries[responseID]
	if !ok {
		e = &entry{}
		s.entries[responseID] = e
	}
	e.instructions = instructions
	e.lastAccess = now
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

	s.cleanupExpiredLocked(now)

	e, ok := s.entries[responseID]
	if !ok {
		return nil, false
	}
	e.lastAccess = now

	keys := make([]string, 0, len(e.calls))
	for id := range e.calls {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	out := make([]FunctionCall, 0, len(keys))
	for _, id := range keys {
		c := e.calls[id]
		out = append(out, FunctionCall{
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

	s.cleanupExpiredLocked(now)

	e, ok := s.entries[responseID]
	if !ok {
		return nil, false
	}
	e.lastAccess = now

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

	s.cleanupExpiredLocked(now)

	e, ok := s.entries[responseID]
	if !ok {
		return "", false
	}
	e.lastAccess = now

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

	s.cleanupExpiredLocked(now)
	if e, ok := s.entries[responseID]; ok {
		e.lastAccess = now
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

	s.cleanupExpiredLocked(now)
	s.conv[conversationID] = conversationLink{
		responseID: responseID,
		lastAccess: now,
	}
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

	s.cleanupExpiredLocked(now)
	link, ok := s.conv[conversationID]
	if !ok || link.responseID == "" {
		return "", false
	}
	link.lastAccess = now
	s.conv[conversationID] = link
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
}

func (s *Store) putContextLocked(responseID string, ctxCopy []types.ResponsesInputItem, now time.Time) {
	e, ok := s.entries[responseID]
	if !ok {
		e = &entry{}
		s.entries[responseID] = e
	}
	e.context = ctxCopy
	e.lastAccess = now
}

func (s *Store) cleanupExpiredLocked(now time.Time) {
	for responseID, e := range s.entries {
		if now.Sub(e.lastAccess) > s.ttl {
			delete(s.entries, responseID)
		}
	}
	for conversationID, c := range s.conv {
		if now.Sub(c.lastAccess) > s.ttl {
			delete(s.conv, conversationID)
		}
	}
}

func (s *Store) evictIfNeededLocked() {
	for len(s.entries)+len(s.conv) > s.capacity {
		var oldestID string
		var oldestAt time.Time
		oldestIsConv := false
		first := true
		for responseID, e := range s.entries {
			if first || e.lastAccess.Before(oldestAt) {
				oldestID = responseID
				oldestAt = e.lastAccess
				oldestIsConv = false
				first = false
			}
		}
		for conversationID, c := range s.conv {
			if first || c.lastAccess.Before(oldestAt) {
				oldestID = conversationID
				oldestAt = c.lastAccess
				oldestIsConv = true
				first = false
			}
		}
		if oldestID == "" {
			return
		}
		if oldestIsConv {
			delete(s.conv, oldestID)
		} else {
			delete(s.entries, oldestID)
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
