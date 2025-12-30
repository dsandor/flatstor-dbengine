package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenConfig holds JWT token configuration.
type TokenConfig struct {
	// SigningKey is the secret key used to sign tokens.
	// If empty, a random key is generated on startup.
	SigningKey string

	// AccessTokenDuration is how long access tokens are valid.
	// Default: 1 hour
	AccessTokenDuration time.Duration

	// RefreshTokenDuration is how long refresh tokens are valid.
	// Default: 24 hours
	RefreshTokenDuration time.Duration
}

// Claims represents the JWT claims.
type Claims struct {
	Username    string   `json:"username"`
	Permissions []string `json:"permissions,omitempty"`
	TokenType   string   `json:"token_type"` // "access" or "refresh"
	jwt.RegisteredClaims
}

// TokenPair represents an access token and refresh token pair.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"` // seconds until access token expires
	ExpiresAt    int64  `json:"expires_at"` // unix timestamp when access token expires
}

// AuthManager handles authentication for the API.
type AuthManager struct {
	username string
	password string
	enabled  bool

	// JWT configuration
	signingKey           []byte
	accessTokenDuration  time.Duration
	refreshTokenDuration time.Duration

	// Revoked tokens (in production, use Redis or similar)
	revokedTokens map[string]time.Time
	revokedMu     sync.RWMutex
}

// NewAuthManager creates a new AuthManager.
// If username and password are empty, authentication is disabled.
func NewAuthManager(username, password string) *AuthManager {
	return NewAuthManagerWithConfig(username, password, TokenConfig{})
}

// NewAuthManagerWithConfig creates a new AuthManager with token configuration.
func NewAuthManagerWithConfig(username, password string, cfg TokenConfig) *AuthManager {
	// Generate random signing key if not provided
	signingKey := []byte(cfg.SigningKey)
	if len(signingKey) == 0 {
		signingKey = make([]byte, 32)
		rand.Read(signingKey)
	}

	accessDuration := cfg.AccessTokenDuration
	if accessDuration == 0 {
		accessDuration = 1 * time.Hour
	}

	refreshDuration := cfg.RefreshTokenDuration
	if refreshDuration == 0 {
		refreshDuration = 24 * time.Hour
	}

	return &AuthManager{
		username:             username,
		password:             password,
		enabled:              username != "" && password != "",
		signingKey:           signingKey,
		accessTokenDuration:  accessDuration,
		refreshTokenDuration: refreshDuration,
		revokedTokens:        make(map[string]time.Time),
	}
}

// Authenticate checks if the request has valid credentials.
// Supports both Basic Auth and Bearer token authentication.
func (a *AuthManager) Authenticate(r *http.Request) bool {
	// If auth is disabled, allow all requests
	if !a.enabled {
		return true
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	// Check for Bearer token first (faster validation)
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := a.ValidateToken(token)
		if err != nil {
			return false
		}
		// Only allow access tokens for API requests
		return claims.TokenType == "access"
	}

	// Fall back to Basic Auth
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}

	// Use constant-time comparison to prevent timing attacks
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1

	return usernameMatch && passwordMatch
}

// AuthenticateCredentials validates username and password.
func (a *AuthManager) AuthenticateCredentials(username, password string) bool {
	if !a.enabled {
		return true
	}

	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(a.username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(a.password)) == 1

	return usernameMatch && passwordMatch
}

// GenerateTokenPair creates a new access and refresh token pair.
func (a *AuthManager) GenerateTokenPair(username string, permissions []string) (*TokenPair, error) {
	now := time.Now()
	accessExpiry := now.Add(a.accessTokenDuration)
	refreshExpiry := now.Add(a.refreshTokenDuration)

	// Generate unique token ID
	tokenID := generateTokenID()

	// Create access token
	accessClaims := Claims{
		Username:    username,
		Permissions: permissions,
		TokenType:   "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID + "_access",
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(accessExpiry),
			Issuer:    "dbengine",
		},
	}

	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessTokenString, err := accessToken.SignedString(a.signingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign access token: %w", err)
	}

	// Create refresh token
	refreshClaims := Claims{
		Username:  username,
		TokenType: "refresh",
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID + "_refresh",
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(refreshExpiry),
			Issuer:    "dbengine",
		},
	}

	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshTokenString, err := refreshToken.SignedString(a.signingKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessTokenString,
		RefreshToken: refreshTokenString,
		TokenType:    "Bearer",
		ExpiresIn:    int64(a.accessTokenDuration.Seconds()),
		ExpiresAt:    accessExpiry.Unix(),
	}, nil
}

// ValidateToken validates a JWT token and returns its claims.
func (a *AuthManager) ValidateToken(tokenString string) (*Claims, error) {
	// Check if token is revoked
	a.revokedMu.RLock()
	if _, revoked := a.revokedTokens[tokenString]; revoked {
		a.revokedMu.RUnlock()
		return nil, fmt.Errorf("token has been revoked")
	}
	a.revokedMu.RUnlock()

	// Parse and validate token
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return a.signingKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// RefreshTokens generates a new token pair using a valid refresh token.
func (a *AuthManager) RefreshTokens(refreshTokenString string) (*TokenPair, error) {
	claims, err := a.ValidateToken(refreshTokenString)
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	if claims.TokenType != "refresh" {
		return nil, fmt.Errorf("not a refresh token")
	}

	// Revoke the old refresh token
	a.RevokeToken(refreshTokenString)

	// Generate new token pair
	return a.GenerateTokenPair(claims.Username, claims.Permissions)
}

// RevokeToken adds a token to the revocation list.
func (a *AuthManager) RevokeToken(tokenString string) {
	a.revokedMu.Lock()
	defer a.revokedMu.Unlock()
	a.revokedTokens[tokenString] = time.Now()
}

// CleanupRevokedTokens removes expired tokens from the revocation list.
func (a *AuthManager) CleanupRevokedTokens() {
	a.revokedMu.Lock()
	defer a.revokedMu.Unlock()

	// Remove tokens that were revoked more than refreshTokenDuration ago
	cutoff := time.Now().Add(-a.refreshTokenDuration)
	for token, revokedAt := range a.revokedTokens {
		if revokedAt.Before(cutoff) {
			delete(a.revokedTokens, token)
		}
	}
}

// IsEnabled returns whether authentication is enabled.
func (a *AuthManager) IsEnabled() bool {
	return a.enabled
}

// GetAccessTokenDuration returns the access token duration.
func (a *AuthManager) GetAccessTokenDuration() time.Duration {
	return a.accessTokenDuration
}

// generateTokenID creates a unique token identifier.
func generateTokenID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
