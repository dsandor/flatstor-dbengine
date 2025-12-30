package query

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
)

// Translator converts column group SQL to DuckDB SQL.
type Translator struct {
	manager         *colgroup.Manager
	colGroupPattern *regexp.Regexp
}

// NewTranslator creates a new query translator.
func NewTranslator(manager *colgroup.Manager) *Translator {
	return &Translator{
		manager: manager,
		// Pattern matches Group.Column but NOT decimal numbers like 123.456
		// Group must start with letter/underscore, Column can be * or start with letter/underscore
		colGroupPattern: regexp.MustCompile(`([a-zA-Z_]\w*)\.([a-zA-Z_]\w*|\*)`),
	}
}

// Translate converts a column group SQL query to DuckDB SQL.
func (t *Translator) Translate(ctx context.Context, parsed *ParsedQuery) (string, error) {
	switch parsed.Type {
	case QuerySelect:
		return t.translateSelect(ctx, parsed)
	case QueryInsert:
		return t.translateInsert(ctx, parsed)
	case QueryUpdate:
		return t.translateUpdate(ctx, parsed)
	case QueryDelete:
		return t.translateDelete(ctx, parsed)
	default:
		// Pass through other queries unchanged
		return parsed.RawSQL, nil
	}
}

// translateSelect converts a SELECT query.
func (t *Translator) translateSelect(ctx context.Context, parsed *ParsedQuery) (string, error) {
	schema, err := t.manager.GetTable(ctx, parsed.TableName)
	if err != nil {
		return "", fmt.Errorf("table %s not found: %w", parsed.TableName, err)
	}

	var selectCols []string

	if parsed.AllColumns {
		// Expand * to all columns with flattened names
		selectCols = append(selectCols, "_row_id")
		for _, col := range schema.AllColumns() {
			selectCols = append(selectCols, col.FlatName())
		}
	} else {
		// Translate each column reference
		for _, col := range parsed.Columns {
			if col.GroupName == "" {
				// Check if it's a SQL function (contains parentheses) - pass through with translation
				if strings.Contains(col.ColumnName, "(") {
					// Translate any column references inside the function
					translated := t.translateColumnRefs(col.ColumnName)
					selectCols = append(selectCols, translated)
					continue
				}
				// Check if it's a special column like _row_id
				if strings.HasPrefix(col.ColumnName, "_") {
					selectCols = append(selectCols, col.ColumnName)
					continue
				}
				// Try to find unqualified column
				resolved, err := t.resolveUnqualifiedColumn(schema, col.ColumnName)
				if err != nil {
					return "", err
				}
				selectCols = append(selectCols, resolved.FlatName())
			} else if col.ColumnName == "*" {
				// Group.* - expand to all columns in this group
				group := schema.GetColumnGroup(col.GroupName)
				if group == nil {
					return "", fmt.Errorf("column group %s not found", col.GroupName)
				}
				for _, c := range group.Columns {
					selectCols = append(selectCols, colgroup.QualifiedColumn{
						GroupName:  col.GroupName,
						ColumnName: c.Name,
					}.FlatName())
				}
			} else {
				selectCols = append(selectCols, col.FlatName())
			}
		}
	}

	// Build the translated SELECT
	sql := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectCols, ", "), parsed.TableName)

	// Translate WHERE clause
	if parsed.WhereClause != "" {
		translatedWhere := t.translateColumnRefs(parsed.WhereClause)
		sql += " WHERE " + translatedWhere
	}

	// Preserve ORDER BY, LIMIT, GROUP BY, HAVING from original query
	// and translate any column references in them
	trailingClauses := t.extractTrailingClauses(parsed.RawSQL)
	if trailingClauses != "" {
		sql += t.translateColumnRefs(trailingClauses)
	}

	return sql, nil
}

// extractTrailingClauses extracts ORDER BY, LIMIT, GROUP BY, HAVING clauses from the original SQL.
func (t *Translator) extractTrailingClauses(sql string) string {
	upperSQL := strings.ToUpper(sql)

	// Find the earliest trailing clause
	clauses := []string{" ORDER BY ", " LIMIT ", " GROUP BY ", " HAVING "}
	earliestIdx := -1

	for _, clause := range clauses {
		idx := strings.Index(upperSQL, clause)
		if idx != -1 && (earliestIdx == -1 || idx < earliestIdx) {
			earliestIdx = idx
		}
	}

	if earliestIdx == -1 {
		return ""
	}

	// Return the trailing portion (preserving original case)
	return sql[earliestIdx:]
}

