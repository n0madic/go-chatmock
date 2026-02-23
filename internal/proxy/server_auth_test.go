package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/n0madic/go-chatmock/internal/config"
)

func TestAuthMiddlewareAccessTokenValidation(t *testing.T) {
	const okStatus = http.StatusTeapot

	cases := []struct {
		name           string
		method         string
		path           string
		accessToken    string
		headers        map[string]string
		wantStatusCode int
		wantContains   string
	}{
		{
			name:           "no configured access token bypasses middleware",
			method:         http.MethodPost,
			path:           "/v1/responses",
			wantStatusCode: okStatus,
		},
		{
			name:           "health root bypasses middleware",
			method:         http.MethodGet,
			path:           "/",
			accessToken:    "secret-token",
			wantStatusCode: okStatus,
		},
		{
			name:           "health endpoint bypasses middleware",
			method:         http.MethodGet,
			path:           "/health",
			accessToken:    "secret-token",
			wantStatusCode: okStatus,
		},
		{
			name:           "options requests bypass middleware",
			method:         http.MethodOptions,
			path:           "/v1/responses",
			accessToken:    "secret-token",
			wantStatusCode: okStatus,
		},
		{
			name:           "non api path bypasses middleware",
			method:         http.MethodGet,
			path:           "/favicon.ico",
			accessToken:    "secret-token",
			wantStatusCode: okStatus,
		},
		{
			name:           "missing auth header returns unauthorized",
			method:         http.MethodPost,
			path:           "/v1/responses",
			accessToken:    "secret-token",
			wantStatusCode: http.StatusUnauthorized,
			wantContains:   serverAccessTokenError,
		},
		{
			name:        "wrong bearer token returns unauthorized",
			method:      http.MethodPost,
			path:        "/v1/responses",
			accessToken: "secret-token",
			headers: map[string]string{
				"Authorization": "Bearer wrong-token",
			},
			wantStatusCode: http.StatusUnauthorized,
			wantContains:   serverAccessTokenError,
		},
		{
			name:        "non bearer auth returns unauthorized",
			method:      http.MethodPost,
			path:        "/v1/responses",
			accessToken: "secret-token",
			headers: map[string]string{
				"Authorization": "Basic abc123",
			},
			wantStatusCode: http.StatusUnauthorized,
			wantContains:   serverAccessTokenError,
		},
		{
			name:        "matching bearer token passes",
			method:      http.MethodPost,
			path:        "/v1/responses",
			accessToken: "secret-token",
			headers: map[string]string{
				"Authorization": "Bearer secret-token",
			},
			wantStatusCode: okStatus,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Config: &config.ServerConfig{AccessToken: tc.accessToken}}
			handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(okStatus)
			}))

			req := httptest.NewRequest(tc.method, tc.path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != tc.wantStatusCode {
				t.Fatalf("status: got %d, want %d body=%s", w.Code, tc.wantStatusCode, w.Body.String())
			}
			if tc.wantContains != "" && !strings.Contains(w.Body.String(), tc.wantContains) {
				t.Fatalf("expected body to contain %q, got %s", tc.wantContains, w.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareUnauthorizedResponseFormats(t *testing.T) {
	s := &Server{Config: &config.ServerConfig{AccessToken: "secret-token"}}
	handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	cases := []struct {
		name         string
		path         string
		headers      map[string]string
		wantContains []string
	}{
		{
			name: "openai style response",
			path: "/v1/responses",
			wantContains: []string{
				`"error":{"message":"` + serverAccessTokenError + `"}`,
			},
		},
		{
			name: "anthropic style response",
			path: "/v1/messages",
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
			wantContains: []string{
				`"type":"error"`,
				`"type":"authentication_error"`,
				`"message":"` + serverAccessTokenError + `"`,
			},
		},
		{
			name: "ollama style response",
			path: "/api/chat",
			wantContains: []string{
				`"error":"` + serverAccessTokenError + `"`,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status: got %d, want %d body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(w.Body.String(), want) {
					t.Fatalf("expected body to contain %q, got %s", want, w.Body.String())
				}
			}
		})
	}
}

func TestParseBearerAuthToken(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		wantToken string
		wantOK    bool
	}{
		{
			name:      "valid bearer token",
			header:    "Bearer abc123",
			wantToken: "abc123",
			wantOK:    true,
		},
		{
			name:      "valid bearer token with extra spaces",
			header:    "  Bearer   abc123  ",
			wantToken: "abc123",
			wantOK:    true,
		},
		{
			name:   "lowercase bearer rejected",
			header: "bearer abc123",
			wantOK: false,
		},
		{
			name:   "basic auth rejected",
			header: "Basic abc123",
			wantOK: false,
		},
		{
			name:   "empty token rejected",
			header: "Bearer ",
			wantOK: false,
		},
		{
			name:   "extra parts rejected",
			header: "Bearer one two",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotToken, gotOK := parseBearerAuthToken(tt.header)
			if gotOK != tt.wantOK {
				t.Fatalf("ok: got %v, want %v", gotOK, tt.wantOK)
			}
			if gotToken != tt.wantToken {
				t.Fatalf("token: got %q, want %q", gotToken, tt.wantToken)
			}
		})
	}
}
