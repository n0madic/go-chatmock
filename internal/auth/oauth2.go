package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"golang.org/x/oauth2"
)

// NewOAuth2Config creates an oauth2.Config for the ChatGPT OAuth flow.
func NewOAuth2Config(clientID, issuer string) *oauth2.Config {
	return &oauth2.Config{
		ClientID: clientID,
		Endpoint: oauth2.Endpoint{
			AuthURL:   issuer + "/oauth/authorize",
			TokenURL:  issuer + "/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:      []string{"openid", "profile", "email", "offline_access"},
		RedirectURL: "http://localhost:1455/auth/callback",
	}
}

// refreshResult holds the result of a token refresh.
type refreshResult struct {
	AccessToken  string
	IDToken      string
	RefreshToken string
	AccountID    string
}

// tokenRefreshResponse is the JSON response from the token refresh endpoint.
type tokenRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

// tokenExchangeResponse is the JSON response from the token exchange endpoint.
type tokenExchangeResponse struct {
	AccessToken string `json:"access_token"`
}

// refreshChatGPTTokens exchanges a refresh token for new tokens.
// NOTE: kept as manual HTTP because OpenAI's token refresh endpoint
// expects application/json body, not form-encoded.
func refreshChatGPTTokens(refreshToken, clientID, tokenURL string) (*refreshResult, error) {
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"scope":         "openid profile email offline_access",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(tokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("refresh token request returned status %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("unable to read refresh response: %w", err)
	}

	var data tokenRefreshResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("unable to parse refresh response: %w", err)
	}

	newRefresh := data.RefreshToken
	if newRefresh == "" {
		newRefresh = refreshToken
	}

	if data.IDToken == "" || data.AccessToken == "" {
		return nil, ErrRefreshFailed
	}

	accountID := DeriveAccountID(data.IDToken)

	return &refreshResult{
		AccessToken:  data.AccessToken,
		IDToken:      data.IDToken,
		RefreshToken: newRefresh,
		AccountID:    accountID,
	}, nil
}

// TokenExchange performs the urn:ietf:params:oauth:grant-type:token-exchange to get an API key.
// NOTE: kept as manual HTTP because this is a non-standard grant type.
func TokenExchange(tokenEndpoint, clientID, idToken, name string) (string, error) {
	form := url.Values{
		"grant_type":         {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"client_id":          {clientID},
		"requested_token":    {"openai-api-key"},
		"subject_token":      {idToken},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:id_token"},
		"name":               {name},
	}

	resp, err := http.PostForm(tokenEndpoint, form)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("token exchange returned status %d: %s", resp.StatusCode, string(body))
	}

	var result tokenExchangeResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result.AccessToken, nil
}
