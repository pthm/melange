// Package sqlgen provides a domain-specific SQL DSL for generating Melange authorization queries.
// It models authorization concepts directly rather than generic SQL syntax.
package sqlgen

import (
	"fmt"
	"strings"
)

// sqlf formats SQL with automatic dedenting and blank line removal.
// The SQL shape is visible in the format string.
func sqlf(format string, args ...any) string {
	s := fmt.Sprintf(format, args...)
	lines := strings.Split(s, "\n")

	// Find minimum indentation (ignoring empty lines)
	minIndent := 1000
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		if indent < minIndent {
			minIndent = indent
		}
	}

	// Remove common indent and empty lines
	var result []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) >= minIndent {
			result = append(result, line[minIndent:])
		} else {
			result = append(result, strings.TrimLeft(line, " \t"))
		}
	}

	return strings.Join(result, "\n")
}

// optf returns formatted string if condition is true, empty string otherwise.
// Useful for optional SQL clauses.
func optf(cond bool, format string, args ...any) string {
	if !cond {
		return ""
	}
	return fmt.Sprintf(format, args...)
}

// JoinClause represents a SQL JOIN clause.
type JoinClause struct {
	Type      string    // "INNER", "LEFT", etc.
	Table     string    // Deprecated: use TableExpr instead
	TableExpr TableExpr // Preferred: typed table expression
	Alias     string    // Deprecated: use TableExpr's alias instead
	On        Expr
}

// SQL renders the JOIN clause.
func (j JoinClause) SQL() string {
	// Use TableExpr if provided, otherwise fall back to Table string
	var tableSQL string
	if j.TableExpr != nil {
		tableSQL = j.TableExpr.TableSQL()
	} else {
		tableSQL = j.Table
		if j.Alias != "" {
			tableSQL += " AS " + j.Alias
		}
	}
	// CROSS JOIN doesn't have an ON clause
	if j.Type == "CROSS" || j.On == nil {
		return j.Type + " JOIN " + tableSQL
	}
	return j.Type + " JOIN " + tableSQL + " ON " + j.On.SQL()
}

// SelectStmt represents a SELECT query.
type SelectStmt struct {
	Distinct    bool
	Columns     []string  // Deprecated: use ColumnExprs instead
	ColumnExprs []Expr    // Preferred: typed column expressions
	From        string    // Deprecated: use FromExpr instead
	FromExpr    TableExpr // Preferred: typed table expression
	Alias       string    // Deprecated: use FromExpr's alias instead
	Joins       []JoinClause
	Where       Expr
	Limit       int
}

// SQL renders the SELECT statement.
func (s SelectStmt) SQL() string {
	return sqlf(`
		SELECT %s%s
		%s
		%s
		%s
		%s`,
		optf(s.Distinct, "DISTINCT "),
		s.columnsSQL(),
		s.fromSQL(),
		s.joinsSQL(),
		s.whereSQL(),
		s.limitSQL(),
	)
}

func (s SelectStmt) columnsSQL() string {
	// Use ColumnExprs if provided, otherwise fall back to Columns
	if len(s.ColumnExprs) > 0 {
		parts := make([]string, len(s.ColumnExprs))
		for i, e := range s.ColumnExprs {
			parts[i] = e.SQL()
		}
		return strings.Join(parts, ", ")
	}
	if len(s.Columns) > 0 {
		return strings.Join(s.Columns, ", ")
	}
	return "1"
}

func (s SelectStmt) fromSQL() string {
	// Use FromExpr if provided, otherwise fall back to From string
	if s.FromExpr != nil {
		return "FROM " + s.FromExpr.TableSQL()
	}
	if s.From == "" {
		return ""
	}
	if s.Alias != "" {
		return "FROM " + s.From + " AS " + s.Alias
	}
	return "FROM " + s.From
}

func (s SelectStmt) joinsSQL() string {
	if len(s.Joins) == 0 {
		return ""
	}
	var parts []string
	for _, j := range s.Joins {
		parts = append(parts, j.SQL())
	}
	return strings.Join(parts, "\n")
}

func (s SelectStmt) whereSQL() string {
	if s.Where == nil {
		return ""
	}
	return "WHERE " + s.Where.SQL()
}

func (s SelectStmt) limitSQL() string {
	if s.Limit <= 0 {
		return ""
	}
	return fmt.Sprintf("LIMIT %d", s.Limit)
}

