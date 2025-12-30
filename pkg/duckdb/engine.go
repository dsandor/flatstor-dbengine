package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/marcboeker/go-duckdb"
)

// Engine wraps DuckDB for query execution.
type Engine struct {
	db   *sql.DB
	mu   sync.RWMutex
	path string // Empty string for in-memory
}

// Config holds DuckDB configuration options.
type Config struct {
	Path     string // Database file path, empty for in-memory
	ReadOnly bool
}

// NewEngine creates a new DuckDB engine.
func NewEngine(cfg Config) (*Engine, error) {
	// go-duckdb uses empty string for in-memory, or a file path for persistent
	dsn := cfg.Path
	if cfg.ReadOnly && dsn != "" {
		dsn += "?access_mode=read_only"
	}

	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open DuckDB: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping DuckDB: %w", err)
	}

	return &Engine{
		db:   db,
		path: cfg.Path,
	}, nil
}

// Close closes the DuckDB connection.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.db != nil {
		return e.db.Close()
	}
	return nil
}

// DB returns the underlying sql.DB.
func (e *Engine) DB() *sql.DB {
	return e.db
}

// Exec executes a SQL statement that doesn't return rows.
func (e *Engine) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.db.ExecContext(ctx, query, args...)
}

// Query executes a SQL query that returns rows.
func (e *Engine) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.db.QueryContext(ctx, query, args...)
}

// QueryRow executes a SQL query that returns a single row.
func (e *Engine) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.db.QueryRowContext(ctx, query, args...)
}

// QueryResult represents the result of a query.
type QueryResult struct {
	Columns []string
	Rows    []map[string]any
}

// QueryToMaps executes a query and returns results as a slice of maps.
func (e *Engine) QueryToMaps(ctx context.Context, query string, args ...any) (*QueryResult, error) {
	rows, err := e.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("failed to get columns: %w", err)
	}

	result := &QueryResult{
		Columns: columns,
		Rows:    make([]map[string]any, 0),
	}

	for rows.Next() {
		// Create a slice of interface{} to hold each column value
		values := make([]any, len(columns))
		valuePtrs := make([]any, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		// Create a map for this row
		rowMap := make(map[string]any)
		for i, col := range columns {
			rowMap[col] = values[i]
		}
		result.Rows = append(result.Rows, rowMap)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return result, nil
}

// CreateTable creates a table in DuckDB.
func (e *Engine) CreateTable(ctx context.Context, tableName string, columns map[string]string) error {
	if len(columns) == 0 {
		return fmt.Errorf("no columns provided")
	}

	// Build CREATE TABLE statement
	query := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (", tableName)
	first := true
	for colName, colType := range columns {
		if !first {
			query += ", "
		}
		query += fmt.Sprintf("%s %s", colName, colType)
		first = false
	}
	query += ")"

	_, err := e.Exec(ctx, query)
	return err
}

// DropTable drops a table from DuckDB.
func (e *Engine) DropTable(ctx context.Context, tableName string) error {
	_, err := e.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName))
	return err
}

// TableExists checks if a table exists in DuckDB.
func (e *Engine) TableExists(ctx context.Context, tableName string) (bool, error) {
	query := `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?`
	var count int
	err := e.QueryRow(ctx, query, tableName).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// InsertRow inserts a row into a DuckDB table.
func (e *Engine) InsertRow(ctx context.Context, tableName string, data map[string]any) error {
	if len(data) == 0 {
		return fmt.Errorf("no data provided")
	}

	columns := make([]string, 0, len(data))
	placeholders := make([]string, 0, len(data))
	values := make([]any, 0, len(data))

	for col, val := range data {
		columns = append(columns, col)
		placeholders = append(placeholders, "?")
		values = append(values, val)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		tableName,
		joinStrings(columns, ", "),
		joinStrings(placeholders, ", "),
	)

	_, err := e.Exec(ctx, query, values...)
	return err
}

// TruncateTable removes all rows from a table.
func (e *Engine) TruncateTable(ctx context.Context, tableName string) error {
	_, err := e.Exec(ctx, fmt.Sprintf("DELETE FROM %s", tableName))
	return err
}

// joinStrings joins strings with a separator.
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
