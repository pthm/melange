package sqlgen

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

// Table creates a simple table reference.
func Table(name string) TableRef {
	return TableRef{Name: name}
}

// TableAs creates a table reference with an alias.
func TableAs(name, alias string) TableRef {
	return TableRef{Name: name, Alias: alias}
}

// RawTableExpr wraps a raw SQL string as a TableExpr.
// Use for complex table expressions that don't fit other types.
type RawTableExpr struct {
	SQL   string
	Alias string
}

// TableSQL implements TableExpr.
func (r RawTableExpr) TableSQL() string {
	return r.SQL
}

// TableAlias implements TableExpr.
func (r RawTableExpr) TableAlias() string {
	return r.Alias
}

// RawTable creates a raw table expression.
func RawTable(sql string) RawTableExpr {
	return RawTableExpr{SQL: sql}
}

// RawTableAs creates a raw table expression with an alias.
func RawTableAs(sql, alias string) RawTableExpr {
	return RawTableExpr{SQL: sql, Alias: alias}
}
