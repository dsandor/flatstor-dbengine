// Package dbclient provides a Go client for the dbengine HTTP API.
package dbclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a dbengine HTTP API client with connection pooling and token caching.
type Client struct {
	baseURL    string
	httpClient *http.Client
	username   string
	password   string

	// Token management
	tokenMu      sync.RWMutex
	accessToken  string
	refreshToken string
	tokenExpiry  time.Time

	// Configuration
	autoRefresh       bool
	refreshThreshold  time.Duration // Refresh token before this duration before expiry
	useTokenAuth      bool          // Whether to use token auth (vs basic auth)
}

// Config holds client configuration options.
type Config struct {
	// BaseURL is the base URL of the dbengine API server (e.g., "http://localhost:8080")
	BaseURL string

	// Username for authentication
	Username string

	// Password for authentication
	Password string

	// Timeout for HTTP requests (default: 30s)
	Timeout time.Duration

	// UseTokenAuth enables JWT token authentication instead of Basic Auth.
	// When enabled, the client will automatically login, cache tokens,
	// and refresh them before expiry. (default: true if username/password provided)
	UseTokenAuth bool

	// AutoRefresh automatically refreshes tokens before they expire (default: true)
	AutoRefresh bool

	// RefreshThreshold is how long before token expiry to trigger refresh (default: 5 minutes)
	RefreshThreshold time.Duration

	// Connection pool settings
	MaxIdleConns        int           // Maximum idle connections (default: 100)
	MaxIdleConnsPerHost int           // Maximum idle connections per host (default: 100)
	MaxConnsPerHost     int           // Maximum total connections per host (default: 100)
	IdleConnTimeout     time.Duration // Idle connection timeout (default: 90s)
}

// New creates a new dbengine client with optimized connection pooling.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("BaseURL is required")
	}

	// Ensure BaseURL doesn't have trailing slash
	baseURL := strings.TrimSuffix(cfg.BaseURL, "/")

	// Default timeout
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	// Default connection pool settings for high performance
	maxIdleConns := cfg.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = 100
	}

	maxIdleConnsPerHost := cfg.MaxIdleConnsPerHost
	if maxIdleConnsPerHost == 0 {
		maxIdleConnsPerHost = 100
	}

	maxConnsPerHost := cfg.MaxConnsPerHost
	if maxConnsPerHost == 0 {
		maxConnsPerHost = 100
	}

	idleConnTimeout := cfg.IdleConnTimeout
	if idleConnTimeout == 0 {
		idleConnTimeout = 90 * time.Second
	}

	refreshThreshold := cfg.RefreshThreshold
	if refreshThreshold == 0 {
		refreshThreshold = 5 * time.Minute
	}

	// Create optimized transport with connection pooling
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		MaxConnsPerHost:       maxConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    false,
	}

	// Determine if we should use token auth
	useTokenAuth := cfg.UseTokenAuth
	if !useTokenAuth && cfg.Username != "" && cfg.Password != "" {
		useTokenAuth = true // Default to token auth when credentials provided
	}

	autoRefresh := cfg.AutoRefresh
	if !cfg.AutoRefresh && useTokenAuth {
		autoRefresh = true // Default to auto-refresh when using tokens
	}

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		username:         cfg.Username,
		password:         cfg.Password,
		useTokenAuth:     useTokenAuth,
		autoRefresh:      autoRefresh,
		refreshThreshold: refreshThreshold,
	}, nil
}

// TokenPair represents an access and refresh token pair from the server.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
}

// Login authenticates with the server and caches the tokens.
// This is called automatically on first request if UseTokenAuth is enabled.
func (c *Client) Login(ctx context.Context) error {
	if c.username == "" || c.password == "" {
		return fmt.Errorf("username and password required for login")
	}

	body := map[string]string{
		"username": c.username,
		"password": c.password,
	}

	var tokens TokenPair
	if err := c.doRequestNoAuth(ctx, http.MethodPost, "/api/v1/auth/login", body, &tokens); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	c.tokenMu.Lock()
	c.accessToken = tokens.AccessToken
	c.refreshToken = tokens.RefreshToken
	c.tokenExpiry = time.Unix(tokens.ExpiresAt, 0)
	c.tokenMu.Unlock()

	return nil
}

// RefreshTokens refreshes the access token using the refresh token.
func (c *Client) RefreshTokens(ctx context.Context) error {
	c.tokenMu.RLock()
	refreshToken := c.refreshToken
	c.tokenMu.RUnlock()

	if refreshToken == "" {
		return c.Login(ctx)
	}

	body := map[string]string{
		"refresh_token": refreshToken,
	}

	var tokens TokenPair
	if err := c.doRequestNoAuth(ctx, http.MethodPost, "/api/v1/auth/refresh", body, &tokens); err != nil {
		// If refresh fails, try to login again
		return c.Login(ctx)
	}

	c.tokenMu.Lock()
	c.accessToken = tokens.AccessToken
	c.refreshToken = tokens.RefreshToken
	c.tokenExpiry = time.Unix(tokens.ExpiresAt, 0)
	c.tokenMu.Unlock()

	return nil
}

