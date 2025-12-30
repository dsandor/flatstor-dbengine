package duckdb

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
	"github.com/dsandor/flatstor/dbengine/pkg/storage"
)

// IndexMeta stores metadata about the table index.
type IndexMeta struct {
	TableName    string    `json:"table_name"`
	IndexedAt    time.Time `json:"indexed_at"`
	RowCount     int       `json:"row_count"`
	SchemaHash   string    `json:"schema_hash"`
	DataHash     string    `json:"data_hash"` // Hash of row IDs to detect changes
	ColumnCount  int       `json:"column_count"`
	GroupCount   int       `json:"group_count"`
}

const indexMetaFileName = "_index_meta.json"

// Indexer manages table indexing and change detection.
type Indexer struct {
	storage   storage.Storage
	manager   *colgroup.Manager
	assembler *colgroup.Assembler
	loader    *Loader
	basePath  string
}

// NewIndexer creates a new indexer.
func NewIndexer(store storage.Storage, manager *colgroup.Manager, assembler *colgroup.Assembler, loader *Loader, basePath string) *Indexer {
	return &Indexer{
		storage:   store,
		manager:   manager,
		assembler: assembler,
		loader:    loader,
		basePath:  basePath,
	}
}

// GetIndexMeta returns the index metadata for a table.
func (idx *Indexer) GetIndexMeta(ctx context.Context, tableName string) (*IndexMeta, error) {
	metaPath := filepath.Join(tableName, indexMetaFileName)
	var meta IndexMeta
	if err := idx.storage.ReadJSON(ctx, metaPath, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

// SaveIndexMeta saves the index metadata for a table.
func (idx *Indexer) SaveIndexMeta(ctx context.Context, meta *IndexMeta) error {
	metaPath := filepath.Join(meta.TableName, indexMetaFileName)
	return idx.storage.WriteJSON(ctx, metaPath, meta)
}

// ComputeDataHash computes a hash of all row IDs to detect changes.
func (idx *Indexer) ComputeDataHash(ctx context.Context, tableName string) (string, error) {
	rowIDs, err := idx.assembler.ListRows(ctx, tableName)
	if err != nil {
		return "", err
	}

	// Sort for consistent hashing
	sort.Strings(rowIDs)

	// Also check modification times of row directories
	h := md5.New()
	for _, rowID := range rowIDs {
		h.Write([]byte(rowID))

		// Check modification time of row directory
		rowDir := filepath.Join(idx.basePath, tableName, rowID)
		if info, err := os.Stat(rowDir); err == nil {
			h.Write([]byte(info.ModTime().String()))
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeSchemaHash computes a hash of the table schema.
func (idx *Indexer) ComputeSchemaHash(ctx context.Context, tableName string) (string, error) {
	schema, err := idx.manager.GetTable(ctx, tableName)
	if err != nil {
		return "", err
	}

	h := md5.New()
	h.Write([]byte(schema.Name))
	h.Write([]byte(schema.UpdatedAt))
	for _, group := range schema.ColumnGroups {
		h.Write([]byte(group.Name))
		for _, col := range group.Columns {
			h.Write([]byte(col.Name))
			h.Write([]byte(col.Type))
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// NeedsReindex checks if a table needs to be reindexed.
func (idx *Indexer) NeedsReindex(ctx context.Context, tableName string) (bool, string, error) {
	meta, err := idx.GetIndexMeta(ctx, tableName)
	if err != nil {
		return true, "no index exists", nil
	}

	// Check schema hash
	schemaHash, err := idx.ComputeSchemaHash(ctx, tableName)
	if err != nil {
		return true, "failed to compute schema hash", err
	}
	if meta.SchemaHash != schemaHash {
		return true, "schema changed", nil
	}

	// Check data hash
	dataHash, err := idx.ComputeDataHash(ctx, tableName)
	if err != nil {
		return true, "failed to compute data hash", err
	}
	if meta.DataHash != dataHash {
		return true, "data changed", nil
	}

	return false, "", nil
}

// BuildIndex builds the index for a table.
func (idx *Indexer) BuildIndex(ctx context.Context, tableName string, progress ProgressFunc) error {
	schema, err := idx.manager.GetTable(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to get schema: %w", err)
	}

	// Load all data into DuckDB
	if err := idx.loader.LoadTableWithProgress(ctx, tableName, progress); err != nil {
		return fmt.Errorf("failed to load table: %w", err)
	}

	// Compute hashes
	schemaHash, err := idx.ComputeSchemaHash(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to compute schema hash: %w", err)
	}

	dataHash, err := idx.ComputeDataHash(ctx, tableName)
	if err != nil {
		return fmt.Errorf("failed to compute data hash: %w", err)
	}

	rowCount, err := idx.loader.GetRowCount(ctx, tableName)
	if err != nil {
		rowCount = 0
	}

	// Count columns
	colCount := 0
	for _, group := range schema.ColumnGroups {
		colCount += len(group.Columns)
	}

	// Save index metadata
	meta := &IndexMeta{
		TableName:   tableName,
		IndexedAt:   time.Now().UTC(),
		RowCount:    int(rowCount),
		SchemaHash:  schemaHash,
		DataHash:    dataHash,
		ColumnCount: colCount,
		GroupCount:  len(schema.ColumnGroups),
	}

	if err := idx.SaveIndexMeta(ctx, meta); err != nil {
		return fmt.Errorf("failed to save index metadata: %w", err)
	}

	return nil
}

// SmartLoad loads a table, rebuilding index only if necessary.
func (idx *Indexer) SmartLoad(ctx context.Context, tableName string, progress ProgressFunc) (bool, error) {
	needsReindex, reason, err := idx.NeedsReindex(ctx, tableName)
	if err != nil {
		return false, err
	}

	if needsReindex {
		if progress != nil {
			// Signal that we're rebuilding
			progress(-1, 0) // Special signal: -1 means "rebuilding because: reason"
		}
		if err := idx.BuildIndex(ctx, tableName, progress); err != nil {
			return false, err
		}
		return true, nil // Was rebuilt
	}

	// Index is current - still need to load into DuckDB if using in-memory
	// But the data should already be there if using persistent DuckDB
	exists, err := idx.loader.engine.TableExists(ctx, tableName)
	if err != nil {
		return false, err
	}

	if !exists {
		// Table not in DuckDB, need to load
		if err := idx.BuildIndex(ctx, tableName, progress); err != nil {
			return false, err
		}
		return true, nil
	}

	// Verify row count matches
	dbRowCount, err := idx.loader.GetRowCount(ctx, tableName)
	if err != nil {
		return false, err
	}

	meta, _ := idx.GetIndexMeta(ctx, tableName)
	if meta != nil && int(dbRowCount) != meta.RowCount {
		// Row count mismatch, rebuild
		if err := idx.BuildIndex(ctx, tableName, progress); err != nil {
			return false, err
		}
		return true, nil
	}

	_ = reason // Used for logging if needed
	return false, nil // No rebuild needed
}

// GetIndexStatus returns a human-readable status of the index.
func (idx *Indexer) GetIndexStatus(ctx context.Context, tableName string) (string, error) {
	meta, err := idx.GetIndexMeta(ctx, tableName)
	if err != nil {
		return "not indexed", nil
	}

	needsReindex, reason, _ := idx.NeedsReindex(ctx, tableName)
	if needsReindex {
		return fmt.Sprintf("stale (%s)", reason), nil
	}

	age := time.Since(meta.IndexedAt)
	return fmt.Sprintf("current (indexed %s ago, %d rows)", formatDuration(age), meta.RowCount), nil
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
