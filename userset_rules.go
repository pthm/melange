package melange

// UsersetRule represents a precomputed userset rule with relation closure applied.
// Each row means: a tuple with tuple_relation on object_type can satisfy relation
// when the tuple subject is subject_type#subject_relation.
type UsersetRule struct {
	ObjectType                string
	Relation                  string
	TupleRelation             string
	SubjectType               string
	SubjectRelation           string
	SubjectRelationSatisfying string
}

// ToUsersetRules expands userset references using the relation closure.
// This precomputes which tuple relations can satisfy a target relation for userset rules.
func ToUsersetRules(types []TypeDefinition, closureRows []ClosureRow) []UsersetRule {
	closureMap := make(map[string]map[string][]string)
	targetBySatisfying := make(map[string]map[string][]string)
	for _, row := range closureRows {
		if _, ok := closureMap[row.ObjectType]; !ok {
			closureMap[row.ObjectType] = make(map[string][]string)
		}
		closureMap[row.ObjectType][row.Relation] = append(
			closureMap[row.ObjectType][row.Relation],
			row.SatisfyingRelation,
		)

		if _, ok := targetBySatisfying[row.ObjectType]; !ok {
			targetBySatisfying[row.ObjectType] = make(map[string][]string)
		}
		targetBySatisfying[row.ObjectType][row.SatisfyingRelation] = append(
			targetBySatisfying[row.ObjectType][row.SatisfyingRelation],
			row.Relation,
		)
	}

	seen := make(map[string]struct{})
	var rules []UsersetRule

	for _, t := range types {
		for _, r := range t.Relations {
			if len(r.SubjectTypeRefs) == 0 {
				continue
			}

			targetRelations := targetBySatisfying[t.Name][r.Name]
			if len(targetRelations) == 0 {
				targetRelations = []string{r.Name}
			}

			for _, ref := range r.SubjectTypeRefs {
				if ref.Relation == "" {
					continue
				}

				subjectSatisfying := closureMap[ref.Type][ref.Relation]
				if len(subjectSatisfying) == 0 {
					subjectSatisfying = []string{ref.Relation}
				}

				for _, targetRel := range targetRelations {
					objectSatisfying := closureMap[t.Name][targetRel]
					if len(objectSatisfying) == 0 {
						objectSatisfying = []string{targetRel}
					}

					for _, tupleRel := range objectSatisfying {
						for _, subjectRel := range subjectSatisfying {
							key := t.Name + "\x00" + targetRel + "\x00" + tupleRel + "\x00" + ref.Type + "\x00" + ref.Relation + "\x00" + subjectRel
							if _, ok := seen[key]; ok {
								continue
							}
							seen[key] = struct{}{}

							rules = append(rules, UsersetRule{
								ObjectType:                t.Name,
								Relation:                  targetRel,
								TupleRelation:             tupleRel,
								SubjectType:               ref.Type,
								SubjectRelation:           ref.Relation,
								SubjectRelationSatisfying: subjectRel,
							})
						}
					}
				}
			}
		}
	}

	return rules
}
