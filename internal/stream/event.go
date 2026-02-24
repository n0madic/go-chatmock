package stream

import "encoding/json"

// Event represents a single SSE event from the upstream.
type Event struct {
	Type string
	Raw  json.RawMessage
	Data map[string]any
}
