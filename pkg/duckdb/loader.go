package duckdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
)

// Loader handles loading data from column groups into DuckDB tables.
type Loader struct {
	engine    *Engine
	assembler *colgroup.Assembler
	manager   *colgroup.Manager
}

// NewLoader creates a new data loader.
func NewLoader(engine *Engine, assembler *colgroup.Assembler, manager *colgroup.Manager) *Loader {
	return &Loader{
		engine:    engine,
		assembler: assembler,
		manager:   manager,
	}
}

// CreateDuckDBTable creates a DuckDB table matching the schema.
func (l *Loader) CreateDuckDBTable(ctx context.Context, tableName string) error {
	schema, err := l.manager.GetTable(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to get schema: %w", err)
	}

	// Build column definitions with flattened names
	var colDefs []string
	colDefs = append(colDefs, "_row_id VARCHAR PRIMARY KEY")

	for _, group := range schema.ColumnGroups {
		for _, col := range group.Columns {
			flatName := fmt.Sprintf("%s_%s", group.Name, col.Name)
			duckType := col.Type.ToDuckDBType()
			colDefs = append(colDefs, fmt.Sprintf("%s %s", flatName, duckType))
		}
	}

	// Create the table
	createSQL := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)",
		tableName, strings.Join(colDefs, ", "))

	_, err = l.engine.Exec(ctx, createSQL)
	if err != nil {
		return fmt.Errorf("failed to create DuckDB table: %w", err)
	}

	return nil
}

// ProgressFunc is a callback for reporting loading progress.
type ProgressFunc func(loaded, total int)

// LoadTable loads all rows from JSON files into the DuckDB table.
func (l *Loader) LoadTable(ctx context.Context, tableName string) error {
	return l.LoadTableWithProgress(ctx, tableName, nil)
}

// LoadTableWithProgress loads all rows with progress reporting.
func (l *Loader) LoadTableWithProgress(ctx context.Context, tableName string, progress ProgressFunc) error {
	// Create the table first
	if err := l.CreateDuckDBTable(ctx, tableName); err != nil {
		return err
	}

	// Clear existing data
	if err := l.engine.TruncateTable(ctx, tableName); err != nil {
		return fmt.Errorf("failed to truncate table: %w", err)
	}

	// Get list of row IDs first (fast - just directory listing)
	rowIDs, err := l.assembler.ListRows(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to list rows: %w", err)
	}

	total := len(rowIDs)
	if progress != nil {
		progress(0, total)
	}

	// Load and insert each row one at a time (streaming)
	for i, rowID := range rowIDs {
		row, err := l.assembler.ReadRow(ctx, tableName, rowID)
		if err != nil {
			return fmt.Errorf("failed to read row %s: %w", rowID, err)
		}

		if err := l.insertRow(ctx, tableName, row); err != nil {
			return fmt.Errorf("failed to insert row %s: %w", rowID, err)
		}

		// Report progress every 100 rows or at the end
		if progress != nil && (i%100 == 0 || i == total-1) {
			progress(i+1, total)
		}
	}

	return nil
}

// insertRow inserts a single row into DuckDB.
func (l *Loader) insertRow(ctx context.Context, tableName string, row *colgroup.Row) error {
	data := row.ToFlatMap()
	data["_row_id"] = row.ID

	return l.engine.InsertRow(ctx, tableName, data)
}

// RefreshRow updates a single row in DuckDB.
func (l *Loader) RefreshRow(ctx context.Context, tableName, rowID string) error {
	// Read the row from JSON
	row, err := l.assembler.ReadRow(ctx, tableName, rowID)
	if err != nil {
		return fmt.Errorf("failed to read row: %w", err)
	}

	// Delete existing row
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE _row_id = ?", tableName)
	if _, err := l.engine.Exec(ctx, deleteSQL, rowID); err != nil {
		return fmt.Errorf("failed to delete existing row: %w", err)
	}

	// Insert updated row
	return l.insertRow(ctx, tableName, row)
}