// Logout revokes the current tokens.
func (c *Client) Logout(ctx context.Context) error {
	c.tokenMu.Lock()
	accessToken := c.accessToken
	refreshToken := c.refreshToken
	c.accessToken = ""
	c.refreshToken = ""
	c.tokenExpiry = time.Time{}
	c.tokenMu.Unlock()

	body := map[string]string{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	}

	return c.post(ctx, "/api/v1/auth/logout", body, nil)
}

// ensureValidToken ensures we have a valid access token, refreshing if needed.
func (c *Client) ensureValidToken(ctx context.Context) error {
	if !c.useTokenAuth {
		return nil
	}

	c.tokenMu.RLock()
	accessToken := c.accessToken
	expiry := c.tokenExpiry
	c.tokenMu.RUnlock()

	// If no token, login
	if accessToken == "" {
		return c.Login(ctx)
	}

	// If auto-refresh enabled and token is near expiry, refresh
	if c.autoRefresh && time.Now().Add(c.refreshThreshold).After(expiry) {
		return c.RefreshTokens(ctx)
	}

	return nil
}

// QueryResult represents the result of a query.
type QueryResult struct {
	Columns       []string         `json:"columns"`
	Rows          []map[string]any `json:"rows"`
	RowCount      int              `json:"row_count"`
	ExecutionMs   int64            `json:"execution_ms"`
	TranslatedSQL string           `json:"translated_sql,omitempty"`
}

// ExecuteResult represents the result of an execute operation.
type ExecuteResult struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	RowsAffected int64  `json:"rows_affected,omitempty"`
	ExecutionMs  int64  `json:"execution_ms"`
}

// TableSchema represents a table schema.
type TableSchema struct {
	Name         string        `json:"name"`
	PrimaryKey   string        `json:"primary_key"`
	CreatedAt    string        `json:"created_at,omitempty"`
	UpdatedAt    string        `json:"updated_at,omitempty"`
	ColumnGroups []ColumnGroup `json:"column_groups"`
}

// ColumnGroup represents a group of columns.
type ColumnGroup struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// Column represents a column definition.
type Column struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	PrimaryKey bool   `json:"primary_key,omitempty"`
	Indexed    bool   `json:"indexed,omitempty"`
	Nullable   bool   `json:"nullable,omitempty"`
}

// Row represents a data row.
type Row struct {
	ID     string                    `json:"id"`
	Groups map[string]map[string]any `json:"groups"`
}

