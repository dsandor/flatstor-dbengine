package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dsandor/flatstor/dbengine/pkg/colgroup"
)

// QueryType represents the type of SQL query.
type QueryType int

const (
	QuerySelect QueryType = iota
	QueryInsert
	QueryUpdate
	QueryDelete
	QueryCreate
	QueryDrop
	QueryUnknown
)

// ParsedQuery represents a parsed SQL query with column group information.
type ParsedQuery struct {
	Type        QueryType
	TableName   string
	Columns     []colgroup.QualifiedColumn
	AllColumns  bool // SELECT *
	WhereClause string
	Values      map[string]any // For INSERT/UPDATE
	RawSQL      string
}

// Parser parses SQL queries with column group syntax.
type Parser struct {
	// Column group pattern: Group.Column
	colGroupPattern *regexp.Regexp
}

// NewParser creates a new SQL parser.
func NewParser() *Parser {
	return &Parser{
		// Pattern matches Group.Column but NOT decimal numbers like 123.456
		// Group must start with letter/underscore, Column can be * or start with letter/underscore
		colGroupPattern: regexp.MustCompile(`([a-zA-Z_]\w*)\.([a-zA-Z_]\w*|\*)`),
	}
}

// Parse parses a SQL query and extracts column group references.
func (p *Parser) Parse(sql string) (*ParsedQuery, error) {
	sql = strings.TrimSpace(sql)
	upperSQL := strings.ToUpper(sql)

	query := &ParsedQuery{
		RawSQL: sql,
	}

	switch {
	case strings.HasPrefix(upperSQL, "SELECT"):
		return p.parseSelect(sql)
	case strings.HasPrefix(upperSQL, "INSERT"):
		return p.parseInsert(sql)
	case strings.HasPrefix(upperSQL, "UPDATE"):
		return p.parseUpdate(sql)
	case strings.HasPrefix(upperSQL, "DELETE"):
		return p.parseDelete(sql)
	case strings.HasPrefix(upperSQL, "CREATE"):
		return p.parseCreate(sql)
	case strings.HasPrefix(upperSQL, "DROP"):
		return p.parseDrop(sql)
	default:
		query.Type = QueryUnknown
		return query, nil
	}
}

// parseSelect parses a SELECT query.
func (p *Parser) parseSelect(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QuerySelect,
		RawSQL: sql,
	}

	upperSQL := strings.ToUpper(sql)

	// Find SELECT ... FROM
	fromIdx := strings.Index(upperSQL, " FROM ")
	if fromIdx == -1 {
		return nil, fmt.Errorf("missing FROM clause")
	}

	// Extract column list
	colList := strings.TrimSpace(sql[7:fromIdx]) // Skip "SELECT "
	if colList == "*" {
		query.AllColumns = true
	} else {
		cols, err := p.parseColumnList(colList)
		if err != nil {
			return nil, fmt.Errorf("failed to parse columns: %w", err)
		}
		query.Columns = cols
	}

	// Extract table name and WHERE clause
	afterFrom := sql[fromIdx+6:] // Skip " FROM "

	// Find the end of table name (could be WHERE, ORDER BY, LIMIT, GROUP BY, etc.)
	upperAfterFrom := strings.ToUpper(afterFrom)
	tableEndIdx := len(afterFrom)

	for _, keyword := range []string{" WHERE ", " ORDER ", " LIMIT ", " GROUP ", " HAVING ", " UNION "} {
		idx := strings.Index(upperAfterFrom, keyword)
		if idx != -1 && idx < tableEndIdx {
			tableEndIdx = idx
		}
	}

	query.TableName = strings.TrimSpace(afterFrom[:tableEndIdx])

	// Extract WHERE clause if present
	whereIdx := strings.Index(upperAfterFrom, " WHERE ")
	if whereIdx != -1 {
		// Find end of WHERE clause (before ORDER BY, LIMIT, etc.)
		whereEnd := len(afterFrom)
		for _, keyword := range []string{" ORDER ", " LIMIT ", " GROUP ", " HAVING ", " UNION "} {
			idx := strings.Index(upperAfterFrom[whereIdx+7:], keyword)
			if idx != -1 {
				endIdx := whereIdx + 7 + idx
				if endIdx < whereEnd {
					whereEnd = endIdx
				}
			}
		}
		query.WhereClause = strings.TrimSpace(afterFrom[whereIdx+7 : whereEnd])
	}

	return query, nil
}

