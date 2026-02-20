package session

import (
	"fmt"
	"testing"

	"github.com/n0madic/go-chatmock/internal/types"
)

// resetCache clears the in-memory LRU cache. Call from test code only.
func resetCache() {
	mu.Lock()
	defer mu.Unlock()
	fingerprintMap = make(map[string]string)
	order = nil
}

// TestClientSuppliedSessionIDPassthrough verifies that a non-empty clientSupplied
// value is returned as-is without consulting the cache.
func TestClientSuppliedSessionIDPassthrough(t *testing.T) {
	resetCache()
	want := "my-session-id"
	got := EnsureSessionID("instructions", nil, want)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestDeterministicForSameInput verifies that identical inputs always yield
// the same session ID.
func TestDeterministicForSameInput(t *testing.T) {
	resetCache()
	items := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}}},
	}
	id1 := EnsureSessionID("sys", items, "")
	id2 := EnsureSessionID("sys", items, "")
	if id1 != id2 {
		t.Errorf("same input produced different IDs: %q vs %q", id1, id2)
	}
}

// TestDifferentInputsDifferentIDs verifies that differing inputs yield different IDs.
func TestDifferentInputsDifferentIDs(t *testing.T) {
	resetCache()
	items1 := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}}},
	}
	items2 := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "goodbye"}}},
	}
	id1 := EnsureSessionID("sys", items1, "")
	id2 := EnsureSessionID("sys", items2, "")
	if id1 == id2 {
		t.Errorf("different inputs produced the same ID: %q", id1)
	}
}

// TestDifferentInstructionsDifferentIDs verifies that different system instructions
// produce different session IDs.
func TestDifferentInstructionsDifferentIDs(t *testing.T) {
	resetCache()
	items := []types.ResponsesInputItem{
		{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "hi"}}},
	}
	id1 := EnsureSessionID("instructions A", items, "")
	id2 := EnsureSessionID("instructions B", items, "")
	if id1 == id2 {
		t.Errorf("different instructions produced the same ID: %q", id1)
	}
}

// TestLRUEvictionAtMaxEntries verifies that the cache does not grow beyond maxEntries
// and that the oldest entry is evicted when the limit is reached.
func TestLRUEvictionAtMaxEntries(t *testing.T) {
	resetCache()

	// Fill the cache to capacity using unique instructions.
	firstID := EnsureSessionID("instr-0", nil, "")
	for i := 1; i < maxEntries; i++ {
		EnsureSessionID(fmt.Sprintf("instr-%d", i), nil, "")
	}

	mu.Lock()
	sizeBefore := len(fingerprintMap)
	mu.Unlock()

	if sizeBefore != maxEntries {
		t.Fatalf("expected cache size %d before overflow, got %d", maxEntries, sizeBefore)
	}

	// Adding one more entry should evict the oldest (instr-0).
	EnsureSessionID("instr-overflow", nil, "")

	mu.Lock()
	sizeAfter := len(fingerprintMap)
	mu.Unlock()

	if sizeAfter != maxEntries {
		t.Errorf("expected cache size %d after eviction, got %d", maxEntries, sizeAfter)
	}

	// The first entry should have been evicted; looking it up should return a new ID.
	newID := EnsureSessionID("instr-0", nil, "")
	if newID == firstID {
		t.Errorf("evicted entry was found in cache with the same ID: %q", firstID)
	}
}

// TestCacheGrowsOnNewEntries verifies that each unique input adds exactly one entry.
func TestCacheGrowsOnNewEntries(t *testing.T) {
	resetCache()

	for i := 0; i < 5; i++ {
		EnsureSessionID(fmt.Sprintf("unique-%d", i), nil, "")
	}

	mu.Lock()
	size := len(fingerprintMap)
	mu.Unlock()

	if size != 5 {
		t.Errorf("expected cache size 5, got %d", size)
	}
}

// TestNilInputItems verifies that nil input items are handled without panic.
func TestNilInputItems(t *testing.T) {
	resetCache()
	id := EnsureSessionID("instructions", nil, "")
	if id == "" {
		t.Error("expected non-empty session ID for nil input items")
	}
}

// TestEmptyInstructionsAndInput verifies behaviour with no instructions and no items.
func TestEmptyInstructionsAndInput(t *testing.T) {
	resetCache()
	id1 := EnsureSessionID("", nil, "")
	id2 := EnsureSessionID("", nil, "")
	if id1 != id2 {
		t.Errorf("empty inputs should yield the same ID; got %q and %q", id1, id2)
	}
}
