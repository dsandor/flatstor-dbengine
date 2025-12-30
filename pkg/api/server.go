package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
	"github.com/dsandor/flatstor/dbengine/pkg/engine"
)

// Security constants
const (
	// MaxRequestBodySize limits request body size to prevent DoS (10MB)
	MaxRequestBodySize = 10 * 1024 * 1024

	// RateLimitRequests is the max requests per window
	RateLimitRequests = 100

	// RateLimitWindow is the time window for rate limiting
	RateLimitWindow = time.Minute

	// LoginRateLimitRequests is max login attempts per window
	LoginRateLimitRequests = 5

	// LoginRateLimitWindow is the time window for login rate limiting
	LoginRateLimitWindow = 15 * time.Minute
)

// validIdentifierRegex validates table and row identifiers to prevent SQL injection
var validIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)

// validRowIDRegex validates row IDs (more permissive but still safe)
var validRowIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,255}$`)

// RateLimiter tracks request rates per IP
type RateLimiter struct {
	requests map[string][]time.Time
	mu       sync.RWMutex
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
	}
}

// Allow checks if a request from the given IP should be allowed
func (rl *RateLimiter) Allow(ip string, limit int, window time.Duration) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	// Get existing requests and filter out old ones
	existing := rl.requests[ip]
	var recent []time.Time
	for _, t := range existing {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	// Check if limit exceeded
	if len(recent) >= limit {
		rl.requests[ip] = recent
		return false
	}

	// Add current request
	recent = append(recent, now)
	rl.requests[ip] = recent
	return true
}

// Cleanup removes old entries from the rate limiter
func (rl *RateLimiter) Cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-LoginRateLimitWindow)
	for ip, times := range rl.requests {
		var recent []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = recent
		}
	}
}

// Server is the HTTP API server for dbengine.
type Server struct {
	engine      *engine.Engine
	auth        *AuthManager
	httpServer  *http.Server
	rateLimiter *RateLimiter
	config      Config
	mu          sync.RWMutex
}

// Config holds server configuration.
type Config struct {
	Addr     string // Listen address (e.g., ":8080")
	Username string // Basic auth username
	Password string // Basic auth password

	// JWT token configuration
	JWTSigningKey        string        // Secret key for signing tokens (required for production)
	AccessTokenDuration  time.Duration // Access token validity (default: 15 minutes)
	RefreshTokenDuration time.Duration // Refresh token validity (default: 24 hours)

	// TLS configuration
	TLSEnabled  bool   // Enable TLS/HTTPS
	TLSCertFile string // Path to TLS certificate file
	TLSKeyFile  string // Path to TLS private key file

	// CORS configuration
	AllowedOrigins []string // Allowed CORS origins (empty = no CORS)

	// Security settings
	DebugMode bool // Enable verbose error messages (disable in production)
}

// New creates a new API server.
func New(eng *engine.Engine, cfg Config) *Server {
	s := &Server{
		engine: eng,
		auth: NewAuthManagerWithConfig(cfg.Username, cfg.Password, TokenConfig{
			SigningKey:           cfg.JWTSigningKey,
			AccessTokenDuration:  cfg.AccessTokenDuration,
			RefreshTokenDuration: cfg.RefreshTokenDuration,
		}),
		rateLimiter: NewRateLimiter(),
		config:      cfg,
	}

	mux := http.NewServeMux()

	// Health check (no auth required, but rate limited)
	mux.HandleFunc("/health", s.withRateLimit(s.handleHealth))

	// Auth endpoints (login requires credentials, refresh requires valid refresh token)
	// Login has stricter rate limiting to prevent brute force
	mux.HandleFunc("/api/v1/auth/login", s.withLoginRateLimit(s.handleLogin))
	mux.HandleFunc("/api/v1/auth/refresh", s.withRateLimit(s.handleRefresh))
	mux.HandleFunc("/api/v1/auth/logout", s.withAuth(s.handleLogout))

	// API endpoints (auth required - supports both Basic Auth and Bearer token)
	mux.HandleFunc("/api/v1/query", s.withAuth(s.withRateLimit(s.handleQuery)))
	mux.HandleFunc("/api/v1/execute", s.withAuth(s.withRateLimit(s.handleExecute)))
	mux.HandleFunc("/api/v1/tables", s.withAuth(s.withRateLimit(s.handleTables)))
	mux.HandleFunc("/api/v1/tables/", s.withAuth(s.withRateLimit(s.handleTableOperations)))
	mux.HandleFunc("/api/v1/rows/", s.withAuth(s.withRateLimit(s.handleRowOperations)))

	// Build middleware chain: Security Headers -> CORS -> Request Size Limit -> Logging -> Keep-Alive -> Handler
	handler := s.withSecurityHeaders(
		s.withCORS(
			s.withRequestSizeLimit(
				s.withLogging(
					s.withKeepAlive(mux)))))

	s.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB max header size
	}

	// Configure TLS if enabled
	if cfg.TLSEnabled {
		s.httpServer.TLSConfig = &tls.Config{
			MinVersion:               tls.VersionTLS12,
			PreferServerCipherSuites: true,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			},
		}
	}

	// Start background cleanup loops
	go s.tokenCleanupLoop()
	go s.rateLimiterCleanupLoop()

	return s
}

// withKeepAlive ensures connection keep-alive headers are set.
func (s *Server) withKeepAlive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enable keep-alive
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Keep-Alive", "timeout=120, max=1000")
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders adds security headers to all responses.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")

		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// XSS protection for legacy browsers
		w.Header().Set("X-XSS-Protection", "1; mode=block")

		// Content Security Policy - restrict resource loading
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		// Referrer policy - don't leak URLs
		w.Header().Set("Referrer-Policy", "no-referrer")

		// Permissions policy - disable unnecessary browser features
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// HSTS - enforce HTTPS (only if TLS is enabled)
		if s.config.TLSEnabled || r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}

		// Cache control for API responses
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
		w.Header().Set("Pragma", "no-cache")

		next.ServeHTTP(w, r)
	})
}

// withCORS handles Cross-Origin Resource Sharing.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// If no allowed origins configured, deny all CORS requests
		if len(s.config.AllowedOrigins) == 0 {
			// No CORS headers - browser will block cross-origin requests
			next.ServeHTTP(w, r)
			return
		}

		// Check if origin is allowed
		allowed := false
		for _, allowedOrigin := range s.config.AllowedOrigins {
			if allowedOrigin == "*" || allowedOrigin == origin {
				allowed = true
				break
			}
		}

		if !allowed && origin != "" {
			// Origin not in allowlist
			s.writeError(w, http.StatusForbidden, "cors_error", "Origin not allowed")
			return
		}

		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Correlation-ID")
			w.Header().Set("Access-Control-Max-Age", "3600")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}

		// Handle preflight OPTIONS request
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// withRequestSizeLimit limits the size of request bodies.
func (s *Server) withRequestSizeLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > MaxRequestBodySize {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("Request body exceeds maximum size of %d bytes", MaxRequestBodySize))
			return
		}

		// Wrap body with size limiter
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
		next.ServeHTTP(w, r)
	})
}

// withRateLimit applies rate limiting per IP address.
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)

		if !s.rateLimiter.Allow(ip, RateLimitRequests, RateLimitWindow) {
			w.Header().Set("Retry-After", "60")
			s.writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
				"Too many requests. Please try again later.")
			s.logSecurityEvent("rate_limit_exceeded", ip, r.URL.Path, false, "general rate limit")
			return
		}

		next(w, r)
	}
}

// withLoginRateLimit applies stricter rate limiting for login attempts.
func (s *Server) withLoginRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)

		if !s.rateLimiter.Allow(ip+":login", LoginRateLimitRequests, LoginRateLimitWindow) {
			w.Header().Set("Retry-After", "900") // 15 minutes
			s.writeError(w, http.StatusTooManyRequests, "login_rate_limit_exceeded",
				"Too many login attempts. Please try again in 15 minutes.")
			s.logSecurityEvent("login_rate_limit_exceeded", ip, "/api/v1/auth/login", false, "brute force protection")
			return
		}

		next(w, r)
	}
}

// withContentType validates Content-Type header for requests with bodies.
func (s *Server) withContentType(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			contentType := r.Header.Get("Content-Type")
			if !strings.Contains(contentType, "application/json") {
				s.writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type",
					"Content-Type must be application/json")
				return
			}
		}
		next(w, r)
	}
}

// getClientIP extracts the client IP address from the request.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (for reverse proxies)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the chain
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	// Remove port if present
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

// generateCorrelationID creates a unique ID for request tracking.
func generateCorrelationID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// tokenCleanupLoop periodically cleans up expired revoked tokens.
func (s *Server) tokenCleanupLoop() {
	// Cleanup every 5 minutes instead of 1 hour for better memory management
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.auth.CleanupRevokedTokens()
	}
}

// rateLimiterCleanupLoop periodically cleans up old rate limiter entries.
func (s *Server) rateLimiterCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.rateLimiter.Cleanup()
	}
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	if s.config.TLSEnabled {
		log.Printf("API server starting on %s (TLS enabled)", s.httpServer.Addr)
		return s.httpServer.ListenAndServeTLS(s.config.TLSCertFile, s.config.TLSKeyFile)
	}

	log.Printf("API server starting on %s", s.httpServer.Addr)
	log.Printf("WARNING: Running without TLS - credentials will be transmitted in cleartext. Use TLS in production.")
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's listen address.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// withLogging wraps a handler with request logging.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		correlationID := r.Header.Get("X-Correlation-ID")
		if correlationID == "" {
			correlationID = generateCorrelationID()
		}

		// Add correlation ID to response
		w.Header().Set("X-Correlation-ID", correlationID)

		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)

		// Security-aware logging - redact sensitive headers
		duration := time.Since(start)
		ip := getClientIP(r)

		// Log request (never log Authorization header contents)
		log.Printf("[%s] %s %s %s %d %s",
			correlationID, ip, r.Method, r.URL.Path, wrapped.statusCode, duration)

		// Log security events for failed requests
		if wrapped.statusCode >= 400 {
			s.logSecurityEvent("http_error", ip, r.URL.Path, false,
				fmt.Sprintf("status=%d method=%s", wrapped.statusCode, r.Method))
		}
	})
}

// logSecurityEvent logs security-relevant events in a structured format.
func (s *Server) logSecurityEvent(eventType, ip, resource string, success bool, details string) {
	status := "FAILURE"
	if success {
		status = "SUCCESS"
	}
	log.Printf("[SECURITY] event=%s status=%s ip=%s resource=%s details=%s",
		eventType, status, ip, resource, details)
}

// withAuth wraps a handler with authentication.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Authenticate(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="dbengine"`)
			s.writeError(w, http.StatusUnauthorized, "unauthorized", "Invalid credentials")
			return
		}
		next(w, r)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// handleHealth handles health check requests.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is allowed")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"service": "dbengine",
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// LoginRequest represents a login request.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin handles user login and returns JWT tokens.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
		return
	}

	if req.Username == "" || req.Password == "" {
		s.writeError(w, http.StatusBadRequest, "missing_credentials", "Username and password are required")
		return
	}

	// Validate credentials
	if !s.auth.AuthenticateCredentials(req.Username, req.Password) {
		s.writeError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password")
		return
	}

	// Generate token pair
	tokens, err := s.auth.GenerateTokenPair(req.Username, []string{"read", "write"})
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "token_error", "Failed to generate tokens")
		return
	}

	s.writeJSON(w, http.StatusOK, tokens)
}

