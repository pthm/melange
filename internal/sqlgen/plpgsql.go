package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// PL/pgSQL Block Helpers
// =============================================================================

// VarDecl represents a PL/pgSQL variable declaration.
type VarDecl struct {
	Name    string
	Type    string
	Default Expr // nil for no default
}

// SQL renders the variable declaration.
func (v VarDecl) SQL() string {
	if v.Default != nil {
		return fmt.Sprintf("%s %s := %s;", v.Name, v.Type, v.Default.SQL())
	}
	return fmt.Sprintf("%s %s;", v.Name, v.Type)
}

// DeclareBlock renders a DECLARE block with variable declarations.
func DeclareBlock(vars ...VarDecl) string {
	if len(vars) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("DECLARE\n")
	for _, v := range vars {
		b.WriteString("    ")
		b.WriteString(v.SQL())
		b.WriteString("\n")
	}
	return b.String()
}

// ReturnQuery wraps a SQL query in RETURN QUERY.
func ReturnQuery(sql string) string {
	return "RETURN QUERY\n" + sql + ";"
}

// ReturnQueryExpr wraps an Expr in RETURN QUERY.
func ReturnQueryExpr(expr Expr) string {
	return "RETURN QUERY\n" + expr.SQL() + ";"
}

// ReturnValue returns a simple RETURN statement.
func ReturnValue(expr Expr) string {
	return "RETURN " + expr.SQL() + ";"
}

// indentBlock adds indentation to each line of a multi-line string.
func indentBlock(s, indent string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

// IfThen represents a simple IF ... THEN ... END IF block.
type IfThen struct {
	Condition Expr
	Body      string
}

// SQL renders the IF block.
func (i IfThen) SQL() string {
	return fmt.Sprintf("IF %s THEN\n%s\nEND IF;", i.Condition.SQL(), indentBlock(i.Body, "    "))
}

// IfThenElse represents an IF ... THEN ... ELSE ... END IF block.
type IfThenElse struct {
	Condition Expr
	Then      string
	Else      string
}

// SQL renders the IF/ELSE block.
func (i IfThenElse) SQL() string {
	return fmt.Sprintf("IF %s THEN\n%s\nELSE\n%s\nEND IF;",
		i.Condition.SQL(),
		indentBlock(i.Then, "    "),
		indentBlock(i.Else, "    "))
}

// FunctionBody builds a complete PL/pgSQL function body.
type FunctionBody struct {
	Declares []VarDecl
	Body     []string // Lines of PL/pgSQL code
}

// SQL renders the complete function body.
func (f FunctionBody) SQL() string {
	var b strings.Builder

	// DECLARE block
	if len(f.Declares) > 0 {
		b.WriteString(DeclareBlock(f.Declares...))
	}

	// BEGIN block
	b.WriteString("BEGIN\n")
	for _, line := range f.Body {
		// Handle multi-line entries by indenting each line
		b.WriteString(indentBlock(line, "    "))
		b.WriteString("\n")
	}
	b.WriteString("END;")

	return b.String()
}

// FunctionSignature represents a SQL function signature.
type FunctionSignature struct {
	Name       string
	Params     []FunctionParam
	ReturnType string
}

// FunctionParam represents a function parameter.
type FunctionParam struct {
	Name string
	Type string
}

// SQL renders the function signature.
func (f FunctionSignature) SQL() string {
	params := make([]string, len(f.Params))
	for i, p := range f.Params {
		params[i] = fmt.Sprintf("%s %s", p.Name, p.Type)
	}
	return fmt.Sprintf("%s(%s) RETURNS %s", f.Name, strings.Join(params, ", "), f.ReturnType)
}

// CreateFunction renders a complete CREATE OR REPLACE FUNCTION statement.
func CreateFunction(sig FunctionSignature, body FunctionBody, options ...string) string {
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE FUNCTION ")
	b.WriteString(sig.SQL())
	b.WriteString(" AS $$\n")
	b.WriteString(body.SQL())
	b.WriteString("\n$$ LANGUAGE plpgsql")
	for _, opt := range options {
		b.WriteString(" ")
		b.WriteString(opt)
	}
	b.WriteString(";\n")
	return b.String()
}

// Common function options.
const (
	FuncStable    = "STABLE"
	FuncImmutable = "IMMUTABLE"
	FuncVolatile  = "VOLATILE"
)
