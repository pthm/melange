package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/tooling/schema/sqlgen/dsl"
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

	q := dsl.Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			dsl.Eq{Left: dsl.Col{Column: "object_id"}, Right: dsl.ObjectID},
			dsl.In{Expr: dsl.Col{Column: "subject_type"}, Values: input.SubjectTypes},
			dsl.Eq{Left: dsl.Col{Column: "subject_type"}, Right: dsl.SubjectType},
			dsl.SubjectIDMatch(dsl.Col{Column: "subject_id"}, dsl.SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