// RefreshRequest represents a token refresh request.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// handleRefresh handles token refresh requests.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
		return
	}

	if req.RefreshToken == "" {
		s.writeError(w, http.StatusBadRequest, "missing_token", "Refresh token is required")
		return
	}

	// Generate new token pair
	tokens, err := s.auth.RefreshTokens(req.RefreshToken)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "invalid_token", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, tokens)
}

// LogoutRequest represents a logout request.
type LogoutRequest struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// handleLogout handles user logout by revoking tokens.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	var req LogoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow empty body - just revoke the token from the header
		req = LogoutRequest{}
	}

	// Revoke tokens if provided
	if req.AccessToken != "" {
		s.auth.RevokeToken(req.AccessToken)
	}
	if req.RefreshToken != "" {
		s.auth.RevokeToken(req.RefreshToken)
	}

	// Also revoke the token used in the Authorization header
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		s.auth.RevokeToken(token)
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "Logged out successfully",
	})
}

// QueryRequest represents a SQL query request.
type QueryRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

// QueryResponse represents a query response.
type QueryResponse struct {
	Columns      []string         `json:"columns"`
	Rows         []map[string]any `json:"rows"`
	RowCount     int              `json:"row_count"`
	ExecutionMs  int64            `json:"execution_ms"`
	TranslatedSQL string          `json:"translated_sql,omitempty"`
}

