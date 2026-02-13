package sse

import (
	"io"
	"strings"
	"testing"
)

func TestReader(t *testing.T) {
	stream := `data: {"type":"response.output_text.delta","delta":"Hello"}

data: {"type":"response.output_text.delta","delta":" world"}

data: {"type":"response.completed","response":{"id":"resp_123"}}

data: [DONE]

`
	reader := NewReader(strings.NewReader(stream))

	// First event
	evt, err := reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Type != "response.output_text.delta" {
		t.Errorf("expected response.output_text.delta, got %s", evt.Type)
	}
	delta, _ := evt.Data["delta"].(string)
	if delta != "Hello" {
		t.Errorf("expected Hello, got %s", delta)
	}

	// Second event
	evt, err = reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	delta, _ = evt.Data["delta"].(string)
	if delta != " world" {
		t.Errorf("expected ' world', got %s", delta)
	}

	// Third event
	evt, err = reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Type != "response.completed" {
		t.Errorf("expected response.completed, got %s", evt.Type)
	}

	// DONE
	_, err = reader.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestReaderEmptyLines(t *testing.T) {
	stream := `
data: {"type":"response.output_text.delta","delta":"test"}

data: [DONE]

`
	reader := NewReader(strings.NewReader(stream))
	evt, err := reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Type != "response.output_text.delta" {
		t.Errorf("expected response.output_text.delta, got %s", evt.Type)
	}
}

func TestReaderInvalidJSON(t *testing.T) {
	stream := `data: not json
data: {"type":"valid","delta":"ok"}
data: [DONE]
`
	reader := NewReader(strings.NewReader(stream))
	evt, err := reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if evt.Type != "valid" {
		t.Errorf("expected valid, got %s", evt.Type)
	}
}
