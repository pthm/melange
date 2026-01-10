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
	Type  string // "INNER", "LEFT", etc.
	Table string
	Alias string
	On    Expr
}

// SQL renders the JOIN clause.
func (j JoinClause) SQL() string {
	alias := ""
	if j.Alias != "" {
		alias = " AS " + j.Alias
	}
	// CROSS JOIN doesn't have an ON clause
	if j.Type == "CROSS" || j.On == nil {
		return fmt.Sprintf("%s JOIN %s%s", j.Type, j.Table, alias)
	}
	return fmt.Sprintf("%s JOIN %s%s ON %s", j.Type, j.Table, alias, j.On.SQL())
}

// SelectStmt represents a SELECT query.
type SelectStmt struct {
	Distinct bool
	Columns  []string
	From     string
	Alias    string
	Joins    []JoinClause
	Where    Expr
	Limit    int
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
		strings.Join(s.Columns, ", "),
		s.fromSQL(),
		s.joinsSQL(),
		s.whereSQL(),
		s.limitSQL(),
	)
}

func (s SelectStmt) fromSQL() string {
	if s.From == "" {
		return ""
	}
	return fmt.Sprintf("FROM %s%s", s.From, optf(s.Alias != "", " AS %s", s.Alias))
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
		return fmt.Sprintf("(VALUES %s) AS %s", v.Values, v.Alias)
	}
	return fmt.Sprintf("(VALUES %s) AS %s(%s)", v.Values, v.Alias, strings.Join(v.Columns, ", "))
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

// ListLiterals formats a slice of strings as a SQL list of literals.
// Example: ListLiterals([]string{"a", "b"}) returns "'a', 'b'"
// Returns empty string for empty slice - callers should handle this case
// appropriately (e.g., not generating IN clauses for empty lists).
func ListLiterals(values []string) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, len(values))
	for i, v := range values {
		// Escape single quotes
		escaped := strings.ReplaceAll(v, "'", "''")
		parts[i] = "'" + escaped + "'"
	}
	return strings.Join(parts, ", ")
}

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

// =============================================================================
// Function/Table Expressions
// =============================================================================

// FunctionTable represents a function call used as a table source.
// Example: FunctionTable{Name: "list_doc_viewer_objects", Args: []Expr{Param("p_subject_type"), Param("p_subject_id")}, Alias: "f"}
// Renders: list_doc_viewer_objects(p_subject_type, p_subject_id) AS f
type FunctionTable struct {
	Name  string
	Args  []Expr
	Alias string
}

// SQL renders the function table expression.
func (f FunctionTable) SQL() string {
	args := make([]string, len(f.Args))
	for i, arg := range f.Args {
		args[i] = arg.SQL()
	}
	call := f.Name + "(" + strings.Join(args, ", ") + ")"
	if f.Alias != "" {
		return call + " AS " + f.Alias
	}
	return call
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

// CrossJoinLateral creates a JoinClause for CROSS JOIN LATERAL with a function.
func CrossJoinLateral(funcName string, args []Expr, alias string) JoinClause {
	return JoinClause{
		Type:  "CROSS",
		Table: LateralFunction{Name: funcName, Args: args, Alias: alias}.SQL(),
	}
}