// handleQuery handles SELECT queries.
func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
		return
	}

	if req.SQL == "" {
		s.writeError(w, http.StatusBadRequest, "missing_sql", "SQL query is required")
		return
	}

	// Only allow SELECT queries through /query endpoint
	sqlUpper := strings.ToUpper(strings.TrimSpace(req.SQL))
	if !strings.HasPrefix(sqlUpper, "SELECT") {
		s.writeError(w, http.StatusBadRequest, "invalid_query", "Only SELECT queries are allowed. Use /execute for other statements.")
		return
	}

	start := time.Now()
	result, err := s.engine.Query(r.Context(), req.SQL)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "query_error", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, QueryResponse{
		Columns:       result.Columns,
		Rows:          result.Rows,
		RowCount:      len(result.Rows),
		ExecutionMs:   time.Since(start).Milliseconds(),
		TranslatedSQL: result.SQL,
	})
}

// ExecuteRequest represents a SQL execute request.
type ExecuteRequest struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

// ExecuteResponse represents an execute response.
type ExecuteResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	RowsAffected int64  `json:"rows_affected,omitempty"`
	ExecutionMs  int64  `json:"execution_ms"`
}

// handleExecute handles INSERT, UPDATE, DELETE, and other non-SELECT statements.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
		return
	}

	if req.SQL == "" {
		s.writeError(w, http.StatusBadRequest, "missing_sql", "SQL statement is required")
		return
	}

	start := time.Now()

	// For non-SELECT queries, execute directly on DuckDB
	sqlUpper := strings.ToUpper(strings.TrimSpace(req.SQL))

	var result interface{}
	var err error

	switch {
	case strings.HasPrefix(sqlUpper, "SELECT"):
		// SELECT queries should use /query endpoint, but we'll allow it here too
		result, err = s.engine.Query(r.Context(), req.SQL)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "query_error", err.Error())
			return
		}
		queryResult := result.(*engine.QueryResult)
		s.writeJSON(w, http.StatusOK, QueryResponse{
			Columns:       queryResult.Columns,
			Rows:          queryResult.Rows,
			RowCount:      len(queryResult.Rows),
			ExecutionMs:   time.Since(start).Milliseconds(),
			TranslatedSQL: queryResult.SQL,
		})
		return

	case strings.HasPrefix(sqlUpper, "INSERT"):
		// Parse and execute INSERT
		res, execErr := s.engine.DuckDB().Exec(r.Context(), req.SQL, req.Params...)
		if execErr != nil {
			s.writeError(w, http.StatusBadRequest, "execute_error", execErr.Error())
			return
		}
		rowsAffected, _ := res.RowsAffected()
		s.writeJSON(w, http.StatusOK, ExecuteResponse{
			Success:      true,
			Message:      "Insert successful",
			RowsAffected: rowsAffected,
			ExecutionMs:  time.Since(start).Milliseconds(),
		})
		return

	case strings.HasPrefix(sqlUpper, "UPDATE"):
		res, execErr := s.engine.DuckDB().Exec(r.Context(), req.SQL, req.Params...)
		if execErr != nil {
			s.writeError(w, http.StatusBadRequest, "execute_error", execErr.Error())
			return
		}
		rowsAffected, _ := res.RowsAffected()
		s.writeJSON(w, http.StatusOK, ExecuteResponse{
			Success:      true,
			Message:      "Update successful",
			RowsAffected: rowsAffected,
			ExecutionMs:  time.Since(start).Milliseconds(),
		})
		return

	case strings.HasPrefix(sqlUpper, "DELETE"):
		res, execErr := s.engine.DuckDB().Exec(r.Context(), req.SQL, req.Params...)
		if execErr != nil {
			s.writeError(w, http.StatusBadRequest, "execute_error", execErr.Error())
			return
		}
		rowsAffected, _ := res.RowsAffected()
		s.writeJSON(w, http.StatusOK, ExecuteResponse{
			Success:      true,
			Message:      "Delete successful",
			RowsAffected: rowsAffected,
			ExecutionMs:  time.Since(start).Milliseconds(),
		})
		return

	default:
		// For other statements (CREATE, DROP, etc.)
		_, execErr := s.engine.DuckDB().Exec(r.Context(), req.SQL, req.Params...)
		if execErr != nil {
			s.writeError(w, http.StatusBadRequest, "execute_error", execErr.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, ExecuteResponse{
			Success:     true,
			Message:     "Statement executed successfully",
			ExecutionMs: time.Since(start).Milliseconds(),
		})
		return
	}
}

