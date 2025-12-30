package colgroup

import (
	"context"
	"fmt"
	"strings"

	"github.com/dsandor/flatstor/dbengine/pkg/storage"
)

// Assembler handles reading and writing row data across column groups.
type Assembler struct {
	storage storage.Storage
	manager *Manager
}

// NewAssembler creates a new row assembler.
func NewAssembler(store storage.Storage, manager *Manager) *Assembler {
	return &Assembler{
		storage: store,
		manager: manager,
	}
}

// Row represents a complete row with data from all column groups.
type Row struct {
	ID     string
	Groups map[string]map[string]any // GroupName -> ColumnName -> Value
}

// NewRow creates a new empty row with the given ID.
func NewRow(id string) *Row {
	return &Row{
		ID:     id,
		Groups: make(map[string]map[string]any),
	}
}

// SetValue sets a value for a qualified column.
func (r *Row) SetValue(groupName, columnName string, value any) {
	if r.Groups[groupName] == nil {
		r.Groups[groupName] = make(map[string]any)
	}
	r.Groups[groupName][columnName] = value
}

// GetValue gets a value for a qualified column.
func (r *Row) GetValue(groupName, columnName string) (any, bool) {
	if group, ok := r.Groups[groupName]; ok {
		if val, ok := group[columnName]; ok {
			return val, true
		}
	}
	return nil, false
}

// ToFlatMap converts the row to a flat map with qualified column names.
func (r *Row) ToFlatMap() map[string]any {
	result := make(map[string]any)
	for groupName, group := range r.Groups {
		for colName, value := range group {
			key := fmt.Sprintf("%s_%s", groupName, colName)
			result[key] = value
		}
	}
	return result
}

// InsertRow inserts a new row into the table.
func (a *Assembler) InsertRow(ctx context.Context, tableName string, row *Row) error {
	schema, err := a.manager.GetTable(ctx, tableName)
	if err != nil {
		return fmt.Errorf("table not found: %w", err)
	}

	// Create row directory
	rowDir := a.manager.GetRowDir(tableName, row.ID)
	if err := a.storage.MkdirAll(ctx, rowDir); err != nil {
		return fmt.Errorf("failed to create row directory: %w", err)
	}

	// Write each column group's data
	for _, colGroup := range schema.ColumnGroups {
		groupData := row.Groups[colGroup.Name]
		if groupData == nil {
			groupData = make(map[string]any)
		}

		path := a.manager.GetRowPath(tableName, row.ID, colGroup.Name)
		if err := a.storage.WriteJSON(ctx, path, groupData); err != nil {
			return fmt.Errorf("failed to write column group %s: %w", colGroup.Name, err)
		}
	}

	return nil
}

// ReadRow reads a complete row from the table.
func (a *Assembler) ReadRow(ctx context.Context, tableName, rowID string) (*Row, error) {
	schema, err := a.manager.GetTable(ctx, tableName)
	if err != nil {
		return nil, fmt.Errorf("table not found: %w", err)
	}

	row := NewRow(rowID)

	// Read each column group's data
	for _, colGroup := range schema.ColumnGroups {
		path := a.manager.GetRowPath(tableName, rowID, colGroup.Name)

		var groupData map[string]any
		if err := a.storage.ReadJSON(ctx, path, &groupData); err != nil {
			// Column group file might not exist (optional groups)
			continue
		}

		row.Groups[colGroup.Name] = groupData
	}

	return row, nil
}

// ReadRowColumns reads specific columns from a row.
func (a *Assembler) ReadRowColumns(ctx context.Context, tableName, rowID string, columns []QualifiedColumn) (*Row, error) {
	// Group columns by column group to minimize file reads
	groupsNeeded := make(map[string][]string)
	for _, col := range columns {
		groupsNeeded[col.GroupName] = append(groupsNeeded[col.GroupName], col.ColumnName)
	}

	row := NewRow(rowID)

	for groupName, colNames := range groupsNeeded {
		path := a.manager.GetRowPath(tableName, rowID, groupName)

		var groupData map[string]any
		if err := a.storage.ReadJSON(ctx, path, &groupData); err != nil {
			continue
		}

		// Only include requested columns
		filteredData := make(map[string]any)
		for _, colName := range colNames {
			if val, ok := groupData[colName]; ok {
				filteredData[colName] = val
			}
		}
		row.Groups[groupName] = filteredData
	}

	return row, nil
}

