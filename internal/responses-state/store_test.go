package responsesstate

import (
	"sync"
	"testing"
	"time"

	"github.com/n0madic/go-chatmock/internal/types"
)

func TestStorePutGet(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.Put("resp_1", []FunctionCall{
		{CallID: "call_b", Name: "write_file", Arguments: `{"path":"x"}`},
		{CallID: "call_a", Name: "read_file", Arguments: `{"path":"y"}`},
	})

	got, ok := s.Get("resp_1")
	if !ok {
		t.Fatal("expected entry to exist")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(got))
	}
	if got[0].CallID != "call_a" || got[1].CallID != "call_b" {
		t.Fatalf("expected sorted call IDs [call_a call_b], got [%s %s]", got[0].CallID, got[1].CallID)
	}
}

func TestStorePutGetContext(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.PutContext("resp_1", []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "hello"}},
		},
	})

	got, ok := s.GetContext("resp_1")
	if !ok {
		t.Fatal("expected context entry to exist")
	}
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("unexpected context: %+v", got)
	}
}

func TestStorePutSnapshotStoresBothCallsAndContext(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.PutSnapshot(
		"resp_1",
		[]types.ResponsesInputItem{
			{Type: "message", Role: "user", Content: []types.ResponsesContent{{Type: "input_text", Text: "run"}}},
		},
		[]FunctionCall{{CallID: "call_1", Name: "read_file", Arguments: `{"path":"README.md"}`}},
	)

	calls, ok := s.Get("resp_1")
	if !ok || len(calls) != 1 || calls[0].CallID != "call_1" {
		t.Fatalf("unexpected calls: ok=%v calls=%+v", ok, calls)
	}
	context, ok := s.GetContext("resp_1")
	if !ok || len(context) != 1 || context[0].Type != "message" {
		t.Fatalf("unexpected context: ok=%v context=%+v", ok, context)
	}
}

func TestStorePutGetInstructions(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.PutInstructions("resp_1", "You are strict")

	got, ok := s.GetInstructions("resp_1")
	if !ok {
		t.Fatal("expected instructions entry to exist")
	}
	if got != "You are strict" {
		t.Fatalf("unexpected instructions: %q", got)
	}
	if !s.Exists("resp_1") {
		t.Fatal("expected entry to exist")
	}
}

func TestStoreExpiry(t *testing.T) {
	s := NewStore(25*time.Millisecond, 10)
	s.Put("resp_1", []FunctionCall{{CallID: "call_1", Name: "read_file"}})
	time.Sleep(40 * time.Millisecond)

	if _, ok := s.Get("resp_1"); ok {
		t.Fatal("expected entry to expire")
	}
}

func TestStoreCapacityEvictionByLeastRecentAccess(t *testing.T) {
	s := NewStore(5*time.Minute, 2)
	s.Put("resp_1", []FunctionCall{{CallID: "call_1", Name: "read_file"}})
	time.Sleep(5 * time.Millisecond)
	s.Put("resp_2", []FunctionCall{{CallID: "call_2", Name: "write_file"}})
	time.Sleep(5 * time.Millisecond)

	// Touch resp_1 so resp_2 becomes least-recently-used.
	if _, ok := s.Get("resp_1"); !ok {
		t.Fatal("expected resp_1 to exist")
	}
	time.Sleep(5 * time.Millisecond)
	s.Put("resp_3", []FunctionCall{{CallID: "call_3", Name: "edit_file"}})

	if _, ok := s.Get("resp_2"); ok {
		t.Fatal("expected resp_2 to be evicted as LRU")
	}
	if _, ok := s.Get("resp_1"); !ok {
		t.Fatal("expected resp_1 to remain")
	}
	if _, ok := s.Get("resp_3"); !ok {
		t.Fatal("expected resp_3 to exist")
	}
}

func TestStoreGetReturnsCopy(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.Put("resp_1", []FunctionCall{{CallID: "call_1", Name: "read_file"}})

	got, ok := s.Get("resp_1")
	if !ok {
		t.Fatal("expected entry")
	}
	got[0].Name = "mutated"

	got2, ok := s.Get("resp_1")
	if !ok {
		t.Fatal("expected entry")
	}
	if got2[0].Name != "read_file" {
		t.Fatalf("expected original value, got %q", got2[0].Name)
	}
}

func TestStoreGetContextReturnsCopy(t *testing.T) {
	s := NewStore(5*time.Minute, 10)
	s.PutContext("resp_1", []types.ResponsesInputItem{
		{
			Type:    "message",
			Role:    "user",
			Content: []types.ResponsesContent{{Type: "input_text", Text: "original"}},
		},
	})

	got, ok := s.GetContext("resp_1")
	if !ok {
		t.Fatal("expected entry")
	}
	got[0].Content[0].Text = "mutated"

	got2, ok := s.GetContext("resp_1")
	if !ok {
		t.Fatal("expected entry")
	}
	if got2[0].Content[0].Text != "original" {
		t.Fatalf("expected original value, got %q", got2[0].Content[0].Text)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	s := NewStore(5*time.Minute, 1000)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "resp_" + string(rune('a'+(i%26)))
			s.Put(id, []FunctionCall{{CallID: "call_" + string(rune('a'+(i%26))), Name: "read_file"}})
			s.Get(id)
		}(i)
	}
	wg.Wait()

	if s.Len() == 0 {
		t.Fatal("expected store to contain entries")
	}
}
