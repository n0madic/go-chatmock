package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/n0madic/go-chatmock/internal/types"
)

// TranslateOllamaOptions holds options for Ollama SSE translation.
type TranslateOllamaOptions struct {
	ReasoningCompat string
}

// TranslateOllama reads upstream SSE events and writes Ollama NDJSON chunks.
func TranslateOllama(w http.ResponseWriter, body io.ReadCloser, model, createdAt string, opts TranslateOllamaOptions) {
	defer body.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	reader := NewReader(body)
	compat := strings.ToLower(strings.TrimSpace(opts.ReasoningCompat))
	if compat == "" {
		compat = "think-tags"
	}

	thinkOpen := false
	thinkClosed := false
	sawAnySummary := false
	pendingSummaryParagraph := false

	writeMsg := func(content string, done bool) {
		chunk := types.OllamaStreamChunk{
			Model:     model,
			CreatedAt: createdAt,
			Message:   types.OllamaMessage{Role: "assistant", Content: content},
			Done:      done,
		}
		if done {
			chunk.OllamaFakeEval = types.OllamaFakeEvalDefaults
		}
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "%s\n", data)
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
			Model: model, CreatedAt: createdAt,
			Message: types.OllamaMessage{Role: "assistant", Content: "Error: upstream returned empty response"},
			Done:    true,
		}
		errChunk.OllamaFakeEval = types.OllamaFakeEvalDefaults
		data, _ := json.Marshal(errChunk)
		fmt.Fprintf(w, "%s\n", data)
		flusher.Flush()
		return
	}
	if compat == "think-tags" && thinkOpen && !thinkClosed {
		writeMsg("</think>", false)
	}
	writeMsg("", true)
}
