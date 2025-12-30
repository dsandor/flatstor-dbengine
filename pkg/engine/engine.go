package engine

import (
	"context"
	"fmt"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
	"github.com/dsandor/flatstor/dbengine/pkg/duckdb"
	"github.com/dsandor/flatstor/dbengine/pkg/query"
	"github.com/dsandor/flatstor/dbengine/pkg/storage"
)

// Engine is the main coordinator for the database engine.
type Engine struct {
	storage    storage.Storage
	duckdb     *duckdb.Engine
	manager    *colgroup.Manager
	assembler  *colgroup.Assembler
	loader     *duckdb.Loader
	indexer    *duckdb.Indexer
	translator *query.Translator
	parser     *query.Parser
	dataPath   string
}

// Config holds configuration for the engine.
type Config struct {
	DataPath   string // Base path for data storage
	DuckDBPath string // Path for DuckDB database (empty for in-memory)
}

// New creates a new database engine.
func New(cfg Config) (*Engine, error) {
	// Initialize storage
	store, err := storage.NewDiskStorage(cfg.DataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Initialize DuckDB
	duckEngine, err := duckdb.NewEngine(duckdb.Config{
		Path: cfg.DuckDBPath,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize DuckDB: %w", err)
	}

	// Initialize components
	manager := colgroup.NewManager(store)
	assembler := colgroup.NewAssembler(store, manager)
	loader := duckdb.NewLoader(duckEngine, assembler, manager)
	indexer := duckdb.NewIndexer(store, manager, assembler, loader, store.BasePath())
	translator := query.NewTranslator(manager)
	parser := query.NewParser()

	return &Engine{
		storage:    store,
		duckdb:     duckEngine,
		manager:    manager,
		assembler:  assembler,
		loader:     loader,
		indexer:    indexer,
		translator: translator,
		parser:     parser,
		dataPath:   cfg.DataPath,
	}, nil
}

// Close shuts down the engine.
func (e *Engine) Close() error {
	return e.duckdb.Close()
}

// CreateTable creates a new table with the given schema.
func (e *Engine) CreateTable(ctx context.Context, schema *colgroup.TableSchema) error {
	// Create the table in the column group manager (JSON files)
	if err := e.manager.CreateTable(ctx, schema); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	// Create corresponding DuckDB table
	if err := e.loader.CreateDuckDBTable(ctx, schema.Name); err != nil {
		return fmt.Errorf("failed to create DuckDB table: %w", err)
	}

	return nil
}

// DropTable removes a table and all its data.
func (e *Engine) DropTable(ctx context.Context, tableName string) error {
	// Drop from DuckDB first
	if err := e.loader.DropDuckDBTable(ctx, tableName); err != nil {
		// Log but continue - table might not exist in DuckDB
	}

	// Drop from column group manager (deletes JSON files)
	return e.manager.DropTable(ctx, tableName)
}

// ListTables returns all table names.
func (e *Engine) ListTables(ctx context.Context) ([]string, error) {
	return e.manager.ListTables(ctx)
}

// GetTableSchema returns the schema for a table.
func (e *Engine) GetTableSchema(ctx context.Context, tableName string) (*colgroup.TableSchema, error) {
	return e.manager.GetTable(ctx, tableName)
}

// InsertRow inserts a new row into a table.
func (e *Engine) InsertRow(ctx context.Context, tableName string, row *colgroup.Row) error {
	// Insert into JSON files
	if err := e.assembler.InsertRow(ctx, tableName, row); err != nil {
		return fmt.Errorf("failed to insert row: %w", err)
	}

	// Refresh DuckDB
	if err := e.loader.RefreshRow(ctx, tableName, row.ID); err != nil {
		return fmt.Errorf("failed to sync to DuckDB: %w", err)
	}

	return nil
}

// UpdateRow updates an existing row.
func (e *Engine) UpdateRow(ctx context.Context, tableName string, row *colgroup.Row) error {
	// Update JSON files
	if err := e.assembler.UpdateRow(ctx, tableName, row); err != nil {
		return fmt.Errorf("failed to update row: %w", err)
	}

	// Refresh DuckDB
	if err := e.loader.RefreshRow(ctx, tableName, row.ID); err != nil {
		return fmt.Errorf("failed to sync to DuckDB: %w", err)
	}

	return nil
}

// DeleteRow removes a row from a table.
func (e *Engine) DeleteRow(ctx context.Context, tableName, rowID string) error {
	// Delete from JSON files
	if err := e.assembler.DeleteRow(ctx, tableName, rowID); err != nil {
		return fmt.Errorf("failed to delete row: %w", err)
	}

	// Delete from DuckDB
	if err := e.loader.DeleteRow(ctx, tableName, rowID); err != nil {
		return fmt.Errorf("failed to sync to DuckDB: %w", err)
	}

	return nil
}

// GetRow retrieves a single row by ID.
func (e *Engine) GetRow(ctx context.Context, tableName, rowID string) (*colgroup.Row, error) {
	return e.assembler.ReadRow(ctx, tableName, rowID)
}

// Query executes a SQL query and returns results.
func (e *Engine) Query(ctx context.Context, sql string) (*QueryResult, error) {
	// Parse the query
	parsed, err := e.parser.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	// Handle special query types
	switch parsed.Type {
	case query.QueryCreate:
		return nil, fmt.Errorf("use CreateTable method for CREATE TABLE")
	case query.QueryDrop:
		return nil, fmt.Errorf("use DropTable method for DROP TABLE")
	}

	// Validate the query
	if err := e.translator.ValidateQuery(ctx, parsed); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// Translate to DuckDB SQL
	translated, err := e.translator.Translate(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("translation error: %w", err)
	}

	// Execute on DuckDB
	duckResult, err := e.duckdb.QueryToMaps(ctx, translated)
	if err != nil {
		return nil, fmt.Errorf("execution error: %w", err)
	}

	return &QueryResult{
		Columns: duckResult.Columns,
		Rows:    duckResult.Rows,
		SQL:     translated,
	}, nil
}

// QueryResult represents the result of a query.
type QueryResult struct {
	Columns []string
	Rows    []map[string]any
	SQL     string // The translated SQL that was executed
}

// ProgressFunc is a callback for reporting loading progress.
type ProgressFunc = duckdb.ProgressFunc

// LoadTable loads a table from JSON files into DuckDB.
func (e *Engine) LoadTable(ctx context.Context, tableName string) error {
	return e.loader.LoadTable(ctx, tableName)
}

// LoadTableWithProgress loads a table with progress reporting.
func (e *Engine) LoadTableWithProgress(ctx context.Context, tableName string, progress ProgressFunc) error {
	return e.loader.LoadTableWithProgress(ctx, tableName, progress)
}

// SyncTable synchronizes a table between JSON files and DuckDB.
func (e *Engine) SyncTable(ctx context.Context, tableName string) error {
	return e.loader.SyncTable(ctx, tableName)
}

// LoadAllTables loads all tables from JSON files into DuckDB.
func (e *Engine) LoadAllTables(ctx context.Context) error {
	tables, err := e.ListTables(ctx)
	if err != nil {
		return err
	}

	for _, table := range tables {
		if err := e.LoadTable(ctx, table); err != nil {
			return fmt.Errorf("failed to load table %s: %w", table, err)
		}
	}

	return nil
}

// SmartLoad loads a table, only rebuilding the index if data has changed.
// Returns true if the index was rebuilt, false if it was already current.
func (e *Engine) SmartLoad(ctx context.Context, tableName string, progress ProgressFunc) (bool, error) {
	return e.indexer.SmartLoad(ctx, tableName, progress)
}

// RebuildIndex forces a rebuild of the table index.
func (e *Engine) RebuildIndex(ctx context.Context, tableName string, progress ProgressFunc) error {
	return e.indexer.BuildIndex(ctx, tableName, progress)
}

// GetIndexStatus returns the index status for a table.
func (e *Engine) GetIndexStatus(ctx context.Context, tableName string) (string, error) {
	return e.indexer.GetIndexStatus(ctx, tableName)
}

// NeedsReindex checks if a table needs to be reindexed.
func (e *Engine) NeedsReindex(ctx context.Context, tableName string) (bool, string, error) {
	return e.indexer.NeedsReindex(ctx, tableName)
}

// UpdateColumnGroup updates a specific column group for a row.
func (e *Engine) UpdateColumnGroup(ctx context.Context, tableName, rowID, groupName string, data map[string]any) error {
	if err := e.assembler.UpdateColumnGroup(ctx, tableName, rowID, groupName, data); err != nil {
		return fmt.Errorf("failed to update column group: %w", err)
	}

	// Refresh DuckDB
	if err := e.loader.RefreshRow(ctx, tableName, rowID); err != nil {
		return fmt.Errorf("failed to sync to DuckDB: %w", err)
	}

	return nil
}

// GetColumnGroupData retrieves a specific column group for a row.
func (e *Engine) GetColumnGroupData(ctx context.Context, tableName, rowID, groupName string) (map[string]any, error) {
	return e.assembler.GetColumnGroupData(ctx, tableName, rowID, groupName)
}

// Storage returns the underlying storage interface.
func (e *Engine) Storage() storage.Storage {
	return e.storage
}

// DuckDB returns the underlying DuckDB engine.
func (e *Engine) DuckDB() *duckdb.Engine {
	return e.duckdb
}

// Manager returns the column group manager.
func (e *Engine) Manager() *colgroup.Manager {
	return e.manager
}

// Assembler returns the row assembler.
func (e *Engine) Assembler() *colgroup.Assembler {
	return e.assembler
}
