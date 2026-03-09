package codec

import (
	"net/http"

	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// Format identifies the API wire format of a request/response.
type Format int

const (
	FormatChatCompletions Format = iota
	FormatResponses
	FormatTextCompletions
	FormatAnthropic
	FormatOllama
)

// StreamOpts carries per-request streaming configuration to encoders.
type StreamOpts struct {
	ReasoningCompat string
	IncludeUsage    bool
	CreatedAt       string // Ollama: RFC3339 timestamp for NDJSON chunks
}

// CollectedResponse holds a fully-assembled non-streaming upstream response.
type CollectedResponse struct {
	ResponseID       string
	FullText         string
	ReasoningSummary string
	ReasoningFull    string
	ToolCalls        []types.ToolCall
	OutputItems      []types.ResponsesOutputItem
	Usage            *types.Usage
	ErrorMessage     string
	// RawResponse is the full upstream response object for passthrough formats.
	RawResponse map[string]any
}

// Translator is the streaming translation interface. Implementations read
// upstream SSE events from a stream.Reader and write client-format output.
type Translator interface {
	Translate(reader *stream.Reader)
}

// Decoder converts a raw request body into a CanonicalRequest.
type Decoder interface {
	Decode(body []byte) (*types.CanonicalRequest, error)
	Format() Format
}

// Encoder writes responses in a specific API format.
type Encoder interface {
	WriteStreamHeaders(w http.ResponseWriter, statusCode int)
	StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator
	WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string)
	WriteError(w http.ResponseWriter, statusCode int, message string)
}

// Codec pairs a Decoder and Encoder for a given API format.
type Codec struct {
	Decoder Decoder
	Encoder Encoder
}
