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
