// Package plpgsql provides PL/pgSQL function builder types.
package plpgsql

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// =============================================================================
// PL/pgSQL Function Builder
// =============================================================================
//
// This file provides a typed builder for PL/pgSQL functions, centralizing the
// formatting and indentation of CREATE FUNCTION statements.

// FuncArg represents a function argument with optional default value.
type FuncArg struct {
	Name    string
	Type    string
	Default sqldsl.Expr // nil means no default
}

// Decl represents a DECLARE variable declaration.
type Decl struct {
	Name string
	Type string
}

// Stmt is a PL/pgSQL statement that can be rendered to SQL.
type Stmt interface {
	StmtSQL() string
}

// ReturnQuery renders RETURN QUERY <query>;
type ReturnQuery struct {
	Query string // Already-rendered SQL query
}

func (r ReturnQuery) StmtSQL() string {
	return fmt.Sprintf("RETURN QUERY\n    %s;", r.Query)
}

// Return renders a bare RETURN; statement.
type Return struct{}

func (Return) StmtSQL() string {
	return "RETURN;"
}

// ReturnValue renders RETURN <expr>;
type ReturnValue struct {
	Value sqldsl.Expr
}

func (r ReturnValue) StmtSQL() string {
	return fmt.Sprintf("RETURN %s;", r.Value.SQL())
}

// ReturnInt renders RETURN <int>; as a convenience.
type ReturnInt struct {
	Value int
}

func (r ReturnInt) StmtSQL() string {
	return fmt.Sprintf("RETURN %d;", r.Value)
}

// Assign renders name := value;
type Assign struct {
	Name  string
	Value sqldsl.Expr
}

func (a Assign) StmtSQL() string {
	return fmt.Sprintf("%s := %s;", a.Name, a.Value.SQL())
}

// If renders IF cond THEN ... [ELSE ...] END IF;
type If struct {
	Cond sqldsl.Expr
	Then []Stmt
	Else []Stmt
}

func (i If) StmtSQL() string {
	var sb strings.Builder
	sb.WriteString("IF ")
	sb.WriteString(i.Cond.SQL())
	sb.WriteString(" THEN\n")

	for _, stmt := range i.Then {
		sb.WriteString("    ")
		sb.WriteString(stmt.StmtSQL())
		sb.WriteString("\n")
	}

	if len(i.Else) > 0 {
		sb.WriteString("ELSE\n")
		for _, stmt := range i.Else {
			sb.WriteString("    ")
			sb.WriteString(stmt.StmtSQL())
			sb.WriteString("\n")
		}
	}

	sb.WriteString("END IF;")
	return sb.String()
}

// SelectInto renders SELECT ... INTO variable;
// Used for assigning query results to PL/pgSQL variables.
type SelectInto struct {
	Query    sqldsl.SQLer // The SELECT query
	Variable string       // Variable name to select into
}

func (s SelectInto) StmtSQL() string {
	q := s.Query.SQL()
	// Insert INTO clause after SELECT
	if len(q) >= 6 && q[:6] == "SELECT" {
		return "SELECT" + " INTO " + s.Variable + q[6:] + ";"
	}
	// Fallback: wrap the query
	return fmt.Sprintf("SELECT INTO %s (%s);", s.Variable, q)
}

// RawStmt is an escape hatch for SQL that doesn't map cleanly to typed constructs.
// Use sparingly.
type RawStmt struct {
	SQLText string
}

func (r RawStmt) StmtSQL() string {
	return r.SQLText
}

// Raise renders RAISE EXCEPTION 'message' USING ERRCODE = 'code';
type Raise struct {
	Message string
	ErrCode string
}

func (r Raise) StmtSQL() string {
	return fmt.Sprintf("RAISE EXCEPTION '%s' USING ERRCODE = '%s';", r.Message, r.ErrCode)
}

// Comment renders a SQL comment line.
type Comment struct {
	Text string
}

func (c Comment) StmtSQL() string {
	return "-- " + c.Text
}

// PlpgsqlFunction represents a complete PL/pgSQL function definition.
type PlpgsqlFunction struct {
	Name    string
	Args    []FuncArg
	Returns string
	Decls   []Decl
	Body    []Stmt
	Header  []string // Comment lines at the top of the function (without -- prefix)
}

