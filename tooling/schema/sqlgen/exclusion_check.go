package sqlgen

import (
	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
)

type ExclusionCheckInput struct {
	ObjectType       string
	ExcludedRelation string
	AllowWildcard    bool
}

func ExclusionCheck(input ExclusionCheckInput) (string, error) {
	q := dsl.Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.ExcludedRelation).
		Where(
			dsl.Eq{Left: dsl.Col{Column: "object_id"}, Right: dsl.ObjectID},
			dsl.Eq{Left: dsl.Col{Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.SubjectIDMatch(dsl.Col{Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
