package colgroup

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/dsandor/flatstor/dbengine/pkg/storage"
)

const schemaFileName = "_schema.json"

// Manager handles table schemas and column group operations.
type Manager struct {
	storage storage.Storage
	schemas map[string]*TableSchema
	mu      sync.RWMutex
}

// NewManager creates a new column group manager.
func NewManager(store storage.Storage) *Manager {
	return &Manager{
		storage: store,
		schemas: make(map[string]*TableSchema),
	}
}

// CreateTable creates a new table with the given schema.
func (m *Manager) CreateTable(ctx context.Context, schema *TableSchema) error {
	if err := schema.Validate(); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if table already exists
	if _, exists := m.schemas[schema.Name]; exists {
		return fmt.Errorf("table %s already exists", schema.Name)
	}

	// Check on disk
	schemaPath := filepath.Join(schema.Name, schemaFileName)
	exists, err := m.storage.Exists(ctx, schemaPath)
	if err != nil {
		return fmt.Errorf("failed to check table existence: %w", err)
	}
	if exists {
		return fmt.Errorf("table %s already exists on disk", schema.Name)
	}

	// Set timestamps
	now := time.Now().UTC().Format(time.RFC3339)
	schema.CreatedAt = now
	schema.UpdatedAt = now

	// Create table directory
	if err := m.storage.MkdirAll(ctx, schema.Name); err != nil {
		return fmt.Errorf("failed to create table directory: %w", err)
	}

	// Write schema file
	if err := m.storage.WriteJSON(ctx, schemaPath, schema); err != nil {
		return fmt.Errorf("failed to write schema: %w", err)
	}

	m.schemas[schema.Name] = schema
	return nil
}

// GetTable returns the schema for the given table.
func (m *Manager) GetTable(ctx context.Context, tableName string) (*TableSchema, error) {
	m.mu.RLock()
	if schema, ok := m.schemas[tableName]; ok {
		m.mu.RUnlock()
		return schema, nil
	}
	m.mu.RUnlock()

	// Try to load from disk
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if schema, ok := m.schemas[tableName]; ok {
		return schema, nil
	}

	schemaPath := filepath.Join(tableName, schemaFileName)
	var schema TableSchema
	if err := m.storage.ReadJSON(ctx, schemaPath, &schema); err != nil {
		return nil, fmt.Errorf("table %s not found: %w", tableName, err)
	}

	m.schemas[tableName] = &schema
	return &schema, nil
}

// DropTable removes a table and all its data.
func (m *Manager) DropTable(ctx context.Context, tableName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove from cache
	delete(m.schemas, tableName)

	// Remove from disk
	if err := m.storage.DeleteDir(ctx, tableName); err != nil {
		return fmt.Errorf("failed to delete table: %w", err)
	}

	return nil
}

// ListTables returns all table names.
func (m *Manager) ListTables(ctx context.Context) ([]string, error) {
	dirs, err := m.storage.ListDirs(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}

	// Filter to only include directories with schema files
	var tables []string
	for _, dir := range dirs {
		schemaPath := filepath.Join(dir, schemaFileName)
		exists, err := m.storage.Exists(ctx, schemaPath)
		if err != nil {
			continue
		}
		if exists {
			tables = append(tables, dir)
		}
	}

	return tables, nil
}

// LoadAllSchemas loads all table schemas from disk into memory.
func (m *Manager) LoadAllSchemas(ctx context.Context) error {
	tables, err := m.ListTables(ctx)
	if err != nil {
		return err
	}

	for _, tableName := range tables {
		if _, err := m.GetTable(ctx, tableName); err != nil {
			return fmt.Errorf("failed to load schema for %s: %w", tableName, err)
		}
	}

	return nil
}

// TableExists checks if a table exists.
func (m *Manager) TableExists(ctx context.Context, tableName string) (bool, error) {
	m.mu.RLock()
	if _, ok := m.schemas[tableName]; ok {
		m.mu.RUnlock()
		return true, nil
	}
	m.mu.RUnlock()

	schemaPath := filepath.Join(tableName, schemaFileName)
	return m.storage.Exists(ctx, schemaPath)
}

// UpdateSchema updates a table's schema (for adding columns, etc.).
func (m *Manager) UpdateSchema(ctx context.Context, schema *TableSchema) error {
	if err := schema.Validate(); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	schemaPath := filepath.Join(schema.Name, schemaFileName)
	exists, err := m.storage.Exists(ctx, schemaPath)
	if err != nil {
		return fmt.Errorf("failed to check table existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("table %s does not exist", schema.Name)
	}

	schema.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := m.storage.WriteJSON(ctx, schemaPath, schema); err != nil {
		return fmt.Errorf("failed to write schema: %w", err)
	}

	m.schemas[schema.Name] = schema
	return nil
}

// GetRowPath returns the storage path for a row's column group file.
func (m *Manager) GetRowPath(tableName, rowID, groupName string) string {
	return filepath.Join(tableName, rowID, groupName+".json")
}

// GetRowDir returns the storage directory for a row.
func (m *Manager) GetRowDir(tableName, rowID string) string {
	return filepath.Join(tableName, rowID)
}
