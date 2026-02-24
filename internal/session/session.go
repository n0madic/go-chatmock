package session

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/n0madic/go-chatmock/internal/types"
)

const maxEntries = 10000

// SessionStore holds the fingerprint→sessionID cache with FIFO eviction.
type SessionStore struct {
	mu             sync.Mutex
	fingerprintMap map[string]string
	lru            *list.List
	lruIndex       map[string]*list.Element
}

// NewSessionStore creates a new session store.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		fingerprintMap: make(map[string]string),
		lru:            list.New(),
		lruIndex:       make(map[string]*list.Element),
	}
}

// Len returns the number of cached entries (for tests).
func (ss *SessionStore) Len() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return len(ss.fingerprintMap)
}

// EnsureSessionID returns a deterministic session ID based on the instructions and input items.
// If a client-supplied session ID is provided, it is used as-is.
//
// The deterministic ID enables prompt caching on the upstream: the same
// instructions + first user message always produce the same session key,
// so the ChatGPT backend can reuse cached computation across turns even
// though we never send the previous_response_id in the upstream request.
func (ss *SessionStore) EnsureSessionID(instructions string, inputItems []types.ResponsesInputItem, clientSupplied string) string {
	if clientSupplied != "" {
		return clientSupplied
	}

	canon := canonicalizePrefix(instructions, inputItems)
	fp := fingerprint(canon)

	ss.mu.Lock()
	defer ss.mu.Unlock()

	if sid, ok := ss.fingerprintMap[fp]; ok {
		return sid
	}

	sid := uuid.New().String()
	ss.fingerprintMap[fp] = sid
	elem := ss.lru.PushFront(fp)
	ss.lruIndex[fp] = elem

	if ss.lru.Len() > maxEntries {
		back := ss.lru.Back()
		if back != nil {
			oldest := back.Value.(string)
			ss.lru.Remove(back)
			delete(ss.lruIndex, oldest)
			delete(ss.fingerprintMap, oldest)
		}
	}
	return sid
}

var defaultStore = NewSessionStore()

// EnsureSessionID is a package-level convenience that delegates to the default store.
func EnsureSessionID(instructions string, inputItems []types.ResponsesInputItem, clientSupplied string) string {
	return defaultStore.EnsureSessionID(instructions, inputItems, clientSupplied)
}

// canonicalizePrefix builds a stable string from only the session-invariant parts of
// the request: instructions and the first user message. Subsequent turns in the
// conversation must map to the same session ID so upstream prompt caching is effective.
// Including later messages would produce a new session ID every turn, defeating caching.
func canonicalizePrefix(instructions string, inputItems []types.ResponsesInputItem) string {
	prefix := make(map[string]any)
	if instructions != "" {
		prefix["instructions"] = instructions
	}
	firstUser := canonicalizeFirstUserMessage(inputItems)
	if firstUser != nil {
		prefix["first_user_message"] = firstUser
	}

	// Sort keys for deterministic output
	keys := make([]string, 0, len(prefix))
	for k := range prefix {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	data, _ := json.Marshal(prefix)
	return string(data)
}

func canonicalizeFirstUserMessage(inputItems []types.ResponsesInputItem) map[string]any {
	for _, item := range inputItems {
		if item.Type != "message" {
			continue
		}
		if item.Role != "user" {
			continue
		}
		if len(item.Content) == 0 {
			continue
		}
		var normContent []map[string]any
		for _, part := range item.Content {
			switch part.Type {
			case "input_text":
				if part.Text != "" {
					normContent = append(normContent, map[string]any{"type": "input_text", "text": part.Text})
				}
			case "input_image":
				if part.ImageURL != "" {
					normContent = append(normContent, map[string]any{"type": "input_image", "image_url": part.ImageURL})
				}
			}
		}
		if len(normContent) > 0 {
			return map[string]any{
				"type":    "message",
				"role":    "user",
				"content": normContent,
			}
		}
	}
	return nil
}

func fingerprint(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
