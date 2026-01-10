package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
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

	q := dsl.Tuples("grant_tuple").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			dsl.Eq{Left: dsl.Col{Table: "grant_tuple", Column: "object_id"}, Right: dsl.ObjectID},
			dsl.Eq{Left: dsl.Col{Table: "grant_tuple", Column: "subject_type"}, Right: dsl.Lit(input.SubjectType)},
			dsl.HasUserset{Source: dsl.Col{Table: "grant_tuple", Column: "subject_id"}},
			dsl.Eq{
				Left:  dsl.UsersetRelation{Source: dsl.Col{Table: "grant_tuple", Column: "subject_id"}},
				Right: dsl.Lit(input.SubjectRelation),
			},
		).
		JoinTuples("membership",
			dsl.Eq{Left: dsl.Col{Table: "membership", Column: "object_type"}, Right: dsl.Lit(input.SubjectType)},
			dsl.Eq{
				Left:  dsl.Col{Table: "membership", Column: "object_id"},
				Right: dsl.UsersetObjectID{Source: dsl.Col{Table: "grant_tuple", Column: "subject_id"}},
			},
			dsl.In{Expr: dsl.Col{Table: "membership", Column: "relation"}, Values: input.SatisfyingRelations},
			dsl.Eq{Left: dsl.Col{Table: "membership", Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.SubjectIDMatch(dsl.Col{Table: "membership", Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
