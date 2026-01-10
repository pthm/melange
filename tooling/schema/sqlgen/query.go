package sqlgen

import "fmt"

// TupleQuery is a fluent builder for queries against melange_tuples.
type TupleQuery struct {
	alias      string
	objectType string
	relations  []string
	conditions []Expr
	columns    []string
	joins      []JoinClause
	distinct   bool
	limit      int
}

// Tuples creates a new TupleQuery with the given table alias.
func Tuples(alias string) *TupleQuery {
	return &TupleQuery{
		alias:   alias,
		columns: []string{}, // Default empty, will be set with Select()
	}
}

// Alias returns the query's table alias.
func (q *TupleQuery) Alias() string {
	return q.alias
}

// ObjectType sets the object_type filter.
func (q *TupleQuery) ObjectType(t string) *TupleQuery {
	q.objectType = t
	return q
}

// Relations sets the relation filter (IN clause).
func (q *TupleQuery) Relations(rels ...string) *TupleQuery {
	q.relations = rels
	return q
}

// Select sets the columns to select.
func (q *TupleQuery) Select(cols ...string) *TupleQuery {
	q.columns = cols
	return q
}

// SelectCol adds a column with automatic table prefix.
func (q *TupleQuery) SelectCol(columns ...string) *TupleQuery {
	for _, c := range columns {
		q.columns = append(q.columns, q.alias+"."+c)
	}
	return q
}

// SelectExpr adds an expression as a column.
func (q *TupleQuery) SelectExpr(exprs ...Expr) *TupleQuery {
	for _, e := range exprs {
		q.columns = append(q.columns, e.SQL())
	}
	return q
}

// Distinct enables DISTINCT in the SELECT.
func (q *TupleQuery) Distinct() *TupleQuery {
	q.distinct = true
	return q
}

// Limit sets the LIMIT clause.
func (q *TupleQuery) Limit(n int) *TupleQuery {
	q.limit = n
	return q
}

// Where adds arbitrary WHERE conditions.
func (q *TupleQuery) Where(exprs ...Expr) *TupleQuery {
	for _, e := range exprs {
		if e != nil {
			q.conditions = append(q.conditions, e)
		}
	}
	return q
}

// WhereSubject adds conditions for matching subject type and ID.
func (q *TupleQuery) WhereSubject(ref SubjectRef) *TupleQuery {
	q.conditions = append(q.conditions,
		Eq{q.col("subject_type"), ref.Type},
		Eq{q.col("subject_id"), ref.ID},
	)
	return q
}

// WhereSubjectType adds a condition for subject_type equals.
func (q *TupleQuery) WhereSubjectType(t Expr) *TupleQuery {
	q.conditions = append(q.conditions, Eq{q.col("subject_type"), t})
	return q
}

// WhereSubjectTypeIn adds a condition for subject_type IN.
func (q *TupleQuery) WhereSubjectTypeIn(types ...string) *TupleQuery {
	q.conditions = append(q.conditions, In{Expr: q.col("subject_type"), Values: types})
	return q
}

// WhereSubjectID adds a condition for subject_id matching.
// If allowWildcard is true, also matches "*".
func (q *TupleQuery) WhereSubjectID(id Expr, allowWildcard bool) *TupleQuery {
	q.conditions = append(q.conditions, SubjectIDMatch(q.col("subject_id"), id, allowWildcard))
	return q
}

// WhereObject adds conditions for matching object type and ID.
func (q *TupleQuery) WhereObject(ref ObjectRef) *TupleQuery {
	q.conditions = append(q.conditions,
		Eq{q.col("object_type"), ref.Type},
		Eq{q.col("object_id"), ref.ID},
	)
	return q
}

// WhereObjectID adds a condition for object_id equals.
func (q *TupleQuery) WhereObjectID(id Expr) *TupleQuery {
	q.conditions = append(q.conditions, Eq{q.col("object_id"), id})
	return q
}

// WhereHasUserset adds a condition requiring subject_id to contain '#'.
func (q *TupleQuery) WhereHasUserset() *TupleQuery {
	q.conditions = append(q.conditions, HasUserset{q.col("subject_id")})
	return q
}

