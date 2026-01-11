package sqldsl

// TableExpr is the interface for table expressions in FROM and JOIN clauses.
// Types that can be used as table sources implement this interface.
type TableExpr interface {
	// TableSQL returns the SQL for use in FROM/JOIN clauses.
	TableSQL() string
	// TableAlias returns the alias if any (empty string if none).
	TableAlias() string
}

// TableRef wraps a raw table name for use as a TableExpr.
type TableRef struct {
	Name  string
	Alias string
}

// TableSQL implements TableExpr.
func (t TableRef) TableSQL() string {
	if t.Alias != "" {
		return t.Name + " AS " + t.Alias
	}
	return t.Name
}

// TableAlias implements TableExpr.
func (t TableRef) TableAlias() string {
	return t.Alias
}

// TableAs creates a table reference with an alias.
func TableAs(name, alias string) TableRef {
	return TableRef{Name: name, Alias: alias}
}

// FunctionCallExpr represents a function call that can be used as a table expression.
// Used for LATERAL joins with table-returning functions.
type FunctionCallExpr struct {
	Name  string // Function name
	Args  []Expr // Function arguments
	Alias string // Table alias for the result
}

// TableSQL implements TableExpr.
func (f FunctionCallExpr) TableSQL() string {
	args := make([]string, len(f.Args))
	for i, arg := range f.Args {
		args[i] = arg.SQL()
	}
	result := f.Name + "(" + joinStrings(args, ", ") + ")"
	if f.Alias != "" {
		result += " AS " + f.Alias
	}
	return result
}

// TableAlias implements TableExpr.
func (f FunctionCallExpr) TableAlias() string {
	return f.Alias
}

// joinStrings joins strings with a separator (local helper to avoid import).
func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for _, s := range strs[1:] {
		result += sep + s
	}
	return result
}
