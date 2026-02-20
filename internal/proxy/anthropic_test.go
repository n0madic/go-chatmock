package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n0madic/go-chatmock/internal/auth"
	"github.com/n0madic/go-chatmock/internal/config"
	"github.com/n0madic/go-chatmock/internal/models"
	"github.com/n0madic/go-chatmock/internal/upstream"
)

func TestIsAnthropicRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	if isAnthropicRequest(req) {
		t.Fatal("expected false when anthropic headers are missing")
	}

	req.Header.Set("anthropic-version", "2023-06-01")
	if !isAnthropicRequest(req) {
		t.Fatal("expected true when anthropic-version is present")
	}
}

func TestValidateAnthropicHeadersAuthVariants(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		ok     bool
	}{
		{
			name: "x-api-key",
			header: map[string]string{
				"anthropic-version": "2023-06-01",
				"x-api-key":         "test-key",
			},
			ok: true,
		},
		{
			name: "authorization bearer",
			header: map[string]string{
				"anthropic-version": "2023-06-01",
				"Authorization":     "Bearer test-token",
			},
			ok: true,
		},
		{
			name: "proxy authorization",
			header: map[string]string{
				"anthropic-version":   "2023-06-01",
				"Proxy-Authorization": "Bearer test-proxy-token",
			},
			ok: true,
		},
		{
			name: "missing auth",
			header: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			ok: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			got := validateAnthropicHeaders(w, req)
			if got != tc.ok {
				t.Fatalf("validateAnthropicHeaders() = %v, want %v body=%s", got, tc.ok, w.Body.String())
			}
		})
	}
}

func TestHandleAnthropicCountTokens(t *testing.T) {
	s := &Server{}
	body := `{
		"model":"claude-sonnet-4",
		"system":"You are helpful",
		"messages":[{"role":"user","content":"Hello"}],
		"tools":[{"name":"read_file","description":"Read file","input_schema":{"type":"object"}}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewBufferString(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", "test-key")
	w := httptest.NewRecorder()

	s.handleAnthropicCountTokens(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"input_tokens":`) {
		t.Fatalf("expected input_tokens in response, got %s", w.Body.String())
	}
}

func TestCollectAnthropicMessageWithToolUse(t *testing.T) {
	s := &Server{}
	stream := `data: {"type":"response.created","response":{"id":"resp_abc"}}

data: {"type":"response.output_text.delta","delta":"Working..."}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"}}

data: {"type":"response.completed","response":{"id":"resp_abc","usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17}}}
`
	resp := &upstream.Response{
		StatusCode: http.StatusOK,
		Body:       &http.Response{Body: io.NopCloser(strings.NewReader(stream))},
	}
	w := httptest.NewRecorder()

	s.collectAnthropicMessage(w, resp, "claude-3-5-sonnet")

	out := w.Body.String()
	if !strings.Contains(out, `"id":"resp_abc"`) {
		t.Fatalf("expected upstream response id in output, got %s", out)
	}
	if !strings.Contains(out, `"type":"tool_use"`) {
		t.Fatalf("expected tool_use block in output, got %s", out)
	}
	if !strings.Contains(out, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected stop_reason tool_use, got %s", out)
	}
}

func TestCollectAnthropicMessageWithObjectToolArguments(t *testing.T) {
	s := &Server{}
	stream := `data: {"type":"response.created","response":{"id":"resp_obj"}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_obj","name":"Glob","arguments":{"pattern":"**/*.go"}}}

data: {"type":"response.completed","response":{"id":"resp_obj","usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}
`
	resp := &upstream.Response{
		StatusCode: http.StatusOK,
		Body:       &http.Response{Body: io.NopCloser(strings.NewReader(stream))},
	}
	w := httptest.NewRecorder()

	s.collectAnthropicMessage(w, resp, "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Glob"`) {
		t.Fatalf("expected tool name in output, got %s", out)
	}
	if !strings.Contains(out, `"pattern":"**/*.go"`) {
		t.Fatalf("expected object arguments to be preserved, got %s", out)
	}
}

func TestCollectAnthropicMessageWithDeltaToolArguments(t *testing.T) {
	s := &Server{}
	stream := `data: {"type":"response.created","response":{"id":"resp_delta"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_delta","call_id":"call_delta","name":"Bash","arguments":""}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_delta","delta":"{\"command\":\"pwd\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_delta","call_id":"call_delta","name":"Bash"}}

data: {"type":"response.completed","response":{"id":"resp_delta","usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}
`
	resp := &upstream.Response{
		StatusCode: http.StatusOK,
		Body:       &http.Response{Body: io.NopCloser(strings.NewReader(stream))},
	}
	w := httptest.NewRecorder()

	s.collectAnthropicMessage(w, resp, "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Bash"`) {
		t.Fatalf("expected tool name in output, got %s", out)
	}
	if !strings.Contains(out, `"command":"pwd"`) {
		t.Fatalf("expected merged delta arguments in output, got %s", out)
	}
}

func TestCollectAnthropicMessageDeltaOverridesPlaceholderArguments(t *testing.T) {
	s := &Server{}
	stream := `data: {"type":"response.created","response":{"id":"resp_delta_override"}}

data: {"type":"response.output_item.added","item":{"type":"function_call","id":"item_override","call_id":"call_override","name":"Glob","arguments":"{}"}}

data: {"type":"response.function_call_arguments.delta","item_id":"item_override","delta":"{\"pattern\":\"**/*.go\"}"}

data: {"type":"response.output_item.done","item":{"type":"function_call","id":"item_override","call_id":"call_override","name":"Glob"}}

data: {"type":"response.completed","response":{"id":"resp_delta_override","usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}
`
	resp := &upstream.Response{
		StatusCode: http.StatusOK,
		Body:       &http.Response{Body: io.NopCloser(strings.NewReader(stream))},
	}
	w := httptest.NewRecorder()

	s.collectAnthropicMessage(w, resp, "claude-sonnet-4")

	out := w.Body.String()
	if !strings.Contains(out, `"name":"Glob"`) {
		t.Fatalf("expected tool name in output, got %s", out)
	}
	if !strings.Contains(out, `"pattern":"**/*.go"`) {
		t.Fatalf("expected delta arguments to override placeholder {}, got %s", out)
	}
}

func TestHandleListModelsAnthropicFormat(t *testing.T) {
	tm := auth.NewTokenManager(config.ClientID(), config.TokenURL())
	s := &Server{
		Config:   &config.ServerConfig{},
		Registry: models.NewRegistry(tm),
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", "test-key")
	w := httptest.NewRecorder()

	s.handleListModels(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	if !strings.Contains(out, `"has_more":false`) {
		t.Fatalf("expected anthropic model list schema, got %s", out)
	}
	if !strings.Contains(out, `"type":"model"`) {
		t.Fatalf("expected model entries in response, got %s", out)
	}
}