// WhereNoUserset adds a condition requiring subject_id to NOT contain '#'.
func (q *TupleQuery) WhereNoUserset() *TupleQuery {
	q.conditions = append(q.conditions, NoUserset{q.col("subject_id")})
	return q
}

// WhereUsersetRelation adds a condition for the userset relation part.
func (q *TupleQuery) WhereUsersetRelation(rel string) *TupleQuery {
	q.conditions = append(q.conditions, Eq{
		UsersetRelation{q.col("subject_id")},
		Lit(rel),
	})
	return q
}

// InnerJoin adds an INNER JOIN clause.
func (q *TupleQuery) InnerJoin(table, alias string, on ...Expr) *TupleQuery {
	q.joins = append(q.joins, JoinClause{
		Type:  "INNER",
		Table: table,
		Alias: alias,
		On:    And(on...),
	})
	return q
}

// LeftJoin adds a LEFT JOIN clause.
func (q *TupleQuery) LeftJoin(table, alias string, on ...Expr) *TupleQuery {
	q.joins = append(q.joins, JoinClause{
		Type:  "LEFT",
		Table: table,
		Alias: alias,
		On:    And(on...),
	})
	return q
}

// JoinTuples adds an INNER JOIN to melange_tuples with the given alias.
func (q *TupleQuery) JoinTuples(alias string, on ...Expr) *TupleQuery {
	return q.InnerJoin("melange_tuples", alias, on...)
}

// JoinClosure adds an INNER JOIN to an inline VALUES closure table.
// closureValues should be in the format "'type1','rel1','sat1'),('type2','rel2','sat2')"
func (q *TupleQuery) JoinClosure(alias, closureValues string, on ...Expr) *TupleQuery {
	valuesTable := fmt.Sprintf("(VALUES %s) AS %s(object_type, relation, satisfying_relation)",
		closureValues, alias)
	q.joins = append(q.joins, JoinClause{
		Type:  "INNER",
		Table: valuesTable,
		Alias: "", // Already included in valuesTable
		On:    And(on...),
	})
	return q
}

// JoinRaw adds a JOIN with a raw table expression.
func (q *TupleQuery) JoinRaw(joinType, tableExpr string, on ...Expr) *TupleQuery {
	q.joins = append(q.joins, JoinClause{
		Type:  joinType,
		Table: tableExpr,
		Alias: "",
		On:    And(on...),
	})
	return q
}

// col returns a column reference for this query's table.
func (q *TupleQuery) col(name string) Col {
	return Col{Table: q.alias, Column: name}
}

// Col returns a column reference for this query's table (public API).
func (q *TupleQuery) Col(name string) Col {
	return q.col(name)
}

// Build returns the declarative SelectStmt for inspection or testing.
func (q *TupleQuery) Build() SelectStmt {
	// Collect WHERE conditions
	var where []Expr
	if q.objectType != "" {
		where = append(where, Eq{q.col("object_type"), Lit(q.objectType)})
	}
	if len(q.relations) > 0 {
		where = append(where, In{Expr: q.col("relation"), Values: q.relations})
	}
	where = append(where, q.conditions...)

	// Build the WHERE clause
	var whereExpr Expr
	if len(where) > 0 {
		whereExpr = And(where...)
	}

	// Default columns if none specified
	columns := q.columns
	if len(columns) == 0 {
		columns = []string{"1"}
	}

	return SelectStmt{
		Distinct: q.distinct,
		Columns:  columns,
		From:     "melange_tuples",
		Alias:    q.alias,
		Joins:    q.joins,
		Where:    whereExpr,
		Limit:    q.limit,
	}
}

// SQL renders the query to a SQL string.
func (q *TupleQuery) SQL() string {
	return q.Build().SQL()
}

// ExistsSQL returns the query wrapped in EXISTS(...).
func (q *TupleQuery) ExistsSQL() string {
	return q.Build().Exists()
}

// NotExistsSQL returns the query wrapped in NOT EXISTS(...).
func (q *TupleQuery) NotExistsSQL() string {
	return q.Build().NotExists()
}
