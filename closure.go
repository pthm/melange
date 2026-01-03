package melange

// ClosureRow represents a row in the melange_relation_closure table.
// Each row indicates that having satisfying_relation grants the relation
// on objects of object_type.
type ClosureRow struct {
	ObjectType         string
	Relation           string
	SatisfyingRelation string
	ViaPath            []string // For debugging: path from relation to satisfying_relation
}

// ComputeRelationClosure computes the transitive closure for all relations.
// For each relation, it finds all relations that can satisfy it (directly or transitively).
//
// Example: For schema owner -> admin -> member:
//   - member is satisfied by: member, admin, owner
//   - admin is satisfied by: admin, owner
//   - owner is satisfied by: owner
//
// The closure enables efficient permission checks by replacing recursive
// implied-by traversal with a single JOIN against the closure table.
func ComputeRelationClosure(types []TypeDefinition) []ClosureRow {
	var rows []ClosureRow

	for _, t := range types {
		// Build adjacency: relation -> relations that imply it
		// impliedBy[A] = [B, C] means B and C both imply A
		impliedBy := make(map[string][]string)
		for _, r := range t.Relations {
			for _, ib := range r.ImpliedBy {
				impliedBy[r.Name] = append(impliedBy[r.Name], ib)
			}
		}

		// For each relation, compute transitive closure via BFS
		for _, r := range t.Relations {
			satisfying := computeTransitiveSatisfiers(r.Name, impliedBy)

			for rel, path := range satisfying {
				rows = append(rows, ClosureRow{
					ObjectType:         t.Name,
					Relation:           r.Name,
					SatisfyingRelation: rel,
					ViaPath:            path,
				})
			}
		}
	}

	return rows
}

// computeTransitiveSatisfiers computes all relations that transitively satisfy the start relation.
// Uses BFS to traverse the implied-by graph and collect all reachable relations.
//
// Returns a map of satisfying_relation -> path from start to that relation.
// The start relation always satisfies itself with path [start].
func computeTransitiveSatisfiers(start string, impliedBy map[string][]string) map[string][]string {
	// result maps satisfying_relation -> path from start
	result := map[string][]string{
		start: {start}, // relation always satisfies itself
	}

	queue := []string{start}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, implied := range impliedBy[current] {
			if _, seen := result[implied]; !seen {
				// Build path: current's path + implied
				path := make([]string, len(result[current]))
				copy(path, result[current])
				path = append(path, implied)
				result[implied] = path
				queue = append(queue, implied)
			}
		}
	}

	return result
}
