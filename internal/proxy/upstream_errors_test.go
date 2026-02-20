package proxy

import (
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