// TableInfo represents table metadata.
type TableInfo struct {
	Name       string `json:"name"`
	PrimaryKey string `json:"primary_key,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// handleTables handles listing tables.
func (s *Server) handleTables(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tables, err := s.engine.ListTables(r.Context())
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "list_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"tables": tables})

	case http.MethodPost:
		// Create table
		var schema colgroup.TableSchema
		if err := json.NewDecoder(r.Body).Decode(&schema); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}
		if err := s.engine.CreateTable(r.Context(), &schema); err != nil {
			s.writeError(w, http.StatusBadRequest, "create_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Table %s created", schema.Name),
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and POST are allowed")
	}
}

// handleTableOperations handles operations on specific tables.
func (s *Server) handleTableOperations(w http.ResponseWriter, r *http.Request) {
	// Extract table name from path: /api/v1/tables/{tableName}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/tables/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		s.writeError(w, http.StatusBadRequest, "missing_table", "Table name is required")
		return
	}
	tableName := parts[0]

	// Validate table name to prevent SQL injection and path traversal
	if err := validateTableName(tableName); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_table_name", err.Error())
		s.logSecurityEvent("invalid_table_name", getClientIP(r), r.URL.Path, false, tableName)
		return
	}

	// Check for sub-resources
	if len(parts) > 1 {
		switch parts[1] {
		case "schema":
			s.handleTableSchema(w, r, tableName)
			return
		case "load":
			s.handleTableLoad(w, r, tableName)
			return
		case "sync":
			s.handleTableSync(w, r, tableName)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		// Get table info
		schema, err := s.engine.GetTableSchema(r.Context(), tableName)
		if err != nil {
			s.writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, schema)

	case http.MethodDelete:
		// Drop table
		if err := s.engine.DropTable(r.Context(), tableName); err != nil {
			s.writeError(w, http.StatusBadRequest, "drop_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Table %s dropped", tableName),
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and DELETE are allowed")
	}
}

// handleTableSchema returns the schema for a table.
func (s *Server) handleTableSchema(w http.ResponseWriter, r *http.Request, tableName string) {
	if r.Method != http.MethodGet {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is allowed")
		return
	}

	schema, err := s.engine.GetTableSchema(r.Context(), tableName)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, schema)
}

// handleTableLoad loads a table into DuckDB.
func (s *Server) handleTableLoad(w http.ResponseWriter, r *http.Request, tableName string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	start := time.Now()
	if err := s.engine.LoadTable(r.Context(), tableName); err != nil {
		s.writeError(w, http.StatusBadRequest, "load_error", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"message":      fmt.Sprintf("Table %s loaded", tableName),
		"execution_ms": time.Since(start).Milliseconds(),
	})
}

// handleTableSync syncs a table with DuckDB.
func (s *Server) handleTableSync(w http.ResponseWriter, r *http.Request, tableName string) {
	if r.Method != http.MethodPost {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only POST is allowed")
		return
	}

	start := time.Now()
	if err := s.engine.SyncTable(r.Context(), tableName); err != nil {
		s.writeError(w, http.StatusBadRequest, "sync_error", err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"message":      fmt.Sprintf("Table %s synced", tableName),
		"execution_ms": time.Since(start).Milliseconds(),
	})
}

// RowRequest represents a row insert/update request.
type RowRequest struct {
	ID     string                    `json:"_id"`
	Groups map[string]map[string]any `json:"groups,omitempty"`
	Data   map[string]any            `json:"data,omitempty"`
}

// handleRowOperations handles row-level operations.
func (s *Server) handleRowOperations(w http.ResponseWriter, r *http.Request) {
	// Extract path: /api/v1/rows/{tableName}/{rowId}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/rows/")
	parts := strings.Split(path, "/")

	if len(parts) < 1 || parts[0] == "" {
		s.writeError(w, http.StatusBadRequest, "missing_table", "Table name is required")
		return
	}
	tableName := parts[0]

	// Validate table name to prevent SQL injection and path traversal
	if err := validateTableName(tableName); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_table_name", err.Error())
		s.logSecurityEvent("invalid_table_name", getClientIP(r), r.URL.Path, false, tableName)
		return
	}

	// Handle row creation (POST to /api/v1/rows/{tableName})
	if r.Method == http.MethodPost && (len(parts) == 1 || parts[1] == "") {
		var req RowRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}
		if req.ID == "" {
			s.writeError(w, http.StatusBadRequest, "missing_id", "_id field is required")
			return
		}

		row := colgroup.NewRow(req.ID)
		if req.Groups != nil {
			row.Groups = req.Groups
		}
		if req.Data != nil {
			for key, value := range req.Data {
				keyParts := strings.SplitN(key, ".", 2)
				if len(keyParts) == 2 {
					row.SetValue(keyParts[0], keyParts[1], value)
				}
			}
		}

		if err := s.engine.InsertRow(r.Context(), tableName, row); err != nil {
			s.writeError(w, http.StatusBadRequest, "insert_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Row %s inserted", req.ID),
		})
		return
	}

	// Operations on specific row
	if len(parts) < 2 || parts[1] == "" {
		s.writeError(w, http.StatusBadRequest, "missing_row_id", "Row ID is required")
		return
	}
	rowID := parts[1]

	// Validate row ID to prevent injection attacks
	if err := validateRowID(rowID); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_row_id", err.Error())
		s.logSecurityEvent("invalid_row_id", getClientIP(r), r.URL.Path, false, rowID)
		return
	}

	switch r.Method {
	case http.MethodGet:
		row, err := s.engine.GetRow(r.Context(), tableName, rowID)
		if err != nil {
			s.writeError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, row)

	case http.MethodPut:
		var req RowRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_json", "Invalid JSON body")
			return
		}

		row := colgroup.NewRow(rowID)
		if req.Groups != nil {
			row.Groups = req.Groups
		}
		if req.Data != nil {
			for key, value := range req.Data {
				keyParts := strings.SplitN(key, ".", 2)
				if len(keyParts) == 2 {
					row.SetValue(keyParts[0], keyParts[1], value)
				}
			}
		}

		if err := s.engine.UpdateRow(r.Context(), tableName, row); err != nil {
			s.writeError(w, http.StatusBadRequest, "update_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Row %s updated", rowID),
		})

	case http.MethodDelete:
		if err := s.engine.DeleteRow(r.Context(), tableName, rowID); err != nil {
			s.writeError(w, http.StatusBadRequest, "delete_error", err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"message": fmt.Sprintf("Row %s deleted", rowID),
		})

	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

// ErrorResponse represents an error response.
type ErrorResponse struct {
	Error         string `json:"error"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// writeError writes an error response with sanitized error messages.
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	correlationID := w.Header().Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = generateCorrelationID()
		w.Header().Set("X-Correlation-ID", correlationID)
	}

	// Log detailed error internally
	log.Printf("[%s] Error: code=%s message=%s", correlationID, code, message)

	// Sanitize error message for external response
	sanitizedMessage := message
	if !s.config.DebugMode {
		sanitizedMessage = s.sanitizeErrorMessage(code, message)
	}

	s.writeJSON(w, status, ErrorResponse{
		Error:         http.StatusText(status),
		Code:          code,
		Message:       sanitizedMessage,
		CorrelationID: correlationID,
	})
}