// Exists wraps a query in EXISTS(...).
func (s SelectStmt) Exists() string {
	return fmt.Sprintf("EXISTS (\n%s\n)", s.SQL())
}

// NotExists wraps a query in NOT EXISTS(...).
func (s SelectStmt) NotExists() string {
	return fmt.Sprintf("NOT EXISTS (\n%s\n)", s.SQL())
}

// =============================================================================
// Values Tables (Inline Data)
// =============================================================================

// ValuesTable represents a VALUES clause as a table expression.
// Used to inline data like closure values without database tables.
//
// Example: ValuesTable{Values: "('doc', 'viewer', 'editor')", Alias: "c", Columns: []string{"object_type", "relation", "satisfying_relation"}}
// Renders: (VALUES ('doc', 'viewer', 'editor')) AS c(object_type, relation, satisfying_relation)
type ValuesTable struct {
	Values  string   // The VALUES content (e.g., "('a', 'b'), ('c', 'd')")
	Alias   string   // Table alias (e.g., "c")
	Columns []string // Column names (e.g., ["object_type", "relation"])
}

// SQL renders the VALUES table expression.
func (v ValuesTable) SQL() string {
	if len(v.Columns) == 0 {
		return "(VALUES " + v.Values + ") AS " + v.Alias
	}
	return "(VALUES " + v.Values + ") AS " + v.Alias + "(" + strings.Join(v.Columns, ", ") + ")"
}

// TableSQL implements TableExpr.
func (v ValuesTable) TableSQL() string {
	return v.SQL()
}

// TableAlias implements TableExpr.
func (v ValuesTable) TableAlias() string {
	return v.Alias
}

// ClosureValuesTable creates a standard closure VALUES table.
// The table has columns: object_type, relation, satisfying_relation
func ClosureValuesTable(values, alias string) ValuesTable {
	return ValuesTable{
		Values:  values,
		Alias:   alias,
		Columns: []string{"object_type", "relation", "satisfying_relation"},
	}
}

// UsersetValuesTable creates a standard userset VALUES table.
// The table has columns: object_type, relation, subject_type, subject_relation
func UsersetValuesTable(values, alias string) ValuesTable {
	return ValuesTable{
		Values:  values,
		Alias:   alias,
		Columns: []string{"object_type", "relation", "subject_type", "subject_relation"},
	}
}

// =============================================================================
// Typed Values Tables (Phase 5 - Expr-based inline data)
// =============================================================================

// ValuesRow represents a single row in a VALUES clause as typed expressions.
// Each element in the slice corresponds to a column value.
type ValuesRow []Expr

