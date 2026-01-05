package melange

// UsersetRule represents a precomputed userset rule with relation closure applied.
// Each row means: a tuple with tuple_relation on object_type can satisfy relation
// when the tuple subject is subject_type#subject_relation.
type UsersetRule struct {
	ObjectType      string
	Relation        string
	TupleRelation   string
	SubjectType     string
	SubjectRelation string
}

// ToUsersetRules expands userset references using the relation closure.
// This precomputes which tuple relations can satisfy a target relation for userset rules.
func ToUsersetRules(types []TypeDefinition, closureRows []ClosureRow) []UsersetRule {
	closureMap := make(map[string]map[string][]string)
	for _, row := range closureRows {
		if _, ok := closureMap[row.ObjectType]; !ok {
			closureMap[row.ObjectType] = make(map[string][]string)
		}
		closureMap[row.ObjectType][row.Relation] = append(
			closureMap[row.ObjectType][row.Relation],
			row.SatisfyingRelation,
		)
	}

	seen := make(map[string]struct{})
	var rules []UsersetRule

	for _, t := range types {
		for _, r := range t.Relations {
			if len(r.SubjectTypeRefs) == 0 {
				continue
			}

			satisfying := closureMap[t.Name][r.Name]
			if len(satisfying) == 0 {
				satisfying = []string{r.Name}
			}

			for _, ref := range r.SubjectTypeRefs {
				if ref.Relation == "" {
					continue
				}

				for _, tupleRel := range satisfying {
					key := t.Name + "\x00" + r.Name + "\x00" + tupleRel + "\x00" + ref.Type + "\x00" + ref.Relation
					if _, ok := seen[key]; ok {
						continue
					}
					seen[key] = struct{}{}

					rules = append(rules, UsersetRule{
						ObjectType:      t.Name,
						Relation:        r.Name,
						TupleRelation:   tupleRel,
						SubjectType:     ref.Type,
						SubjectRelation: ref.Relation,
					})
				}
			}
		}
	}

	return rules
}
