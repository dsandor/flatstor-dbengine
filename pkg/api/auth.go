package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/hkdf"
)

// Security constants for authentication
const (
	// MinSigningKeyLength is the minimum required length for a signing key
	MinSigningKeyLength = 32

	// DefaultAccessTokenDuration is the default access token validity (15 minutes)
	DefaultAccessTokenDuration = 15 * time.Minute

	// DefaultRefreshTokenDuration is the default refresh token validity (24 hours)
	DefaultRefreshTokenDuration = 24 * time.Hour

	// MaxRevokedTokens is the maximum number of revoked tokens to store before emergency cleanup
	MaxRevokedTokens = 10000
)

// TokenConfig holds JWT token configuration.
type TokenConfig struct {
	// SigningKey is the secret key used to sign tokens.
	// SECURITY: Must be at least 32 bytes (256 bits) for production use.
	// If empty, a random ephemeral key is generated (tokens invalidated on restart).
	SigningKey string

	// AccessTokenDuration is how long access tokens are valid.
	// Default: 15 minutes (reduced from 1 hour for better security)
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
	var signingKey []byte

	if cfg.SigningKey != "" {
		// Validate provided signing key
		if len(cfg.SigningKey) < MinSigningKeyLength {
			log.Printf("WARNING: JWT signing key is shorter than %d bytes. Using key derivation for security.", MinSigningKeyLength)
		}
		// Use HKDF to derive a secure key from the provided secret
		signingKey = deriveSigningKey([]byte(cfg.SigningKey))
	} else {
		// Generate random ephemeral signing key
		log.Printf("WARNING: No JWT signing key provided. Generating random ephemeral key. Tokens will be invalidated on server restart.")
		signingKey = make([]byte, 32)
		n, err := rand.Read(signingKey)
		if err != nil || n != 32 {
			// This should never happen, but handle it gracefully
			log.Printf("CRITICAL: Failed to generate secure random key: %v", err)
			// Fall back to time-based entropy (not ideal but better than nothing)
			signingKey = deriveSigningKey([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		}
	}

	accessDuration := cfg.AccessTokenDuration
	if accessDuration == 0 {
		accessDuration = DefaultAccessTokenDuration
	}

	refreshDuration := cfg.RefreshTokenDuration
	if refreshDuration == 0 {
		refreshDuration = DefaultRefreshTokenDuration
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

// deriveSigningKey uses HKDF to derive a secure signing key from the provided secret.
func deriveSigningKey(secret []byte) []byte {
	// Use HKDF with SHA-256 to derive a 32-byte key
	hkdfReader := hkdf.New(sha256.New, secret, []byte("dbengine-jwt-salt"), []byte("jwt-signing-key"))
	key := make([]byte, 32)
	_, err := hkdfReader.Read(key)
	if err != nil {
		// This should never happen with valid parameters
		log.Printf("CRITICAL: Failed to derive signing key: %v", err)
		return secret[:32] // Fall back to truncated secret
	}
	return key
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

	// Check for Bearer token first
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

	return a.AuthenticateCredentials(username, password)
}

// AuthenticateCredentials validates username and password using constant-time comparison.
// This prevents timing attacks that could reveal valid usernames.
func (a *AuthManager) AuthenticateCredentials(username, password string) bool {
	if !a.enabled {
		return true
	}

	// Pad inputs to consistent lengths to prevent timing differences
	// based on string length comparisons
	maxLen := len(a.username)
	if len(a.password) > maxLen {
		maxLen = len(a.password)
	}
	if len(username) > maxLen {
		maxLen = len(username)
	}
	if len(password) > maxLen {
		maxLen = len(password)
	}

	// Create padded versions for constant-time comparison
	expectedUser := make([]byte, maxLen)
	expectedPass := make([]byte, maxLen)
	providedUser := make([]byte, maxLen)
	providedPass := make([]byte, maxLen)

	copy(expectedUser, a.username)
	copy(expectedPass, a.password)
	copy(providedUser, username)
	copy(providedPass, password)

	// Use constant-time comparison to prevent timing attacks
	// Both comparisons are always performed to prevent timing leaks
	usernameMatch := subtle.ConstantTimeCompare(providedUser, expectedUser)
	passwordMatch := subtle.ConstantTimeCompare(providedPass, expectedPass)

	// Use bitwise AND to ensure both must match (no short-circuit)
	return (usernameMatch & passwordMatch) == 1
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
// Also performs emergency cleanup if the list grows too large.
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

	// Emergency cleanup if list is still too large (prevents DoS via token accumulation)
	if len(a.revokedTokens) > MaxRevokedTokens {
		log.Printf("WARNING: Revoked token list exceeds %d entries, performing emergency cleanup", MaxRevokedTokens)
		// Remove oldest half of the tokens
		// This is a safety measure - in production, use persistent storage
		count := 0
		halfMax := MaxRevokedTokens / 2
		for token := range a.revokedTokens {
			if count >= halfMax {
				break
			}
			delete(a.revokedTokens, token)
			count++
		}
	}
}

// RevokedTokenCount returns the current number of revoked tokens (for monitoring).
func (a *AuthManager) RevokedTokenCount() int {
	a.revokedMu.RLock()
	defer a.revokedMu.RUnlock()
	return len(a.revokedTokens)
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
