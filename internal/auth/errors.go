package auth

import "errors"

var (
	ErrInvalidJWT    = errors.New("invalid JWT token")
	ErrNoCredentials = errors.New("no credentials found; run 'login' first")
	ErrRefreshFailed = errors.New("token refresh failed")
)
