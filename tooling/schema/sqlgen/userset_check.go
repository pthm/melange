package sqlgen

import (
	"fmt"

	"github.com/stephenafamo/bob"
	"github.com/stephenafamo/bob/dialect/psql"
	"github.com/stephenafamo/bob/dialect/psql/sm"
)

type UsersetCheckInput struct {
	ObjectType          string
	Relation            string
	SubjectType         string
	SubjectRelation     string
	SatisfyingRelations []string
	AllowWildcard       bool
}

func UsersetCheck(input UsersetCheckInput) (string, error) {
	if len(input.SatisfyingRelations) == 0 {
		return "", fmt.Errorf("userset check requires at least one satisfying relation")
	}

	relExprs := literalList(input.SatisfyingRelations)

	joinConditions := []bob.Expression{
		psql.Quote("membership", "object_type").EQ(psql.S(input.SubjectType)),
		psql.Quote("membership", "object_id").EQ(psql.Raw("split_part(grant_tuple.subject_id, '#', 1)")),
		psql.Quote("membership", "relation").In(relExprs...),
		psql.Quote("membership", "subject_type").EQ(param("p_subject_type")),
		subjectIDCheckExpr(psql.Quote("membership", "subject_id"), input.AllowWildcard),
	}

	where := psql.And(
		psql.Quote("grant_tuple", "object_type").EQ(psql.S(input.ObjectType)),
		psql.Quote("grant_tuple", "object_id").EQ(param("p_object_id")),
		psql.Quote("grant_tuple", "relation").EQ(psql.S(input.Relation)),
		psql.Quote("grant_tuple", "subject_type").EQ(psql.S(input.SubjectType)),
		psql.Raw("position('#' in grant_tuple.subject_id)").GT(psql.Raw("0")),
		psql.Raw("split_part(grant_tuple.subject_id, '#', 2)").EQ(psql.S(input.SubjectRelation)),
	)

	query := psql.Select(
		sm.Columns(psql.Raw("1")),
		sm.From("melange_tuples").As("grant_tuple"),
		sm.InnerJoin("melange_tuples").As("membership").On(joinConditions...),
		sm.Where(where),
		sm.Limit(1),
	)

	return existsSQL(query)
}