// sanitizeErrorMessage returns a generic error message to prevent information disclosure.
func (s *Server) sanitizeErrorMessage(code, originalMessage string) string {
	// Map error codes to generic messages that don't leak internal details
	genericMessages := map[string]string{
		"query_error":       "An error occurred while processing your query",
		"execute_error":     "An error occurred while executing the statement",
		"list_error":        "An error occurred while listing resources",
		"create_error":      "An error occurred while creating the resource",
		"drop_error":        "An error occurred while deleting the resource",
		"load_error":        "An error occurred while loading the resource",
		"sync_error":        "An error occurred while syncing the resource",
		"insert_error":      "An error occurred while inserting the record",
		"update_error":      "An error occurred while updating the record",
		"delete_error":      "An error occurred while deleting the record",
		"not_found":         "The requested resource was not found",
		"invalid_token":     "Authentication failed",
		"token_error":       "Authentication error occurred",
		"invalid_credentials": "Invalid username or password",
	}

	if generic, ok := genericMessages[code]; ok {
		return generic
	}

	// For unknown codes, return the original if it's safe
	// Don't return messages containing SQL, file paths, or stack traces
	if containsSensitiveInfo(originalMessage) {
		return "An error occurred while processing your request"
	}

	return originalMessage
}

// containsSensitiveInfo checks if an error message might contain sensitive information.
func containsSensitiveInfo(message string) bool {
	sensitivePatterns := []string{
		"SELECT", "INSERT", "UPDATE", "DELETE", "FROM", "WHERE",
		"/Users/", "/home/", "/var/", "/etc/",
		"goroutine", "panic", "runtime",
		"sql:", "duckdb:", "error:",
		"column", "table", "index",
		"file", "path", "directory",
	}

	upperMsg := strings.ToUpper(message)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(upperMsg, strings.ToUpper(pattern)) {
			return true
		}
	}
	return false
}

// validateTableName validates a table name to prevent SQL injection.
func validateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("table name is required")
	}
	if !validIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid table name: must start with a letter or underscore, contain only alphanumeric characters and underscores, and be 1-64 characters")
	}
	return nil
}

// validateRowID validates a row ID to prevent injection attacks.
func validateRowID(id string) error {
	if id == "" {
		return fmt.Errorf("row ID is required")
	}
	if !validRowIDRegex.MatchString(id) {
		return fmt.Errorf("invalid row ID: must contain only alphanumeric characters, underscores, and hyphens, and be 1-255 characters")
	}
	return nil
}

// writeJSON writes a JSON response.
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