// SQL renders the complete CREATE OR REPLACE FUNCTION statement.
func (f PlpgsqlFunction) SQL() string {
	var sb strings.Builder

	writeHeader(&sb, f.Header)
	writeSignature(&sb, f.Name, f.Args, f.Returns)

	if len(f.Decls) > 0 {
		sb.WriteString("DECLARE\n")
		for _, d := range f.Decls {
			sb.WriteString("    ")
			sb.WriteString(d.Name)
			sb.WriteString(" ")
			sb.WriteString(d.Type)
			sb.WriteString(";\n")
		}
	}

	sb.WriteString("BEGIN\n")
	for _, stmt := range f.Body {
		for _, line := range strings.Split(stmt.StmtSQL(), "\n") {
			sb.WriteString("    ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString("END;\n")
	sb.WriteString("$$ LANGUAGE plpgsql STABLE;")

	return sb.String()
}

// =============================================================================
// Convenience Constructors
// =============================================================================

// ListObjectsArgs returns the standard arguments for a list_objects function.
func ListObjectsArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_limit", Type: "INT", Default: sqldsl.Null{}},
		{Name: "p_after", Type: "TEXT", Default: sqldsl.Null{}},
	}
}

// ListSubjectsArgs returns the standard arguments for a list_subjects function.
func ListSubjectsArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_limit", Type: "INT", Default: sqldsl.Null{}},
		{Name: "p_after", Type: "TEXT", Default: sqldsl.Null{}},
	}
}

// ListObjectsReturns returns the standard RETURNS clause for list_objects.
func ListObjectsReturns() string {
	return "TABLE(object_id TEXT, next_cursor TEXT)"
}

// ListSubjectsReturns returns the standard RETURNS clause for list_subjects.
func ListSubjectsReturns() string {
	return "TABLE(subject_id TEXT, next_cursor TEXT)"
}

// ListObjectsFunctionHeader creates header comments for a list_objects function.
func ListObjectsFunctionHeader(objectType, relation, features string) []string {
	return []string{
		fmt.Sprintf("Generated list_objects function for %s.%s", objectType, relation),
		fmt.Sprintf("Features: %s", features),
	}
}

// ListSubjectsFunctionHeader creates header comments for a list_subjects function.
func ListSubjectsFunctionHeader(objectType, relation, features string) []string {
	return []string{
		fmt.Sprintf("Generated list_subjects function for %s.%s", objectType, relation),
		fmt.Sprintf("Features: %s", features),
	}
}

// ListObjectsDispatcherArgs returns the arguments for the list_accessible_objects dispatcher.
func ListObjectsDispatcherArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_limit", Type: "INT", Default: sqldsl.Null{}},
		{Name: "p_after", Type: "TEXT", Default: sqldsl.Null{}},
	}
}

// ListSubjectsDispatcherArgs returns the arguments for the list_accessible_subjects dispatcher.
func ListSubjectsDispatcherArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_limit", Type: "INT", Default: sqldsl.Null{}},
		{Name: "p_after", Type: "TEXT", Default: sqldsl.Null{}},
	}
}

// =============================================================================
// Simple SQL Function Builder (non-PL/pgSQL)
// =============================================================================

// SqlFunction represents a simple SQL function (LANGUAGE sql).
// Unlike PlpgsqlFunction, this renders a single SELECT expression as the body.
type SqlFunction struct {
	Name    string
	Args    []FuncArg
	Returns string
	Body    sqldsl.SQLer // The body expression (e.g., a SelectStmt or function call)
	Header  []string     // Comment lines at the top of the function (without -- prefix)
}

// SQL renders the complete CREATE OR REPLACE FUNCTION statement as LANGUAGE sql.
func (f SqlFunction) SQL() string {
	var sb strings.Builder

	writeHeader(&sb, f.Header)
	writeSignature(&sb, f.Name, f.Args, f.Returns)

	sb.WriteString("    ")
	sb.WriteString(f.Body.SQL())
	sb.WriteString(";\n")
	sb.WriteString("$$ LANGUAGE sql STABLE;")

	return sb.String()
}

// writeHeader writes SQL comment lines to the builder.
func writeHeader(sb *strings.Builder, header []string) {
	for _, comment := range header {
		sb.WriteString("-- ")
		sb.WriteString(comment)
		sb.WriteString("\n")
	}
}

// writeSignature writes CREATE OR REPLACE FUNCTION ... AS $$ to the builder.
func writeSignature(sb *strings.Builder, name string, args []FuncArg, returns string) {
	sb.WriteString("CREATE OR REPLACE FUNCTION ")
	sb.WriteString(name)
	sb.WriteString("(\n")

	for i, arg := range args {
		sb.WriteString("    ")
		sb.WriteString(arg.Name)
		sb.WriteString(" ")
		sb.WriteString(arg.Type)
		if arg.Default != nil {
			sb.WriteString(" DEFAULT ")
			sb.WriteString(arg.Default.SQL())
		}
		if i < len(args)-1 {
			sb.WriteString(",")
		}
		sb.WriteString("\n")
	}

	sb.WriteString(") RETURNS ")
	sb.WriteString(returns)
	sb.WriteString(" AS $$\n")
}
