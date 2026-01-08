package schema

// UsersetRule represents a precomputed userset rule with relation closure applied.
// Userset rules handle permissions granted via group membership, expressed in OpenFGA
// as [group#member]. Unlike direct subject types where the tuple directly grants
// permission, usersets require checking if the subject has the specified relation
// on the group object.
//
// Each row means: a tuple with tuple_relation on object_type can satisfy relation
// when the tuple subject is subject_type#subject_relation.
//
// The rules are precomputed by expanding userset references through the relation
// closure data. This allows SQL to resolve userset permissions efficiently without
// nested subqueries for each implied relation.
//
// Example: For "viewer: [group#member]" where admin->member, the rules include:
//   - {relation: "viewer", tuple_relation: "viewer", subject_type: "group", subject_relation: "member", subject_relation_satisfying: "member"}
//   - {relation: "viewer", tuple_relation: "viewer", subject_type: "group", subject_relation: "member", subject_relation_satisfying: "admin"}
//
// This enables check_permission to match tuples where the subject has either member
// or admin on the group, without recursive relation resolution at query time.
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
//
// The expansion combines two sources of transitivity:
//  1. Object relation closure: viewer might be satisfied by editor (implied-by)
//  2. Subject relation closure: member might be satisfied by admin (implied-by)
//
// By precomputing the cross-product of these closures, SQL can match userset
// permissions with a simple JOIN instead of recursive CTEs for each check.
//
// This is analogous to ComputeRelationClosure but handles the subject-side
// relation traversal required for userset references.
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
