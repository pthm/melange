package sqlgen

import (
	"fmt"
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

	q := Tuples("grant_tuple").
		ObjectType(input.ObjectType).
		Relations(input.Relation).
		Where(
			Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(input.SubjectType)},
			HasUserset{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
				Right: Lit(input.SubjectRelation),
			},
		).
		JoinTuples("membership",
			Eq{Left: Col{Table: "membership", Column: "object_type"}, Right: Lit(input.SubjectType)},
			Eq{
				Left:  Col{Table: "membership", Column: "object_id"},
				Right: UsersetObjectID{Source: Col{Table: "grant_tuple", Column: "subject_id"}},
			},
			In{Expr: Col{Table: "membership", Column: "relation"}, Values: input.SatisfyingRelations},
			Eq{Left: Col{Table: "membership", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "membership", Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
