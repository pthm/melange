package sqlgen

type ExclusionCheckInput struct {
	ObjectType       string
	ExcludedRelation string
	AllowWildcard    bool
}

func ExclusionCheck(input ExclusionCheckInput) (string, error) {
	q := Tuples("").
		ObjectType(input.ObjectType).
		Relations(input.ExcludedRelation).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, input.AllowWildcard),
		).
		Select("1").
		Limit(1)

	return q.ExistsSQL(), nil
}