// translateInsert converts an INSERT query.
func (t *Translator) translateInsert(ctx context.Context, parsed *ParsedQuery) (string, error) {
	_, err := t.manager.GetTable(ctx, parsed.TableName)
	if err != nil {
		return "", fmt.Errorf("table %s not found: %w", parsed.TableName, err)
	}

	// Translate column names in the raw SQL
	translated := t.translateColumnRefs(parsed.RawSQL)

	return translated, nil
}

// translateUpdate converts an UPDATE query.
func (t *Translator) translateUpdate(ctx context.Context, parsed *ParsedQuery) (string, error) {
	_, err := t.manager.GetTable(ctx, parsed.TableName)
	if err != nil {
		return "", fmt.Errorf("table %s not found: %w", parsed.TableName, err)
	}

	// Translate column names in the raw SQL
	translated := t.translateColumnRefs(parsed.RawSQL)

	return translated, nil
}

// translateDelete converts a DELETE query.
func (t *Translator) translateDelete(ctx context.Context, parsed *ParsedQuery) (string, error) {
	// Delete doesn't usually have column references, but translate WHERE if present
	translated := t.translateColumnRefs(parsed.RawSQL)
	return translated, nil
}

// translateColumnRefs replaces Group.Column with Group_Column in a SQL string.
func (t *Translator) translateColumnRefs(sql string) string {
	return t.colGroupPattern.ReplaceAllString(sql, "${1}_${2}")
}

// resolveUnqualifiedColumn finds a column in the schema by name only.
// Returns an error if the column is ambiguous (exists in multiple groups).
func (t *Translator) resolveUnqualifiedColumn(schema *colgroup.TableSchema, colName string) (colgroup.QualifiedColumn, error) {
	var found []colgroup.QualifiedColumn

	for _, group := range schema.ColumnGroups {
		for _, col := range group.Columns {
			if col.Name == colName {
				found = append(found, colgroup.QualifiedColumn{
					GroupName:  group.Name,
					ColumnName: col.Name,
				})
			}
		}
	}

	if len(found) == 0 {
		return colgroup.QualifiedColumn{}, fmt.Errorf("column %s not found in any group", colName)
	}

	if len(found) > 1 {
		groups := make([]string, len(found))
		for i, f := range found {
			groups[i] = f.GroupName
		}
		return colgroup.QualifiedColumn{}, fmt.Errorf("column %s is ambiguous, exists in groups: %s",
			colName, strings.Join(groups, ", "))
	}

	return found[0], nil
}

// TranslatedQuery holds the original and translated SQL.
type TranslatedQuery struct {
	Original   string
	Translated string
	Parsed     *ParsedQuery
}

// TranslateSQL parses and translates a SQL query in one step.
func (t *Translator) TranslateSQL(ctx context.Context, sql string) (*TranslatedQuery, error) {
	parser := NewParser()
	parsed, err := parser.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	translated, err := t.Translate(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("translation error: %w", err)
	}

	return &TranslatedQuery{
		Original:   sql,
		Translated: translated,
		Parsed:     parsed,
	}, nil
}

// GetRequiredColumnGroups returns the column groups needed for a query.
func (t *Translator) GetRequiredColumnGroups(parsed *ParsedQuery) []string {
	groups := make(map[string]bool)

	for _, col := range parsed.Columns {
		if col.GroupName != "" {
			groups[col.GroupName] = true
		}
	}

	// Also check WHERE clause for column references
	if parsed.WhereClause != "" {
		matches := t.colGroupPattern.FindAllStringSubmatch(parsed.WhereClause, -1)
		for _, match := range matches {
			if len(match) >= 2 {
				groups[match[1]] = true
			}
		}
	}

	result := make([]string, 0, len(groups))
	for group := range groups {
		result = append(result, group)
	}
	return result
}

// ValidateQuery validates a query against the schema.
func (t *Translator) ValidateQuery(ctx context.Context, parsed *ParsedQuery) error {
	if parsed.TableName == "" {
		return fmt.Errorf("table name is required")
	}

	schema, err := t.manager.GetTable(ctx, parsed.TableName)
	if err != nil {
		return fmt.Errorf("table %s not found: %w", parsed.TableName, err)
	}

	// Validate all column references
	for _, col := range parsed.Columns {
		if col.GroupName == "" {
			// Will be resolved during translation
			continue
		}

		group := schema.GetColumnGroup(col.GroupName)
		if group == nil {
			return fmt.Errorf("column group %s not found in table %s", col.GroupName, parsed.TableName)
		}

		// Allow Group.* for selecting all columns in a group
		if col.ColumnName == "*" {
			continue
		}

		found := false
		for _, c := range group.Columns {
			if c.Name == col.ColumnName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("column %s not found in group %s", col.ColumnName, col.GroupName)
		}
	}

	return nil
}
