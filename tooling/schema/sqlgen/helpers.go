package sqlgen

import (
	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
)

func literalList(items []string) []bob.Expression {
	exprs := make([]bob.Expression, 0, len(items))
	for _, item := range items {
		exprs = append(exprs, psql.S(item))
	}
	return exprs
}

func param(name string) psql.Expression {
	return psql.Raw(name)
}

func subjectIDCheckExpr(column psql.Expression, allowWildcard bool) psql.Expression {
	if allowWildcard {
		return psql.Or(
			column.EQ(param("p_subject_id")),
			column.EQ(psql.S("*")),
		)
	}
	return psql.And(
		column.EQ(param("p_subject_id")),
		column.NE(psql.S("*")),
	)
}
