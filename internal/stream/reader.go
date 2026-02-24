package stream

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// Reader reads SSE events from an io.Reader.
type Reader struct {
	scanner *bufio.Scanner
}

// NewReader creates a new SSE reader.
func NewReader(r io.Reader) *Reader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	return &Reader{scanner: scanner}
}

// Next returns the next SSE event. Returns nil, io.EOF when done.
func (r *Reader) Next() (*Event, error) {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(line[6:])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return nil, io.EOF
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		eventType, _ := parsed["type"].(string)
		return &Event{
			Type: eventType,
			Raw:  json.RawMessage(data),
			Data: parsed,
		}, nil
	}
	if err := r.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
