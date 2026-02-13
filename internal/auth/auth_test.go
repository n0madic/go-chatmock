package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHomeDir(t *testing.T) {
	orig := os.Getenv("CHATGPT_LOCAL_HOME")
	origCodex := os.Getenv("CODEX_HOME")
	defer func() {
		os.Setenv("CHATGPT_LOCAL_HOME", orig)
		os.Setenv("CODEX_HOME", origCodex)
	}()

	os.Setenv("CHATGPT_LOCAL_HOME", "/tmp/test-home")
	if got := HomeDir(); got != "/tmp/test-home" {
		t.Errorf("expected /tmp/test-home, got %s", got)
	}

	os.Unsetenv("CHATGPT_LOCAL_HOME")
	os.Setenv("CODEX_HOME", "/tmp/codex-home")
	if got := HomeDir(); got != "/tmp/codex-home" {
		t.Errorf("expected /tmp/codex-home, got %s", got)
	}

	os.Unsetenv("CODEX_HOME")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".chatgpt-local")
	if got := HomeDir(); got != expected {
		t.Errorf("expected %s, got %s", expected, got)
	}
}

func TestReadWriteAuthFile(t *testing.T) {
	tmpDir := t.TempDir()
	orig := os.Getenv("CHATGPT_LOCAL_HOME")
	defer os.Setenv("CHATGPT_LOCAL_HOME", orig)
	os.Setenv("CHATGPT_LOCAL_HOME", tmpDir)

	af := &AuthFile{
		APIKey: "sk-test",
		Tokens: TokenData{
			IDToken:      "id.tok.en",
			AccessToken:  "access.tok.en",
			RefreshToken: "refresh_token",
			AccountID:    "acct_123",
		},
		LastRefresh: "2024-01-01T00:00:00Z",
	}

	if err := WriteAuthFile(af); err != nil {
		t.Fatalf("WriteAuthFile failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(tmpDir, "auth.json"))
	if err != nil {
		t.Fatalf("stat auth.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}

	read, err := ReadAuthFile()
	if err != nil {
		t.Fatalf("ReadAuthFile failed: %v", err)
	}
	if read.APIKey != af.APIKey {
		t.Errorf("APIKey mismatch: %s vs %s", read.APIKey, af.APIKey)
	}
	if read.Tokens.AccountID != af.Tokens.AccountID {
		t.Errorf("AccountID mismatch: %s vs %s", read.Tokens.AccountID, af.Tokens.AccountID)
	}
}

func TestDeriveAccountID(t *testing.T) {
	claims := map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_abc123",
		},
	}
	token := makeJWT(claims)
	got := DeriveAccountID(token)
	if got != "acct_abc123" {
		t.Errorf("expected acct_abc123, got %s", got)
	}

	if got := DeriveAccountID(""); got != "" {
		t.Errorf("expected empty for empty token, got %s", got)
	}
}

func TestShouldRefreshAccessToken(t *testing.T) {
	if !shouldRefreshAccessToken("", "") {
		t.Error("empty token should need refresh")
	}

	// Token expiring in 3 minutes - should refresh
	expSoon := makeJWT(map[string]any{"exp": float64(time.Now().Unix() + 180)})
	if !shouldRefreshAccessToken(expSoon, "") {
		t.Error("token expiring in 3 min should need refresh")
	}

	// Token expiring in 30 minutes - should not refresh
	expLater := makeJWT(map[string]any{"exp": float64(time.Now().Unix() + 1800)})
	if shouldRefreshAccessToken(expLater, "") {
		t.Error("token expiring in 30 min should not need refresh")
	}

	// Far future
	farFuture := makeJWT(map[string]any{"exp": float64(9999999999)})
	if shouldRefreshAccessToken(farFuture, "") {
		t.Error("far future token should not need refresh")
	}
}
