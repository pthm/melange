package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type DirectCheckInput struct {
	ObjectType    string
	Relations     []string
	SubjectTypes  []string
	AllowWildcard bool
}

func DirectCheck(input DirectCheckInput) (string, error) {
	if len(input.Relations) == 0 {
		return "", fmt.Errorf("direct check requires at least one relation")
	}
	if len(input.SubjectTypes) == 0 {
		return "", fmt.Errorf("direct check requires at least one subject type")
	}

	relationExprs := literalList(input.Relations)
	subjectTypeExprs := literalList(input.SubjectTypes)

	where := psql.And(
		psql.Quote("object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("object_id").EQ(param("p_object_id")),
		psql.Quote("relation").In(relationExprs...),
		psql.Quote("subject_type").In(subjectTypeExprs...),
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
