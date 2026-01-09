package schema

import "sort"

// ClosureRow represents a precomputed relation closure row.
// The closure table is a critical optimization that precomputes transitive
// implied-by relationships at schema load time, eliminating the need for
// recursive function calls during permission checks.
//
// Each row indicates that having satisfying_relation grants the relation
// on objects of object_type. For example, in a role hierarchy where
// owner -> admin -> member:
//   - {object_type: "repo", relation: "member", satisfying_relation: "owner"}
//   - {object_type: "repo", relation: "member", satisfying_relation: "admin"}
//   - {object_type: "repo", relation: "member", satisfying_relation: "member"}
//
// This allows check_permission to evaluate "does user have member?" with a
// simple JOIN rather than recursive traversal: just check if they have ANY
// of the satisfying relations.
type ClosureRow struct {
	ObjectType         string
	Relation           string
	SatisfyingRelation string
	ViaPath            []string // For debugging: path from relation to satisfying_relation
}

// ComputeRelationClosure computes the transitive closure for all relations.
// For each relation, it finds all relations that can satisfy it (directly or transitively).
//
// This is a build-time optimization. Without closure, check_permission would need
// recursive SQL functions to walk implied-by chains. With closure, a single JOIN
// against the inlined closure resolves the entire hierarchy.
//
// Example: For schema owner -> admin -> member:
//   - member is satisfied by: member, admin, owner
//   - admin is satisfied by: admin, owner
//   - owner is satisfied by: owner
//
// The closure table enables O(1) lookups instead of O(depth) recursion,
// which is critical for deeply nested role hierarchies.
func ComputeRelationClosure(types []TypeDefinition) []ClosureRow {
	// Estimate capacity: each relation typically has a few satisfying relations
	totalRelations := 0
	for _, t := range types {
		totalRelations += len(t.Relations)
	}
	rows := make([]ClosureRow, 0, totalRelations*3)

	for _, t := range types {
		// Build adjacency: relation -> relations that imply it
		// impliedBy[A] = [B, C] means B and C both imply A
		impliedBy := make(map[string][]string)
		for _, r := range t.Relations {
			impliedBy[r.Name] = append(impliedBy[r.Name], r.ImpliedBy...)
		}

		// For each relation, compute transitive closure via BFS
		for _, r := range t.Relations {
			satisfying := computeTransitiveSatisfiers(r.Name, impliedBy)

			// Sort map keys for deterministic output order.
			// This ensures consistent ordering of SatisfyingRelations in
			// downstream processing (complexity analysis, code generation).
			satisfyingRels := make([]string, 0, len(satisfying))
			for rel := range satisfying {
				satisfyingRels = append(satisfyingRels, rel)
			}
			sort.Strings(satisfyingRels)

			for _, rel := range satisfyingRels {
				rows = append(rows, ClosureRow{
					ObjectType:         t.Name,
					Relation:           r.Name,
					SatisfyingRelation: rel,
					ViaPath:            satisfying[rel],
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
			if _, seen := result[implied]; seen {
				continue
			}
			// Build path: current's path + implied
			currentPath := result[current]
			path := make([]string, len(currentPath), len(currentPath)+1)
			copy(path, currentPath)
			path = append(path, implied)
			result[implied] = path
			queue = append(queue, implied)
		}
	}

	return result
}
