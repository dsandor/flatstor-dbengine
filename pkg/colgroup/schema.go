package colgroup

import (
	"fmt"
	"strings"
)

// DataType represents the type of a column.
type DataType string

const (
	TypeVarchar  DataType = "VARCHAR"
	TypeInteger  DataType = "INTEGER"
	TypeBigInt   DataType = "BIGINT"
	TypeDouble   DataType = "DOUBLE"
	TypeBoolean  DataType = "BOOLEAN"
	TypeDate     DataType = "DATE"
	TypeDateTime DataType = "TIMESTAMP"
	TypeJSON     DataType = "JSON"
)

// Column represents a single column definition.
type Column struct {
	Name       string   `json:"name"`
	Type       DataType `json:"type"`
	PrimaryKey bool     `json:"primary_key,omitempty"`
	Indexed    bool     `json:"indexed,omitempty"`
	Nullable   bool     `json:"nullable,omitempty"`
}

// ColumnGroup represents a group of columns from a single data source.
type ColumnGroup struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// TableSchema represents the full schema for a table.
type TableSchema struct {
	Name         string        `json:"name"`
	ColumnGroups []ColumnGroup `json:"column_groups"`
	PrimaryKey   string        `json:"primary_key"` // Fully qualified: "Common.InstrumentID"
	CreatedAt    string        `json:"created_at,omitempty"`
	UpdatedAt    string        `json:"updated_at,omitempty"`
}

// QualifiedColumn represents a fully qualified column reference.
type QualifiedColumn struct {
	GroupName  string
	ColumnName string
}

// String returns the fully qualified column name.
func (qc QualifiedColumn) String() string {
	return fmt.Sprintf("%s.%s", qc.GroupName, qc.ColumnName)
}

// FlatName returns the flattened column name for DuckDB.
func (qc QualifiedColumn) FlatName() string {
	return fmt.Sprintf("%s_%s", qc.GroupName, qc.ColumnName)
}

// ParseQualifiedColumn parses a "Group.Column" string into a QualifiedColumn.
func ParseQualifiedColumn(s string) (QualifiedColumn, error) {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return QualifiedColumn{}, fmt.Errorf("invalid qualified column: %s (expected Group.Column)", s)
	}
	return QualifiedColumn{
		GroupName:  strings.TrimSpace(parts[0]),
		ColumnName: strings.TrimSpace(parts[1]),
	}, nil
}

// GetColumnGroup returns the column group with the given name, or nil if not found.
func (ts *TableSchema) GetColumnGroup(name string) *ColumnGroup {
	for i := range ts.ColumnGroups {
		if ts.ColumnGroups[i].Name == name {
			return &ts.ColumnGroups[i]
		}
	}
	return nil
}

// GetColumn returns the column in the given group, or nil if not found.
func (ts *TableSchema) GetColumn(groupName, columnName string) *Column {
	group := ts.GetColumnGroup(groupName)
	if group == nil {
		return nil
	}
	for i := range group.Columns {
		if group.Columns[i].Name == columnName {
			return &group.Columns[i]
		}
	}
	return nil
}

// AllColumns returns all qualified columns in the table.
func (ts *TableSchema) AllColumns() []QualifiedColumn {
	var cols []QualifiedColumn
	for _, group := range ts.ColumnGroups {
		for _, col := range group.Columns {
			cols = append(cols, QualifiedColumn{
				GroupName:  group.Name,
				ColumnName: col.Name,
			})
		}
	}
	return cols
}

// IndexedColumns returns all indexed columns in the table.
func (ts *TableSchema) IndexedColumns() []QualifiedColumn {
	var cols []QualifiedColumn
	for _, group := range ts.ColumnGroups {
		for _, col := range group.Columns {
			if col.Indexed {
				cols = append(cols, QualifiedColumn{
					GroupName:  group.Name,
					ColumnName: col.Name,
				})
			}
		}
	}
	return cols
}

// Validate checks the schema for consistency.
func (ts *TableSchema) Validate() error {
	if ts.Name == "" {
		return fmt.Errorf("table name is required")
	}

	if len(ts.ColumnGroups) == 0 {
		return fmt.Errorf("at least one column group is required")
	}

	// Check primary key exists
	if ts.PrimaryKey != "" {
		pk, err := ParseQualifiedColumn(ts.PrimaryKey)
		if err != nil {
			return fmt.Errorf("invalid primary key: %w", err)
		}
		col := ts.GetColumn(pk.GroupName, pk.ColumnName)
		if col == nil {
			return fmt.Errorf("primary key column %s not found", ts.PrimaryKey)
		}
	}

	// Check for duplicate column group names
	groupNames := make(map[string]bool)
	for _, group := range ts.ColumnGroups {
		if group.Name == "" {
			return fmt.Errorf("column group name is required")
		}
		if groupNames[group.Name] {
			return fmt.Errorf("duplicate column group name: %s", group.Name)
		}
		groupNames[group.Name] = true

		// Check for duplicate column names within group
		colNames := make(map[string]bool)
		for _, col := range group.Columns {
			if col.Name == "" {
				return fmt.Errorf("column name is required in group %s", group.Name)
			}
			if colNames[col.Name] {
				return fmt.Errorf("duplicate column name %s in group %s", col.Name, group.Name)
			}
			colNames[col.Name] = true
		}
	}

	return nil
}

// ToDuckDBType converts a DataType to DuckDB type string.
func (dt DataType) ToDuckDBType() string {
	switch dt {
	case TypeVarchar:
		return "VARCHAR"
	case TypeInteger:
		return "INTEGER"
	case TypeBigInt:
		return "BIGINT"
	case TypeDouble:
		return "DOUBLE"
	case TypeBoolean:
		return "BOOLEAN"
	case TypeDate:
		return "DATE"
	case TypeDateTime:
		return "TIMESTAMP"
	case TypeJSON:
		return "JSON"
	default:
		return "VARCHAR"
	}
}
