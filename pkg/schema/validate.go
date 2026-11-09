package schema

import (
	"fmt"
	"slices"
	"strings"
)

// color represents the state of a node during DFS cycle detection.
type color int

const (
	white color = iota // unvisited
	gray               // in current DFS path (cycle if revisited)
	black              // fully processed
)

// relationNode represents a (objectType, relation) pair in the relation graph.
type relationNode struct {
	objectType string
	relation   string
}

// DetectCycles checks for implied-by cycles: relations within a type that
// resolve through each other on the same object, as computed relations
// (admin implies owner, owner implies admin) or intersection and exclusion
// operands, which OpenFGA rejects too. Returns an error describing the cycle
// if one is found.
//
// Parent relation cycles (viewer from parent over a folder hierarchy, or
// folder.a -> project.b -> folder.a across types) are valid: data can form
// loops through them, and every generated function that follows them detects
// revisits or walks breadth first.
//
// This function is called by both GenerateGo and MigrateWithTypes to catch
// invalid schemas before they cause runtime issues.
//
// Example cyclic schema that would be detected:
//
//	type resource
//	  relations
//	    define admin: [user] or owner
//	    define owner: [user] or admin  // CYCLE: admin ↔ owner
func DetectCycles(types []TypeDefinition) error {
	// Check implied-by cycles (per-type, simpler graph)
	if err := detectImpliedByCycles(types); err != nil {
		return err
	}

	return nil
}

// detectImpliedByCycles checks for cycles among the relations of each type that
// resolve on the same object: implied-by relations (admin implies owner, owner
// implies admin), intersection operands and exclusion operands
// ("a: [user] or (b and c)" with "b: [user] or a"). OpenFGA rejects all of
// them, and check functions count only parent and userset hops as steps, so
// such a cycle would recurse without bound.
func detectImpliedByCycles(types []TypeDefinition) error {
	for _, t := range types {
		graph := make(map[relationNode][]relationNode)

		// Build graph for this type
		// An edge from A to B means A is resolved through B on the same object
		for _, r := range t.Relations {
			n := relationNode{objectType: t.Name, relation: r.Name}
			for _, rel := range sameObjectOperands(r) {
				target := relationNode{objectType: t.Name, relation: rel}
				graph[n] = append(graph[n], target)
			}
		}

		// Check for cycles
		if cycle := detectCycleInGraph(graph); cycle != nil {
			return fmt.Errorf("%w: implied-by cycle in type %q: %s",
				ErrCyclicSchema, t.Name, formatCycle(cycle))
		}
	}
	return nil
}

// sameObjectOperands returns the relations r resolves through on the same
// object: implied-by relations, intersection operands and their exclusions,
// and excluded relations and intersections. An intersection names r itself for
// its direct assignment ("[user] and c"), which is not a cycle; excluded
// intersections cannot hold a direct assignment, so r there is one.
func sameObjectOperands(r RelationDefinition) []string {
	rels := slices.Concat(r.ImpliedBy, r.ExcludedRelations)
	for _, group := range r.IntersectionGroups {
		for _, rel := range group.Relations {
			if rel != r.Name {
				rels = append(rels, rel)
			}
		}
	}
	for _, group := range r.ExcludedIntersectionGroups {
		rels = append(rels, group.Relations...)
	}
	for _, group := range slices.Concat(r.IntersectionGroups, r.ExcludedIntersectionGroups) {
		for _, excluded := range group.Exclusions {
			rels = append(rels, excluded...)
		}
	}
	return rels
}

// detectCycleInGraph uses DFS with three-color marking to detect cycles.
// Returns the cycle path if found, nil otherwise.
func detectCycleInGraph(graph map[relationNode][]relationNode) []relationNode {
	colors := make(map[relationNode]color)
	parent := make(map[relationNode]relationNode)

	var dfs func(n relationNode) []relationNode
	dfs = func(n relationNode) []relationNode {
		colors[n] = gray

		for _, neighbor := range graph[n] {
			switch colors[neighbor] {
			case gray:
				// Found cycle - reconstruct path
				return reconstructCycle(n, neighbor, parent)
			case white:
				parent[neighbor] = n
				if cycle := dfs(neighbor); cycle != nil {
					return cycle
				}
			}
		}

		colors[n] = black
		return nil
	}

	for n := range graph {
		if colors[n] == white {
			if cycle := dfs(n); cycle != nil {
				return cycle
			}
		}
	}

	return nil
}

// reconstructCycle builds the cycle path from parent pointers.
// from is the node where we detected the back-edge, to is the node we're returning to.
func reconstructCycle(from, to relationNode, parent map[relationNode]relationNode) []relationNode {
	cycle := []relationNode{to}
	for n := from; n != to; n = parent[n] {
		cycle = append([]relationNode{n}, cycle...)
	}
	cycle = append([]relationNode{to}, cycle...)
	return cycle
}

// formatCycle converts a cycle path to a human-readable string.
// Example: "resource.admin → resource.owner → resource.admin"
func formatCycle(cycle []relationNode) string {
	parts := make([]string, len(cycle))
	for i, n := range cycle {
		parts[i] = fmt.Sprintf("%s.%s", n.objectType, n.relation)
	}
	return strings.Join(parts, " → ")
}
