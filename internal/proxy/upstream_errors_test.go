package proxy

import (
	"net/http"
	"strings"
	"testing"
)

func TestFormatUpstreamError(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		body         string
		wantContains []string
	}{
		{
			name:       "openai error envelope",
			statusCode: 429,
			body:       `{"error":{"message":"Rate limit exceeded"}}`,
			wantContains: []string{
				"HTTP 429 Too Many Requests",
				"Rate limit exceeded",
			},
		},
		{
			name:       "generic message field",
			statusCode: 403,
			body:       `{"message":"Forbidden by policy"}`,
			wantContains: []string{
				"HTTP 403 Forbidden",
				"Forbidden by policy",
			},
		},
		{
			name:       "nested errors array",
			statusCode: 400,
			body:       `{"errors":[{"detail":"bad schema"}]}`,
			wantContains: []string{
				"HTTP 400 Bad Request",
				"bad schema",
			},
		},
		{
			name:       "raw text body",
			statusCode: 502,
			body:       "gateway overloaded\nplease retry later",
			wantContains: []string{
				"HTTP 502 Bad Gateway",
				"unparsed body",
				"gateway overloaded please retry later",
			},
		},
		{
			name:       "empty body",
			statusCode: 500,
			body:       "",
			wantContains: []string{
				"HTTP 500 Internal Server Error",
				"empty error body",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatUpstreamError(tt.statusCode, []byte(tt.body))
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("expected %q to contain %q", got, want)
				}
			}
		})
	}
}

func TestFormatUpstreamErrorWithHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-request-id", "req_123")

	got := formatUpstreamErrorWithHeaders(http.StatusBadRequest, []byte(``), headers)
	if !strings.Contains(got, "request_id: req_123") {
		t.Fatalf("expected request id in message, got %q", got)
	}

	gotNoHeader := formatUpstreamErrorWithHeaders(http.StatusBadRequest, []byte(``), nil)
	if strings.Contains(gotNoHeader, "request_id:") {
		t.Fatalf("did not expect request id without headers, got %q", gotNoHeader)
	}
}

func TestFormatUpstreamErrorWithHeadersFallbackHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-oai-request-id", "oai_456")

	got := formatUpstreamErrorWithHeaders(http.StatusBadRequest, []byte(``), headers)
	if !strings.Contains(got, "request_id: oai_456") {
		t.Fatalf("expected fallback request id in message, got %q", got)
	}
}
