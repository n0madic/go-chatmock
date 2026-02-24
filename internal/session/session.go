package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/n0madic/go-chatmock/internal/types"
)

const maxEntries = 10000

var (
	mu             sync.Mutex
	fingerprintMap = make(map[string]string)
	order          []string
)

// EnsureSessionID returns a deterministic session ID based on the instructions and input items.
// If a client-supplied session ID is provided, it is used as-is.
//
// The deterministic ID enables prompt caching on the upstream: the same
// instructions + first user message always produce the same session key,
// so the ChatGPT backend can reuse cached computation across turns even
// though we never send the previous_response_id in the upstream request.
func EnsureSessionID(instructions string, inputItems []types.ResponsesInputItem, clientSupplied string) string {
	if clientSupplied != "" {
		return clientSupplied
	}

	canon := canonicalizePrefix(instructions, inputItems)
	fp := fingerprint(canon)

	mu.Lock()
	defer mu.Unlock()

	if sid, ok := fingerprintMap[fp]; ok {
		return sid
	}

	sid := uuid.New().String()
	fingerprintMap[fp] = sid
	order = append(order, fp)
	if len(order) > maxEntries {
		// Simple FIFO eviction: drop the oldest fingerprint when the cap is
		// reached. O(n) copy is acceptable because eviction is rare and avoids
		// an external LRU dependency for what is essentially a bounded table.
		oldest := order[0]
		copy(order, order[1:])
		order[len(order)-1] = ""
		order = order[:len(order)-1]
		delete(fingerprintMap, oldest)
	}
	return sid
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
