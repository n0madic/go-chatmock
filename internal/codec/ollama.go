package codec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/stream"
	"github.com/n0madic/go-chatmock/internal/types"
)

// OllamaEncoder encodes responses in Ollama NDJSON format.
type OllamaEncoder struct{}

func (e *OllamaEncoder) WriteStreamHeaders(w http.ResponseWriter, statusCode int) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(statusCode)
}

func (e *OllamaEncoder) StreamTranslator(w http.ResponseWriter, model string, opts StreamOpts) Translator {
	return &ollamaStreamTranslator{w: w, model: model, opts: opts}
}

func (e *OllamaEncoder) WriteCollected(w http.ResponseWriter, statusCode int, resp *CollectedResponse, model string) {
	if resp.ErrorMessage != "" {
		WriteOllamaError(w, http.StatusBadGateway, resp.ErrorMessage)
		return
	}

	fullText := resp.FullText
	compat := ""
	if resp.RawResponse != nil {
		if c, ok := resp.RawResponse["_reasoning_compat"].(string); ok {
			compat = c
		}
	}
	if compat == "" || compat == "think-tags" {
		var parts []string
		if resp.ReasoningSummary != "" {
			parts = append(parts, resp.ReasoningSummary)
		}
		if resp.ReasoningFull != "" {
			parts = append(parts, resp.ReasoningFull)
		}
		if len(parts) > 0 {
			var rtxt strings.Builder
			for i, p := range parts {
				if i > 0 {
					rtxt.WriteString("\n\n")
				}
				rtxt.WriteString(p)
			}
			fullText = "<think>" + rtxt.String() + "</think>" + fullText
		}
	}

	createdAt := ""
	if resp.RawResponse != nil {
		if c, ok := resp.RawResponse["_created_at"].(string); ok {
			createdAt = c
		}
	}

	chunk := types.OllamaStreamChunk{
		Model:          model,
		CreatedAt:      createdAt,
		Message:        types.OllamaMessage{Role: "assistant", Content: fullText, ToolCalls: resp.ToolCalls},
		Done:           true,
		DoneReason:     "stop",
		OllamaFakeEval: types.OllamaFakeEvalDefaults,
	}
	WriteJSON(w, statusCode, chunk)
}

func (e *OllamaEncoder) WriteError(w http.ResponseWriter, statusCode int, message string) {
	WriteOllamaError(w, statusCode, message)
}

// ollamaStreamTranslator translates upstream SSE into Ollama NDJSON chunks.
type ollamaStreamTranslator struct {
	w     http.ResponseWriter
	model string
	opts  StreamOpts
}

func (t *ollamaStreamTranslator) Translate(reader *stream.Reader) {
	flusher, ok := t.w.(http.Flusher)
	if !ok {
		return
	}

	compat := strings.ToLower(strings.TrimSpace(t.opts.ReasoningCompat))
	if compat == "" {
		compat = "think-tags"
	}

	thinkOpen := false
	thinkClosed := false
	sawAnySummary := false
	pendingSummaryParagraph := false

	createdAt := t.opts.CreatedAt

	writeMsg := func(content string, done bool) {
		chunk := types.OllamaStreamChunk{
			Model:     t.model,
			CreatedAt: createdAt,
			Message:   types.OllamaMessage{Role: "assistant", Content: content},
			Done:      done,
		}
		if done {
			chunk.OllamaFakeEval = types.OllamaFakeEvalDefaults
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(t.w, "%s\n", data)
		flusher.Flush()
	}

	gotEvents := false
	for {
		evt, err := reader.Next()
		if err != nil {
			break
		}
		gotEvents = true

		switch evt.Type {
		case "response.reasoning_summary_part.added":
			if compat == "think-tags" || compat == "o3" {
				if sawAnySummary {
					pendingSummaryParagraph = true
				} else {
					sawAnySummary = true
				}
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			deltaTxt, _ := evt.Data["delta"].(string)
			switch compat {
			case "o3":
				if evt.Type == "response.reasoning_summary_text.delta" && pendingSummaryParagraph {
					writeMsg("\n", false)
					pendingSummaryParagraph = false
				}
				if deltaTxt != "" {
					writeMsg(deltaTxt, false)
				}
			case "think-tags":
				if !thinkOpen && !thinkClosed {
					writeMsg("<think>", false)
					thinkOpen = true
				}
				if thinkOpen && !thinkClosed {
					if evt.Type == "response.reasoning_summary_text.delta" && pendingSummaryParagraph {
						writeMsg("\n", false)
						pendingSummaryParagraph = false
					}
					if deltaTxt != "" {
						writeMsg(deltaTxt, false)
					}
				}
			}

		case "response.output_text.delta":
			delta, _ := evt.Data["delta"].(string)
			if compat == "think-tags" && thinkOpen && !thinkClosed {
				writeMsg("</think>", false)
				thinkOpen = false
				thinkClosed = true
			}
			if delta != "" {
				writeMsg(delta, false)
			}

		case "response.completed":
			if compat == "think-tags" && thinkOpen && !thinkClosed {
				writeMsg("</think>", false)
			}
			writeMsg("", true)
			return
		}
	}

	// Stream ended without response.completed
	if !gotEvents {
		errChunk := types.OllamaStreamChunk{
			Model: t.model, CreatedAt: createdAt,
			Message: types.OllamaMessage{Role: "assistant", Content: "Error: upstream returned empty response"},
			Done:    true,
		}
		errChunk.OllamaFakeEval = types.OllamaFakeEvalDefaults
		data, _ := json.Marshal(errChunk)
		fmt.Fprintf(t.w, "%s\n", data)
		flusher.Flush()
		return
	}
	if compat == "think-tags" && thinkOpen && !thinkClosed {
		writeMsg("</think>", false)
	}
	writeMsg("", true)
}
