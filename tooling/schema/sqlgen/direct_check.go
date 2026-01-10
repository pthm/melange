package sqlgen

import (
	"fmt"
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

	q := Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.Relations...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Column: "subject_type"}, Values: input.SubjectTypes},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