// parseColumnList parses a comma-separated list of column references.
func (p *Parser) parseColumnList(colList string) ([]colgroup.QualifiedColumn, error) {
	var columns []colgroup.QualifiedColumn

	parts := splitColumns(colList)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Handle alias (e.g., "Bloomberg.ISIN AS isin")
		aliasIdx := strings.Index(strings.ToUpper(part), " AS ")
		if aliasIdx != -1 {
			part = strings.TrimSpace(part[:aliasIdx])
		}

		// Check if it's a SQL function (contains parentheses) - pass through as-is
		if strings.Contains(part, "(") {
			columns = append(columns, colgroup.QualifiedColumn{
				GroupName:  "",
				ColumnName: part, // Pass through the entire function call
			})
			continue
		}

		// Check if it's a qualified column (Group.Column or Group.*)
		if strings.Contains(part, ".") {
			// Handle Group.* syntax for selecting all columns in a group
			if strings.HasSuffix(part, ".*") {
				groupName := strings.TrimSuffix(part, ".*")
				columns = append(columns, colgroup.QualifiedColumn{
					GroupName:  groupName,
					ColumnName: "*",
				})
			} else {
				col, err := colgroup.ParseQualifiedColumn(part)
				if err != nil {
					return nil, err
				}
				columns = append(columns, col)
			}
		} else {
			// Unqualified column - will be resolved during translation
			columns = append(columns, colgroup.QualifiedColumn{
				GroupName:  "",
				ColumnName: part,
			})
		}
	}

	return columns, nil
}

// splitColumns splits a column list by commas, respecting parentheses.
func splitColumns(colList string) []string {
	var parts []string
	var current strings.Builder
	depth := 0

	for _, ch := range colList {
		switch ch {
		case '(':
			depth++
			current.WriteRune(ch)
		case ')':
			depth--
			current.WriteRune(ch)
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
			} else {
				current.WriteRune(ch)
			}
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// parseInsert parses an INSERT query.
func (p *Parser) parseInsert(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QueryInsert,
		RawSQL: sql,
		Values: make(map[string]any),
	}

	upperSQL := strings.ToUpper(sql)

	// Find INSERT INTO table_name
	intoIdx := strings.Index(upperSQL, " INTO ")
	if intoIdx == -1 {
		return nil, fmt.Errorf("missing INTO keyword")
	}

	afterInto := sql[intoIdx+6:]

	// Find opening parenthesis for columns
	parenIdx := strings.Index(afterInto, "(")
	if parenIdx == -1 {
		return nil, fmt.Errorf("missing column list")
	}

	query.TableName = strings.TrimSpace(afterInto[:parenIdx])

	// Find column list
	closeParenIdx := strings.Index(afterInto, ")")
	if closeParenIdx == -1 {
		return nil, fmt.Errorf("missing closing parenthesis for columns")
	}

	colList := afterInto[parenIdx+1 : closeParenIdx]
	cols, err := p.parseColumnList(colList)
	if err != nil {
		return nil, fmt.Errorf("failed to parse columns: %w", err)
	}
	query.Columns = cols

	// Find VALUES clause
	valuesIdx := strings.Index(strings.ToUpper(afterInto), "VALUES")
	if valuesIdx == -1 {
		return nil, fmt.Errorf("missing VALUES clause")
	}

	// The rest after VALUES is the values - we keep it as raw for now
	// as parsing values is complex (strings, numbers, nulls, etc.)

	return query, nil
}

// parseUpdate parses an UPDATE query.
func (p *Parser) parseUpdate(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QueryUpdate,
		RawSQL: sql,
	}

	upperSQL := strings.ToUpper(sql)

	// Find table name (UPDATE table_name SET ...)
	setIdx := strings.Index(upperSQL, " SET ")
	if setIdx == -1 {
		return nil, fmt.Errorf("missing SET clause")
	}

	query.TableName = strings.TrimSpace(sql[7:setIdx]) // Skip "UPDATE "

	// Find WHERE clause
	afterSet := sql[setIdx+5:]
	whereIdx := strings.Index(strings.ToUpper(afterSet), " WHERE ")
	if whereIdx != -1 {
		query.WhereClause = strings.TrimSpace(afterSet[whereIdx+7:])
	}

	return query, nil
}

