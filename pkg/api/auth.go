package api

import (
	"crypto/subtle"
	"net/http"
)

// AuthManager handles authentication for the API.
type AuthManager struct {
	username string
	password string
	enabled  bool
}

// NewAuthManager creates a new AuthManager.
// If username and password are empty, authentication is disabled.
func NewAuthManager(username, password string) *AuthManager {
	return &AuthManager{
		username: username,
		password: password,
		enabled:  username != "" && password != "",
	}
}

// Authenticate checks if the request has valid credentials.
func (a *AuthManager) Authenticate(r *http.Request) bool {
	// If auth is disabled, allow all requests
	if !a.enabled {
		return true
	}

	// Check Basic Auth
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}

	// Use constant-time comparison to prevent timing attacks
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1

	return usernameMatch && passwordMatch
}

// IsEnabled returns whether authentication is enabled.
func (a *AuthManager) IsEnabled() bool {
	return a.enabled
}
