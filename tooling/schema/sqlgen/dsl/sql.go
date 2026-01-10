// Package dsl provides a domain-specific SQL DSL for generating Melange authorization queries.
// It models authorization concepts directly rather than generic SQL syntax.
package dsl

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
