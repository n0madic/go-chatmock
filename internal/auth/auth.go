package auth

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TokenData represents the tokens stored in auth.json.
type TokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// AuthFile represents the full auth.json contents.
type AuthFile struct {
	Tokens      TokenData `json:"tokens"`
	LastRefresh string    `json:"last_refresh"`
}

// HomeDir returns the auth storage directory path.
func HomeDir() string {
	if d := os.Getenv("CHATGPT_LOCAL_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".chatgpt-local")
}

// ReadAuthFile searches known locations for auth.json.
func ReadAuthFile() (*AuthFile, error) {
	home, _ := os.UserHomeDir()
	candidates := []string{
		os.Getenv("CHATGPT_LOCAL_HOME"),
		os.Getenv("CODEX_HOME"),
		filepath.Join(home, ".chatgpt-local"),
		filepath.Join(home, ".codex"),
	}
	for _, base := range candidates {
		if base == "" {
			continue
		}
		p := filepath.Join(base, "auth.json")
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var af AuthFile
		if err := json.Unmarshal(data, &af); err != nil {
			continue
		}
		return &af, nil
	}
	return nil, ErrNoCredentials
}

// WriteAuthFile persists the auth data to the home directory with 0600 permissions.
func WriteAuthFile(af *AuthFile) error {
	dir := HomeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("unable to create auth home directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "auth.json")
	return os.WriteFile(p, data, 0o600)
}

// DeriveAccountID extracts the ChatGPT account ID from an id_token's claims.
func DeriveAccountID(idToken string) string {
	if idToken == "" {
		return ""
	}
	claims, err := ParseJWTClaims(idToken)
	if err != nil {
		return ""
	}
	authClaims, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	if aid, ok := authClaims["chatgpt_account_id"].(string); ok {
		return aid
	}
	return ""
}

// TokenManager handles thread-safe token access and refresh.
type TokenManager struct {
	mu       sync.Mutex
	clientID string
	tokenURL string
}

// NewTokenManager creates a new token manager with the given OAuth config.
func NewTokenManager(clientID, tokenURL string) *TokenManager {
	return &TokenManager{
		clientID: clientID,
		tokenURL: tokenURL,
	}
}

// GetEffectiveAuth returns the access token and account ID, refreshing if needed.
func (tm *TokenManager) GetEffectiveAuth() (accessToken, accountID string, err error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	af, err := ReadAuthFile()
	if err != nil {
		return "", "", ErrNoCredentials
	}

	accessToken = af.Tokens.AccessToken
	accountID = af.Tokens.AccountID
	idToken := af.Tokens.IDToken
	refreshToken := af.Tokens.RefreshToken

	if tm.clientID != "" && refreshToken != "" {
		needsRefresh := shouldRefreshAccessToken(accessToken, af.LastRefresh)
		if needsRefresh || accessToken == "" {
			refreshed, err := refreshChatGPTTokens(refreshToken, tm.clientID, tm.tokenURL)
			if err != nil {
				slog.Error("failed to refresh tokens", "error", err)
			} else {
				if refreshed.AccessToken != "" {
					accessToken = refreshed.AccessToken
				}
				if refreshed.IDToken != "" {
					idToken = refreshed.IDToken
				}
				if refreshed.RefreshToken != "" {
					refreshToken = refreshed.RefreshToken
				}
				if refreshed.AccountID != "" {
					accountID = refreshed.AccountID
				}

				af.Tokens.AccessToken = accessToken
				af.Tokens.IDToken = idToken
				af.Tokens.RefreshToken = refreshToken
				af.Tokens.AccountID = accountID
				af.LastRefresh = time.Now().UTC().Format(time.RFC3339)

				if err := WriteAuthFile(af); err != nil {
					slog.Error("unable to persist refreshed auth tokens", "error", err)
				}
			}
		}
	}

	if accountID == "" {
		accountID = DeriveAccountID(idToken)
	}

	return accessToken, accountID, nil
}

// shouldRefreshAccessToken checks if the access token needs refreshing.
func shouldRefreshAccessToken(accessToken, lastRefresh string) bool {
	if accessToken == "" {
		return true
	}

	claims, err := ParseJWTClaims(accessToken)
	if err == nil {
		if exp, ok := claims["exp"].(float64); ok {
			expiry := time.Unix(int64(exp), 0)
			return time.Until(expiry) <= 5*time.Minute
		}
	}

	if lastRefresh != "" {
		t, err := time.Parse(time.RFC3339, lastRefresh)
		if err == nil {
			return time.Since(t) >= 55*time.Minute
		}
	}

	return false
}

// NowISO8601 returns the current UTC time in ISO 8601 format.
func NowISO8601() string {
	return time.Now().UTC().Format(time.RFC3339)
}