// UpdateRow updates an existing row's data.
func (a *Assembler) UpdateRow(ctx context.Context, tableName string, row *Row) error {
	// Check row exists
	rowDir := a.manager.GetRowDir(tableName, row.ID)
	exists, err := a.storage.Exists(ctx, rowDir)
	if err != nil {
		return fmt.Errorf("failed to check row existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("row %s does not exist", row.ID)
	}

	// Update each column group that has data
	for groupName, groupData := range row.Groups {
		if len(groupData) == 0 {
			continue
		}

		path := a.manager.GetRowPath(tableName, row.ID, groupName)

		// Read existing data
		var existingData map[string]any
		if err := a.storage.ReadJSON(ctx, path, &existingData); err != nil {
			existingData = make(map[string]any)
		}

		// Merge new data
		for k, v := range groupData {
			existingData[k] = v
		}

		// Write back
		if err := a.storage.WriteJSON(ctx, path, existingData); err != nil {
			return fmt.Errorf("failed to update column group %s: %w", groupName, err)
		}
	}

	return nil
}

// DeleteRow removes a row from the table.
func (a *Assembler) DeleteRow(ctx context.Context, tableName, rowID string) error {
	rowDir := a.manager.GetRowDir(tableName, rowID)
	return a.storage.DeleteDir(ctx, rowDir)
}

// ListRows returns all row IDs in the table.
func (a *Assembler) ListRows(ctx context.Context, tableName string) ([]string, error) {
	return a.storage.ListDirs(ctx, tableName)
}

// ReadAllRows reads all rows from the table.
func (a *Assembler) ReadAllRows(ctx context.Context, tableName string) ([]*Row, error) {
	rowIDs, err := a.ListRows(ctx, tableName)
	if err != nil {
		return nil, err
	}

	rows := make([]*Row, 0, len(rowIDs))
	for _, rowID := range rowIDs {
		row, err := a.ReadRow(ctx, tableName, rowID)
		if err != nil {
			return nil, fmt.Errorf("failed to read row %s: %w", rowID, err)
		}
		rows = append(rows, row)
	}

	return rows, nil
}

// ReadRowsByIDs reads multiple rows by their IDs.
func (a *Assembler) ReadRowsByIDs(ctx context.Context, tableName string, rowIDs []string) ([]*Row, error) {
	rows := make([]*Row, 0, len(rowIDs))
	for _, rowID := range rowIDs {
		row, err := a.ReadRow(ctx, tableName, rowID)
		if err != nil {
			continue // Skip rows that don't exist
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// UpdateColumnGroup updates only a specific column group for a row.
func (a *Assembler) UpdateColumnGroup(ctx context.Context, tableName, rowID, groupName string, data map[string]any) error {
	path := a.manager.GetRowPath(tableName, rowID, groupName)

	// Ensure row directory exists
	rowDir := a.manager.GetRowDir(tableName, rowID)
	if err := a.storage.MkdirAll(ctx, rowDir); err != nil {
		return fmt.Errorf("failed to create row directory: %w", err)
	}

	return a.storage.WriteJSON(ctx, path, data)
}

// GetColumnGroupData reads a single column group for a row.
func (a *Assembler) GetColumnGroupData(ctx context.Context, tableName, rowID, groupName string) (map[string]any, error) {
	path := a.manager.GetRowPath(tableName, rowID, groupName)

	var data map[string]any
	if err := a.storage.ReadJSON(ctx, path, &data); err != nil {
		return nil, err
	}

	return data, nil
}

// RowIterator provides iteration over rows for streaming reads.
type RowIterator struct {
	assembler *Assembler
	tableName string
	rowIDs    []string
	columns   []QualifiedColumn // nil means all columns
	index     int
	ctx       context.Context
}

// NewRowIterator creates a new row iterator.
func (a *Assembler) NewRowIterator(ctx context.Context, tableName string, columns []QualifiedColumn) (*RowIterator, error) {
	rowIDs, err := a.ListRows(ctx, tableName)
	if err != nil {
		return nil, err
	}

	return &RowIterator{
		assembler: a,
		tableName: tableName,
		rowIDs:    rowIDs,
		columns:   columns,
		index:     0,
		ctx:       ctx,
	}, nil
}

// Next returns the next row, or nil if iteration is complete.
func (it *RowIterator) Next() (*Row, error) {
	if it.index >= len(it.rowIDs) {
		return nil, nil
	}

	rowID := it.rowIDs[it.index]
	it.index++

	if it.columns == nil {
		return it.assembler.ReadRow(it.ctx, it.tableName, rowID)
	}
	return it.assembler.ReadRowColumns(it.ctx, it.tableName, rowID, it.columns)
}

// Count returns the total number of rows.
func (it *RowIterator) Count() int {
	return len(it.rowIDs)
}

// Reset resets the iterator to the beginning.
func (it *RowIterator) Reset() {
	it.index = 0
}

// ParseRowFromMap creates a Row from a flat map with qualified column names.
func ParseRowFromMap(id string, data map[string]any) *Row {
	row := NewRow(id)
	for key, value := range data {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 2 {
			row.SetValue(parts[0], parts[1], value)
		} else {
			// Handle underscore-separated format
			parts = strings.SplitN(key, "_", 2)
			if len(parts) == 2 {
				row.SetValue(parts[0], parts[1], value)
			}
		}
	}
	return row
}