// parseDelete parses a DELETE query.
func (p *Parser) parseDelete(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QueryDelete,
		RawSQL: sql,
	}

	upperSQL := strings.ToUpper(sql)

	// Find FROM clause
	fromIdx := strings.Index(upperSQL, " FROM ")
	if fromIdx == -1 {
		return nil, fmt.Errorf("missing FROM clause")
	}

	// Extract table name and WHERE clause
	afterFrom := sql[fromIdx+6:]
	whereIdx := strings.Index(strings.ToUpper(afterFrom), " WHERE ")
	if whereIdx == -1 {
		query.TableName = strings.TrimSpace(afterFrom)
	} else {
		query.TableName = strings.TrimSpace(afterFrom[:whereIdx])
		query.WhereClause = strings.TrimSpace(afterFrom[whereIdx+7:])
	}

	return query, nil
}

// parseCreate parses a CREATE TABLE query.
func (p *Parser) parseCreate(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QueryCreate,
		RawSQL: sql,
	}

	upperSQL := strings.ToUpper(sql)

	// Find TABLE keyword
	tableIdx := strings.Index(upperSQL, " TABLE ")
	if tableIdx == -1 {
		return nil, fmt.Errorf("missing TABLE keyword")
	}

	afterTable := sql[tableIdx+7:]

	// Handle IF NOT EXISTS
	ifNotExistsIdx := strings.Index(strings.ToUpper(afterTable), "IF NOT EXISTS ")
	if ifNotExistsIdx == 0 {
		afterTable = afterTable[14:]
	}

	// Find opening parenthesis
	parenIdx := strings.Index(afterTable, "(")
	if parenIdx == -1 {
		query.TableName = strings.TrimSpace(afterTable)
	} else {
		query.TableName = strings.TrimSpace(afterTable[:parenIdx])
	}

	return query, nil
}

// parseDrop parses a DROP TABLE query.
func (p *Parser) parseDrop(sql string) (*ParsedQuery, error) {
	query := &ParsedQuery{
		Type:   QueryDrop,
		RawSQL: sql,
	}

	upperSQL := strings.ToUpper(sql)

	// Find TABLE keyword
	tableIdx := strings.Index(upperSQL, " TABLE ")
	if tableIdx == -1 {
		return nil, fmt.Errorf("missing TABLE keyword")
	}

	afterTable := strings.TrimSpace(sql[tableIdx+7:])

	// Handle IF EXISTS
	if strings.HasPrefix(strings.ToUpper(afterTable), "IF EXISTS ") {
		afterTable = strings.TrimSpace(afterTable[10:])
	}

	query.TableName = afterTable

	return query, nil
}

// ExtractColumnGroups extracts all column group references from a SQL string.
func (p *Parser) ExtractColumnGroups(sql string) []colgroup.QualifiedColumn {
	matches := p.colGroupPattern.FindAllStringSubmatch(sql, -1)

	seen := make(map[string]bool)
	var columns []colgroup.QualifiedColumn

	for _, match := range matches {
		if len(match) == 3 {
			key := match[1] + "." + match[2]
			if !seen[key] {
				seen[key] = true
				columns = append(columns, colgroup.QualifiedColumn{
					GroupName:  match[1],
					ColumnName: match[2],
				})
			}
		}
	}

	return columns
}

// String returns a string representation of the query type.
func (qt QueryType) String() string {
	switch qt {
	case QuerySelect:
		return "SELECT"
	case QueryInsert:
		return "INSERT"
	case QueryUpdate:
		return "UPDATE"
	case QueryDelete:
		return "DELETE"
	case QueryCreate:
		return "CREATE"
	case QueryDrop:
		return "DROP"
	default:
		return "UNKNOWN"
	}
}
