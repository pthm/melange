package sqlgen

import (
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type ExclusionCheckInput struct {
	ObjectType       string
	ExcludedRelation string
	AllowWildcard    bool
}

func ExclusionCheck(input ExclusionCheckInput) (string, error) {
	where := psql.And(
		psql.Quote("object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("object_id").EQ(param("p_object_id")),
		psql.Quote("relation").EQ(psql.S(input.ExcludedRelation)),
		psql.Quote("subject_type").EQ(param("p_subject_type")),
		subjectIDCheckExpr(psql.Quote("subject_id"), input.AllowWildcard),
	)

	query := psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From("melange_tuples"),
		sm.Where(where),
		sm.Limit(1),
	)

	return existsSQL(query)
}
