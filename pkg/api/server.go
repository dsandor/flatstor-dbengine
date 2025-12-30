package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
	"github.com/dsandor/flatstor/dbengine/pkg/engine"
)

// Server is the HTTP API server for dbengine.
type Server struct {
	engine     *engine.Engine
	auth       *AuthManager
	httpServer *http.Server
	mu         sync.RWMutex
}

// Config holds server configuration.
type Config struct {
	Addr     string // Listen address (e.g., ":8080")
	Username string // Basic auth username
	Password string // Basic auth password

	// JWT token configuration
	JWTSigningKey        string        // Secret key for signing tokens (optional, random if empty)
	AccessTokenDuration  time.Duration // Access token validity (default: 1 hour)
	RefreshTokenDuration time.Duration // Refresh token validity (default: 24 hours)
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
	}

	mux := http.NewServeMux()

	// Health check (no auth required)
	mux.HandleFunc("/health", s.handleHealth)

	// Auth endpoints (login requires credentials, refresh requires valid refresh token)
	mux.HandleFunc("/api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("/api/v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("/api/v1/auth/logout", s.withAuth(s.handleLogout))

	// API endpoints (auth required - supports both Basic Auth and Bearer token)
	mux.HandleFunc("/api/v1/query", s.withAuth(s.handleQuery))
	mux.HandleFunc("/api/v1/execute", s.withAuth(s.handleExecute))
	mux.HandleFunc("/api/v1/tables", s.withAuth(s.handleTables))
	mux.HandleFunc("/api/v1/tables/", s.withAuth(s.handleTableOperations))
	mux.HandleFunc("/api/v1/rows/", s.withAuth(s.handleRowOperations))

	s.httpServer = &http.Server{
		Addr:         cfg.Addr,
		Handler:      s.withLogging(s.withKeepAlive(mux)),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start background token cleanup
	go s.tokenCleanupLoop()

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

// tokenCleanupLoop periodically cleans up expired revoked tokens.
func (s *Server) tokenCleanupLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		s.auth.CleanupRevokedTokens()
	}
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	log.Printf("API server starting on %s", s.httpServer.Addr)
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
		wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(wrapped, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, wrapped.statusCode, time.Since(start))
	})
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
	Error   string `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes an error response.
func (s *Server) writeError(w http.ResponseWriter, status int, code, message string) {
	s.writeJSON(w, status, ErrorResponse{
		Error:   http.StatusText(status),
		Code:    code,
		Message: message,
	})
}

// writeJSON writes a JSON response.
func (s *Server) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
