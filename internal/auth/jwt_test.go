package auth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + payloadB64 + ".sig"
}

func TestParseJWTClaims(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr bool
		check   func(map[string]any) bool
	}{
		{
			name:    "empty token",
			token:   "",
			wantErr: true,
		},
		{
			name:    "no dots",
			token:   "nodots",
			wantErr: true,
		},
		{
			name:    "too many dots",
			token:   "a.b.c.d",
			wantErr: true,
		},
		{
			name:  "valid token",
			token: makeJWT(map[string]any{"sub": "user123", "email": "test@example.com"}),
			check: func(c map[string]any) bool {
				return c["sub"] == "user123" && c["email"] == "test@example.com"
			},
		},
		{
			name:  "numeric exp claim",
			token: makeJWT(map[string]any{"exp": float64(1700000000)}),
			check: func(c map[string]any) bool {
				exp, ok := c["exp"].(float64)
				return ok && exp == 1700000000
			},
		},
		{
			name:    "invalid base64",
			token:   "header.!!!invalid!!!.sig",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims, err := ParseJWTClaims(tt.token)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil && !tt.check(claims) {
				t.Errorf("claims check failed: %v", claims)
			}
		})
	}
}
