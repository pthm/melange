// Package tuples provides tuple-table specific builders and helpers.
package tuples

import (
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// TupleQuery is a fluent builder for queries against melange_tuples.
type TupleQuery struct {
	alias       string
	objectType  string
	relations   []string
	conditions  []sqldsl.Expr
	columns     []string      // Deprecated: use columnExprs
	columnExprs []sqldsl.Expr // Preferred: typed column expressions
	joins       []sqldsl.JoinClause
	distinct    bool
	limit       int
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

// SelectExpr adds typed expressions as columns.
func (q *TupleQuery) SelectExpr(exprs ...sqldsl.Expr) *TupleQuery {
	q.columnExprs = append(q.columnExprs, exprs...)
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
func (q *TupleQuery) Where(exprs ...sqldsl.Expr) *TupleQuery {
	for _, e := range exprs {
		if e != nil {
			q.conditions = append(q.conditions, e)
		}
	}
	return q
}

// WhereSubject adds conditions for matching subject type and ID.
func (q *TupleQuery) WhereSubject(ref sqldsl.SubjectRef) *TupleQuery {
	q.conditions = append(q.conditions,
		sqldsl.Eq{Left: q.col("subject_type"), Right: ref.Type},
		sqldsl.Eq{Left: q.col("subject_id"), Right: ref.ID},
	)
	return q
}

// WhereSubjectType adds a condition for subject_type equals.
func (q *TupleQuery) WhereSubjectType(t sqldsl.Expr) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.Eq{Left: q.col("subject_type"), Right: t})
	return q
}

// WhereSubjectTypeIn adds a condition for subject_type IN.
func (q *TupleQuery) WhereSubjectTypeIn(types ...string) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.In{Expr: q.col("subject_type"), Values: types})
	return q
}

// WhereSubjectID adds a condition for subject_id matching.
// If allowWildcard is true, also matches "*".
func (q *TupleQuery) WhereSubjectID(id sqldsl.Expr, allowWildcard bool) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.SubjectIDMatch(q.col("subject_id"), id, allowWildcard))
	return q
}

// WhereObject adds conditions for matching object type and ID.
func (q *TupleQuery) WhereObject(ref sqldsl.ObjectRef) *TupleQuery {
	q.conditions = append(q.conditions,
		sqldsl.Eq{Left: q.col("object_type"), Right: ref.Type},
		sqldsl.Eq{Left: q.col("object_id"), Right: ref.ID},
	)
	return q
}

// WhereObjectID adds a condition for object_id equals.
func (q *TupleQuery) WhereObjectID(id sqldsl.Expr) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.Eq{Left: q.col("object_id"), Right: id})
	return q
}

// WhereHasUserset adds a condition requiring subject_id to contain '#'.
func (q *TupleQuery) WhereHasUserset() *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.HasUserset{Source: q.col("subject_id")})
	return q
}

// WhereNoUserset adds a condition requiring subject_id to NOT contain '#'.
func (q *TupleQuery) WhereNoUserset() *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.NoUserset{Source: q.col("subject_id")})
	return q
}

// WhereUsersetRelation adds a condition for the userset relation part.
func (q *TupleQuery) WhereUsersetRelation(rel string) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.Eq{
		Left:  sqldsl.UsersetRelation{Source: q.col("subject_id")},
		Right: sqldsl.Lit(rel),
	})
	return q
}

// WhereUsersetRelationLike adds an optimized LIKE condition for userset relation matching.
// This replaces the combination of WhereHasUserset() + WhereUsersetRelation() with a single
// LIKE pattern match: subject_id LIKE '%#relation' instead of position('#' in subject_id) > 0
// AND split_part(subject_id, '#', 2) = 'relation'.
func (q *TupleQuery) WhereUsersetRelationLike(rel string) *TupleQuery {
	q.conditions = append(q.conditions, sqldsl.Like{
		Expr:    q.col("subject_id"),
		Pattern: sqldsl.Lit("%#" + rel),
	})
	return q
}

// InnerJoin adds an INNER JOIN clause.
func (q *TupleQuery) InnerJoin(table, alias string, on ...sqldsl.Expr) *TupleQuery {
	return q.addJoin("INNER", table, alias, on)
}

// LeftJoin adds a LEFT JOIN clause.
func (q *TupleQuery) LeftJoin(table, alias string, on ...sqldsl.Expr) *TupleQuery {
	return q.addJoin("LEFT", table, alias, on)
}

func (q *TupleQuery) addJoin(joinType, table, alias string, on []sqldsl.Expr) *TupleQuery {
	q.joins = append(q.joins, sqldsl.JoinClause{
		Type:  joinType,
		Table: table,
		Alias: alias,
		On:    sqldsl.And(on...),
	})
	return q
}

// JoinTuples adds an INNER JOIN to melange_tuples with the given alias.
func (q *TupleQuery) JoinTuples(alias string, on ...sqldsl.Expr) *TupleQuery {
	return q.InnerJoin("melange_tuples", alias, on...)
}

// JoinRaw adds a JOIN with a raw table expression.
func (q *TupleQuery) JoinRaw(joinType, tableExpr string, on ...sqldsl.Expr) *TupleQuery {
	q.joins = append(q.joins, sqldsl.JoinClause{
		Type:  joinType,
		Table: tableExpr,
		Alias: "",
		On:    sqldsl.And(on...),
	})
	return q
}

// col returns a column reference for this query's table.
func (q *TupleQuery) col(name string) sqldsl.Col {
	return sqldsl.Col{Table: q.alias, Column: name}
}

// Build returns the declarative SelectStmt for inspection or testing.
func (q *TupleQuery) Build() sqldsl.SelectStmt {
	where := q.buildWhereConditions()

	var whereExpr sqldsl.Expr
	if len(where) > 0 {
		whereExpr = sqldsl.And(where...)
	}

	stmt := sqldsl.SelectStmt{
		Distinct: q.distinct,
		FromExpr: sqldsl.TableAs("melange_tuples", q.alias),
		Joins:    q.joins,
		Where:    whereExpr,
		Limit:    q.limit,
	}

	if len(q.columnExprs) > 0 {
		stmt.ColumnExprs = q.columnExprs
	} else if len(q.columns) > 0 {
		stmt.Columns = q.columns
	}

	return stmt
}

func (q *TupleQuery) buildWhereConditions() []sqldsl.Expr {
	var where []sqldsl.Expr
	if q.objectType != "" {
		where = append(where, sqldsl.Eq{Left: q.col("object_type"), Right: sqldsl.Lit(q.objectType)})
	}
	if len(q.relations) > 0 {
		where = append(where, sqldsl.In{Expr: q.col("relation"), Values: q.relations})
	}
	return append(where, q.conditions...)
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