// SQL renders the row as (expr1, expr2, ...).
func (r ValuesRow) SQL() string {
	if len(r) == 0 {
		return "()"
	}
	parts := make([]string, len(r))
	for i, expr := range r {
		parts[i] = expr.SQL()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// TypedValuesTable represents a VALUES clause with typed expression rows.
// Unlike ValuesTable which uses a pre-formatted string, TypedValuesTable
// uses structured ValuesRow elements that render via the Expr DSL.
//
// Example:
//
//	TypedValuesTable{
//	    Rows: []ValuesRow{{Lit("doc"), Lit("viewer"), Lit("editor")}},
//	    Alias: "c",
//	    Columns: []string{"object_type", "relation", "satisfying_relation"},
//	}
//
// Renders: (VALUES ('doc', 'viewer', 'editor')) AS c(object_type, relation, satisfying_relation)
type TypedValuesTable struct {
	Rows    []ValuesRow // Typed expression rows
	Alias   string      // Table alias
	Columns []string    // Column names
}

// SQL renders the typed VALUES table expression.
func (v TypedValuesTable) SQL() string {
	values := v.valuesSQL()
	if len(v.Columns) == 0 {
		return "(VALUES " + values + ") AS " + v.Alias
	}
	return "(VALUES " + values + ") AS " + v.Alias + "(" + strings.Join(v.Columns, ", ") + ")"
}

// valuesSQL renders the rows portion of the VALUES clause.
func (v TypedValuesTable) valuesSQL() string {
	if len(v.Rows) == 0 {
		// Return a NULL row with correct column count based on Columns
		if len(v.Columns) > 0 {
			nulls := make([]string, len(v.Columns))
			for i := range nulls {
				nulls[i] = "NULL::TEXT"
			}
			return "(" + strings.Join(nulls, ", ") + ")"
		}
		return "(NULL::TEXT)"
	}
	parts := make([]string, len(v.Rows))
	for i, row := range v.Rows {
		parts[i] = row.SQL()
	}
	return strings.Join(parts, ", ")
}

// TableSQL implements TableExpr.
func (v TypedValuesTable) TableSQL() string {
	return v.SQL()
}

// TableAlias implements TableExpr.
func (v TypedValuesTable) TableAlias() string {
	return v.Alias
}

// TypedClosureValuesTable creates a typed closure VALUES table.
// The table has columns: object_type, relation, satisfying_relation
func TypedClosureValuesTable(rows []ValuesRow, alias string) TypedValuesTable {
	return TypedValuesTable{
		Rows:    rows,
		Alias:   alias,
		Columns: []string{"object_type", "relation", "satisfying_relation"},
	}
}

// TypedUsersetValuesTable creates a typed userset VALUES table.
// The table has columns: object_type, relation, subject_type, subject_relation
func TypedUsersetValuesTable(rows []ValuesRow, alias string) TypedValuesTable {
	return TypedValuesTable{
		Rows:    rows,
		Alias:   alias,
		Columns: []string{"object_type", "relation", "subject_type", "subject_relation"},
	}
}

// =============================================================================
// Transitional Factory Functions
// =============================================================================
// These helpers support gradual migration from string-based VALUES to typed rows.
// They prefer typed rows when available, falling back to string-based values.

// ClosureTable returns a closure VALUES table, preferring typed rows when available.
func ClosureTable(rows []ValuesRow, values string, alias string) TableExpr {
	if len(rows) > 0 {
		return TypedClosureValuesTable(rows, alias)
	}
	return ClosureValuesTable(values, alias)
}

// UsersetTable returns a userset VALUES table, preferring typed rows when available.
func UsersetTable(rows []ValuesRow, values string, alias string) TableExpr {
	if len(rows) > 0 {
		return TypedUsersetValuesTable(rows, alias)
	}
	return UsersetValuesTable(values, alias)
}

// =============================================================================
// SQL Formatting Helpers
// =============================================================================

// Ident sanitizes an identifier for use in SQL.
// Replaces non-alphanumeric characters with underscores.
func Ident(name string) string {
	var result strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			result.WriteRune(r)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// LateralFunction represents a LATERAL function call in a JOIN.
// Example: LateralFunction{Name: "list_doc_viewer_subjects", Args: []Expr{...}, Alias: "s"}
// Renders: LATERAL list_doc_viewer_subjects(...) AS s
type LateralFunction struct {
	Name  string
	Args  []Expr
	Alias string
}

// SQL renders the LATERAL function expression.
func (l LateralFunction) SQL() string {
	args := make([]string, len(l.Args))
	for i, arg := range l.Args {
		args[i] = arg.SQL()
	}
	call := "LATERAL " + l.Name + "(" + strings.Join(args, ", ") + ")"
	if l.Alias != "" {
		return call + " AS " + l.Alias
	}
	return call
}

// TableSQL implements TableExpr.
func (l LateralFunction) TableSQL() string {
	return l.SQL()
}

// TableAlias implements TableExpr.
func (l LateralFunction) TableAlias() string {
	return l.Alias
}

// =============================================================================
// Query Blocks (for UNION queries)
// =============================================================================

// QueryBlock represents a query with optional comments.
// Used to build UNION queries with descriptive comments for each branch.
type QueryBlock struct {
	Comments []string // Comment lines (without -- prefix)
	SQL      string   // The query SQL
}

// RenderBlocks renders multiple query blocks sequentially.
// Each block is indented and comments are rendered as SQL comments.
func RenderBlocks(blocks []QueryBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, len(blocks))
	for i, block := range blocks {
		parts[i] = renderSingleBlock(block)
	}
	return strings.Join(parts, "\n")
}

// RenderUnionBlocks renders query blocks joined with UNION.
// Each block is indented and comments are rendered as SQL comments.
func RenderUnionBlocks(blocks []QueryBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, len(blocks))
	for i, block := range blocks {
		parts[i] = renderSingleBlock(block)
	}
	return strings.Join(parts, "\n    UNION\n")
}

// renderSingleBlock renders a single query block with comments and indentation.
func renderSingleBlock(block QueryBlock) string {
	var lines []string
	for _, comment := range block.Comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, indentLines(block.SQL, "    "))
	return strings.Join(lines, "\n")
}

// indentLines adds the given indent prefix to each line of input.
func indentLines(input, indent string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}
