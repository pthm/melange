package sqldsl

import (
	"fmt"
	"strings"
)

// Sqlf formats SQL with automatic dedenting and blank line removal.
// The SQL shape is visible in the format string.
func Sqlf(format string, args ...any) string {
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

// Optf returns formatted string if condition is true, empty string otherwise.
// Useful for optional SQL clauses.
func Optf(cond bool, format string, args ...any) string {
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

	// Determine join keyword - don't add "JOIN" if Type already contains it
	// (e.g., "CROSS JOIN LATERAL" should not become "CROSS JOIN LATERAL JOIN")
	joinKeyword := j.Type + " JOIN"
	if strings.Contains(j.Type, "JOIN") {
		joinKeyword = j.Type
	}

	// CROSS JOIN doesn't have an ON clause
	if j.Type == "CROSS" || strings.HasPrefix(j.Type, "CROSS") || j.On == nil {
		return joinKeyword + " " + tableSQL
	}
	return joinKeyword + " " + tableSQL + " ON " + j.On.SQL()
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
	return Sqlf(`
		SELECT %s%s
		%s
		%s
		%s
		%s`,
		Optf(s.Distinct, "DISTINCT "),
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
// Intersect Subqueries
// =============================================================================

// IntersectSubquery represents an INTERSECT of multiple queries as a subquery.
// Used for intersection groups where all parts must be satisfied.
//
// Example: IntersectSubquery{Queries: [q1, q2], Alias: "ig"}
// Renders: (q1 INTERSECT q2) AS ig
type IntersectSubquery struct {
	Queries []SelectStmt
	Alias   string
}

// TableSQL renders the INTERSECT subquery as a FROM clause table expression.
func (i IntersectSubquery) TableSQL() string {
	if len(i.Queries) == 0 {
		return ""
	}
	if len(i.Queries) == 1 {
		return "(" + i.Queries[0].SQL() + ") AS " + i.Alias
	}

	var parts []string
	for _, q := range i.Queries {
		parts = append(parts, q.SQL())
	}
	return "(\n" + strings.Join(parts, "\nINTERSECT\n") + "\n) AS " + i.Alias
}

// TableAlias implements TableExpr.
func (i IntersectSubquery) TableAlias() string {
	return i.Alias
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

// SQLer is an interface for types that can render SQL.
// Both SelectStmt and RawSelectStmt implement this interface.
type SQLer interface {
	SQL() string
}

// QueryBlock represents a query with optional comments.
// Used to build UNION queries with descriptive comments for each branch.
type QueryBlock struct {
	Comments []string // Comment lines (without -- prefix)
	Query    SQLer    // The query as typed DSL (SelectStmt, RawSelectStmt, etc.)
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
	lines = append(lines, IndentLines(block.Query.SQL(), "    "))
	return strings.Join(lines, "\n")
}

// IndentLines adds the given indent prefix to each line of input.
func IndentLines(input, indent string) string {
	if input == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(input), "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

// =============================================================================
// Pagination Helpers
// =============================================================================

// WrapWithPagination wraps a query with cursor-based pagination.
// The idColumn parameter specifies which column to use for ordering and cursoring.
func WrapWithPagination(query, idColumn string) string {
	return fmt.Sprintf(`WITH base_results AS (
%s
    ),
    paged AS (
        SELECT br.%s
        FROM base_results br
        WHERE (p_after IS NULL OR br.%s > p_after)
        ORDER BY br.%s
        LIMIT CASE WHEN p_limit IS NULL THEN NULL ELSE p_limit + 1 END
    ),
    returned AS (
        SELECT p.%s FROM paged p ORDER BY p.%s LIMIT p_limit
    ),
    next AS (
        SELECT CASE
            WHEN p_limit IS NOT NULL AND (SELECT count(*) FROM paged) > p_limit
            THEN (SELECT max(r.%s) FROM returned r)
        END AS next_cursor
    )
    SELECT r.%s, n.next_cursor
    FROM returned r
    CROSS JOIN next n`,
		IndentLines(query, "        "), idColumn, idColumn, idColumn,
		idColumn, idColumn, idColumn, idColumn)
}

// WrapWithPaginationWildcardFirst wraps a query for list_subjects with wildcard-first ordering.
// Wildcards ('*') are sorted before all other subject IDs to ensure consistent pagination.
// Uses a compound sort key: (is_not_wildcard, subject_id) where is_not_wildcard is 0 for '*', 1 otherwise.
func WrapWithPaginationWildcardFirst(query string) string {
	return fmt.Sprintf(`WITH base_results AS (
%s
    ),
    paged AS (
        SELECT br.subject_id
        FROM base_results br
        WHERE p_after IS NULL OR (
            -- Compound comparison for wildcard-first ordering:
            -- (is_not_wildcard, subject_id) > (cursor_is_not_wildcard, cursor)
            (CASE WHEN br.subject_id = '*' THEN 0 ELSE 1 END, br.subject_id) >
            (CASE WHEN p_after = '*' THEN 0 ELSE 1 END, p_after)
        )
        ORDER BY (CASE WHEN br.subject_id = '*' THEN 0 ELSE 1 END), br.subject_id
        LIMIT CASE WHEN p_limit IS NULL THEN NULL ELSE p_limit + 1 END
    ),
    returned AS (
        SELECT p.subject_id FROM paged p
        ORDER BY (CASE WHEN p.subject_id = '*' THEN 0 ELSE 1 END), p.subject_id
        LIMIT p_limit
    ),
    next AS (
        SELECT CASE
            WHEN p_limit IS NOT NULL AND (SELECT count(*) FROM paged) > p_limit
            THEN (SELECT r.subject_id FROM returned r
                  ORDER BY (CASE WHEN r.subject_id = '*' THEN 0 ELSE 1 END) DESC, r.subject_id DESC
                  LIMIT 1)
        END AS next_cursor
    )
    SELECT r.subject_id, n.next_cursor
    FROM returned r
    CROSS JOIN next n`,
		IndentLines(query, "        "))
}