// APIError represents an API error response.
type APIError struct {
	StatusCode int
	Error      string `json:"error"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

func (e *APIError) String() string {
	return fmt.Sprintf("%s: %s (code: %s)", e.Error, e.Message, e.Code)
}

// Query executes a SELECT query and returns the results.
func (c *Client) Query(ctx context.Context, sql string, params ...any) (*QueryResult, error) {
	body := map[string]any{
		"sql":    sql,
		"params": params,
	}

	var result QueryResult
	if err := c.post(ctx, "/api/v1/query", body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// Execute executes a non-SELECT SQL statement (INSERT, UPDATE, DELETE, etc.).
func (c *Client) Execute(ctx context.Context, sql string, params ...any) (*ExecuteResult, error) {
	body := map[string]any{
		"sql":    sql,
		"params": params,
	}

	var result ExecuteResult
	if err := c.post(ctx, "/api/v1/execute", body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// ListTables returns a list of all table names.
func (c *Client) ListTables(ctx context.Context) ([]string, error) {
	var result struct {
		Tables []string `json:"tables"`
	}

	if err := c.get(ctx, "/api/v1/tables", &result); err != nil {
		return nil, err
	}

	return result.Tables, nil
}

// CreateTable creates a new table with the given schema.
func (c *Client) CreateTable(ctx context.Context, schema *TableSchema) error {
	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	if err := c.post(ctx, "/api/v1/tables", schema, &result); err != nil {
		return err
	}

	if !result.Success {
		return fmt.Errorf("failed to create table: %s", result.Message)
	}

	return nil
}

// GetTableSchema returns the schema for a table.
func (c *Client) GetTableSchema(ctx context.Context, tableName string) (*TableSchema, error) {
	var schema TableSchema
	if err := c.get(ctx, "/api/v1/tables/"+url.PathEscape(tableName), &schema); err != nil {
		return nil, err
	}

	return &schema, nil
}

// DropTable deletes a table and all its data.
func (c *Client) DropTable(ctx context.Context, tableName string) error {
	return c.delete(ctx, "/api/v1/tables/"+url.PathEscape(tableName))
}

// LoadTable loads a table from JSON files into DuckDB.
func (c *Client) LoadTable(ctx context.Context, tableName string) error {
	var result struct {
		Success bool `json:"success"`
	}

	if err := c.post(ctx, "/api/v1/tables/"+url.PathEscape(tableName)+"/load", nil, &result); err != nil {
		return err
	}

	return nil
}

// SyncTable synchronizes a table between JSON files and DuckDB.
func (c *Client) SyncTable(ctx context.Context, tableName string) error {
	var result struct {
		Success bool `json:"success"`
	}

	if err := c.post(ctx, "/api/v1/tables/"+url.PathEscape(tableName)+"/sync", nil, &result); err != nil {
		return err
	}

	return nil
}

// InsertRowData represents data for inserting a row.
type InsertRowData struct {
	ID     string                    `json:"_id"`
	Groups map[string]map[string]any `json:"groups,omitempty"`
	Data   map[string]any            `json:"data,omitempty"`
}

// InsertRow inserts a new row into a table.
func (c *Client) InsertRow(ctx context.Context, tableName string, data *InsertRowData) error {
	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	if err := c.post(ctx, "/api/v1/rows/"+url.PathEscape(tableName), data, &result); err != nil {
		return err
	}

	if !result.Success {
		return fmt.Errorf("failed to insert row: %s", result.Message)
	}

	return nil
}

// GetRow retrieves a row by ID.
func (c *Client) GetRow(ctx context.Context, tableName, rowID string) (*Row, error) {
	var row Row
	path := fmt.Sprintf("/api/v1/rows/%s/%s", url.PathEscape(tableName), url.PathEscape(rowID))
	if err := c.get(ctx, path, &row); err != nil {
		return nil, err
	}

	return &row, nil
}

// UpdateRowData represents data for updating a row.
type UpdateRowData struct {
	Groups map[string]map[string]any `json:"groups,omitempty"`
	Data   map[string]any            `json:"data,omitempty"`
}

// UpdateRow updates an existing row.
func (c *Client) UpdateRow(ctx context.Context, tableName, rowID string, data *UpdateRowData) error {
	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}

	path := fmt.Sprintf("/api/v1/rows/%s/%s", url.PathEscape(tableName), url.PathEscape(rowID))
	if err := c.put(ctx, path, data, &result); err != nil {
		return err
	}

	if !result.Success {
		return fmt.Errorf("failed to update row: %s", result.Message)
	}

	return nil
}

// DeleteRow deletes a row from a table.
func (c *Client) DeleteRow(ctx context.Context, tableName, rowID string) error {
	path := fmt.Sprintf("/api/v1/rows/%s/%s", url.PathEscape(tableName), url.PathEscape(rowID))
	return c.delete(ctx, path)
}

// Health checks if the server is healthy.
func (c *Client) Health(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}

	// Health endpoint doesn't require auth
	if err := c.doRequestNoAuth(ctx, http.MethodGet, "/health", nil, &result); err != nil {
		return err
	}

	if result.Status != "ok" {
		return fmt.Errorf("server unhealthy: %s", result.Status)
	}

	return nil
}

// get performs a GET request.
func (c *Client) get(ctx context.Context, path string, result any) error {
	return c.doRequest(ctx, http.MethodGet, path, nil, result)
}

// post performs a POST request.
func (c *Client) post(ctx context.Context, path string, body, result any) error {
	return c.doRequest(ctx, http.MethodPost, path, body, result)
}

// put performs a PUT request.
func (c *Client) put(ctx context.Context, path string, body, result any) error {
	return c.doRequest(ctx, http.MethodPut, path, body, result)
}

// delete performs a DELETE request.
func (c *Client) delete(ctx context.Context, path string) error {
	return c.doRequest(ctx, http.MethodDelete, path, nil, nil)
}

// doRequest performs an HTTP request with authentication.
func (c *Client) doRequest(ctx context.Context, method, path string, body, result any) error {
	// Ensure we have a valid token if using token auth
	if c.useTokenAuth {
		if err := c.ensureValidToken(ctx); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}

	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connection", "keep-alive")

	// Add authentication
	if c.useTokenAuth {
		c.tokenMu.RLock()
		token := c.accessToken
		c.tokenMu.RUnlock()
		req.Header.Set("Authorization", "Bearer "+token)
	} else if c.username != "" && c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Handle 401 - try to refresh token and retry once
	if resp.StatusCode == http.StatusUnauthorized && c.useTokenAuth {
		if err := c.RefreshTokens(ctx); err != nil {
			return fmt.Errorf("token refresh failed: %w", err)
		}
		// Retry the request
		return c.doRequest(ctx, method, path, body, result)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
		}
		apiErr.StatusCode = resp.StatusCode
		return fmt.Errorf("API error: %s", apiErr.String())
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

// doRequestNoAuth performs an HTTP request without authentication.
func (c *Client) doRequestNoAuth(ctx context.Context, method, path string, body, result any) error {
	url := c.baseURL + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Connection", "keep-alive")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		var apiErr APIError
		if err := json.Unmarshal(respBody, &apiErr); err != nil {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
		}
		apiErr.StatusCode = resp.StatusCode
		return fmt.Errorf("API error: %s", apiErr.String())
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

// Close closes the client and releases resources.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// Stats returns client statistics.
type Stats struct {
	TokenExpiry time.Time
	HasToken    bool
}

// GetStats returns current client statistics.
func (c *Client) GetStats() Stats {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()

	return Stats{
		TokenExpiry: c.tokenExpiry,
		HasToken:    c.accessToken != "",
	}
}