// DeleteRow removes a row from DuckDB.
func (l *Loader) DeleteRow(ctx context.Context, tableName, rowID string) error {
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE _row_id = ?", tableName)
	_, err := l.engine.Exec(ctx, deleteSQL, rowID)
	return err
}

// LoadTableIncremental loads only new rows (those not already in DuckDB).
func (l *Loader) LoadTableIncremental(ctx context.Context, tableName string) error {
	// Ensure table exists
	if err := l.CreateDuckDBTable(ctx, tableName); err != nil {
		return err
	}

	// Get existing row IDs from DuckDB
	existingIDs := make(map[string]bool)
	result, err := l.engine.QueryToMaps(ctx, fmt.Sprintf("SELECT _row_id FROM %s", tableName))
	if err != nil {
		// Table might be empty, continue
	} else {
		for _, row := range result.Rows {
			if id, ok := row["_row_id"].(string); ok {
				existingIDs[id] = true
			}
		}
	}

	// Get all row IDs from JSON files
	rowIDs, err := l.assembler.ListRows(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to list rows: %w", err)
	}

	// Load only new rows
	for _, rowID := range rowIDs {
		if existingIDs[rowID] {
			continue
		}

		row, err := l.assembler.ReadRow(ctx, tableName, rowID)
		if err != nil {
			return fmt.Errorf("failed to read row %s: %w", rowID, err)
		}

		if err := l.insertRow(ctx, tableName, row); err != nil {
			return fmt.Errorf("failed to insert row %s: %w", rowID, err)
		}
	}

	return nil
}

// SyncTable synchronizes the DuckDB table with JSON files.
// This handles inserts, updates, and deletes.
func (l *Loader) SyncTable(ctx context.Context, tableName string) error {
	// Ensure table exists
	if err := l.CreateDuckDBTable(ctx, tableName); err != nil {
		return err
	}

	// Get existing row IDs from DuckDB
	existingIDs := make(map[string]bool)
	result, err := l.engine.QueryToMaps(ctx, fmt.Sprintf("SELECT _row_id FROM %s", tableName))
	if err == nil {
		for _, row := range result.Rows {
			if id, ok := row["_row_id"].(string); ok {
				existingIDs[id] = true
			}
		}
	}

	// Get all row IDs from JSON files
	rowIDs, err := l.assembler.ListRows(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to list rows: %w", err)
	}

	jsonIDs := make(map[string]bool)
	for _, id := range rowIDs {
		jsonIDs[id] = true
	}

	// Delete rows that no longer exist in JSON
	for id := range existingIDs {
		if !jsonIDs[id] {
			if err := l.DeleteRow(ctx, tableName, id); err != nil {
				return fmt.Errorf("failed to delete row %s: %w", id, err)
			}
		}
	}

	// Insert or update rows from JSON
	for _, rowID := range rowIDs {
		row, err := l.assembler.ReadRow(ctx, tableName, rowID)
		if err != nil {
			return fmt.Errorf("failed to read row %s: %w", rowID, err)
		}

		if existingIDs[rowID] {
			// Update existing row
			if err := l.RefreshRow(ctx, tableName, rowID); err != nil {
				return fmt.Errorf("failed to update row %s: %w", rowID, err)
			}
		} else {
			// Insert new row
			if err := l.insertRow(ctx, tableName, row); err != nil {
				return fmt.Errorf("failed to insert row %s: %w", rowID, err)
			}
		}
	}

	return nil
}

// DropDuckDBTable drops the DuckDB table.
func (l *Loader) DropDuckDBTable(ctx context.Context, tableName string) error {
	return l.engine.DropTable(ctx, tableName)
}

// GetRowCount returns the number of rows in the DuckDB table.
func (l *Loader) GetRowCount(ctx context.Context, tableName string) (int64, error) {
	var count int64
	err := l.engine.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
	return count, err
}
