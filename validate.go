package melange

import (
	"fmt"
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

// DetectCycles checks for cycles in the relation graph.
// It validates both implied-by cycles (within a single type) and parent relation
// cycles (across types). Returns an error describing the cycle if one is found.
//
// This function is called by both GenerateGo and MigrateWithTypes to catch
// invalid schemas before they cause runtime issues.
//
// Example cyclic schemas that would be detected:
//
//	// Implied-by cycle:
//	type resource
//	  relations
//	    define admin: [user] or owner
//	    define owner: [user] or admin  // CYCLE: admin ↔ owner
//
//	// Parent relation cycle:
//	type organization
//	  relations
//	    define repo: [repository]
//	    define can_read: can_read from repo
//	type repository
//	  relations
//	    define org: [organization]
//	    define can_read: can_read from org  // CYCLE: crosses types
func DetectCycles(types []TypeDefinition) error {
	// Check implied-by cycles (per-type, simpler graph)
	if err := detectImpliedByCycles(types); err != nil {
		return err
	}

	// Check parent relation cycles (cross-type graph)
	if err := detectParentCycles(types); err != nil {
		return err
	}

	return nil
}

// detectImpliedByCycles checks for cycles in implied-by relations within each type.
// For example: admin implies owner, owner implies admin would be a cycle.
func detectImpliedByCycles(types []TypeDefinition) error {
	for _, t := range types {
		graph := make(map[relationNode][]relationNode)

		// Build graph for this type
		// An edge from A to B means "A is implied by B" (B → A)
		for _, r := range t.Relations {
			n := relationNode{objectType: t.Name, relation: r.Name}
			for _, impliedBy := range r.ImpliedBy {
				target := relationNode{objectType: t.Name, relation: impliedBy}
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

// detectParentCycles checks for cycles in parent relations across types.
// For example: repository.can_read inherits from org.can_read, and
// org.can_read inherits from repository.can_read would be a cycle.
//
// IMPORTANT: Same-type recursive patterns like "viewer from parent" where
// parent: [folder] are NOT cycles - they represent hierarchical inheritance
// (e.g., folders inheriting permissions from parent folders). These are valid
// patterns supported by OpenFGA/Zanzibar and handled at runtime via visited
// tracking in the SQL functions.
//
// We only flag true cross-type cycles where different object types form a loop.
func detectParentCycles(types []TypeDefinition) error {
	graph := buildParentGraph(types)
	if cycle := detectCycleInGraph(graph); cycle != nil {
		// Check if this is a same-type self-reference (hierarchical recursion)
		// These are NOT cycles - they're valid recursive patterns like "viewer from parent"
		if len(cycle) == 2 && cycle[0] == cycle[1] {
			// Self-edge: folder.viewer → folder.viewer - this is valid hierarchical recursion
			return nil
		}
		// Check if all nodes in the cycle are the same type - also valid recursion
		allSameType := true
		for i := 1; i < len(cycle); i++ {
			if cycle[i].objectType != cycle[0].objectType {
				allSameType = false
				break
			}
		}
		if allSameType {
			return nil
		}
		return fmt.Errorf("%w: parent relation cycle: %s",
			ErrCyclicSchema, formatCycle(cycle))
	}
	return nil
}

// buildParentGraph builds the cross-type parent relation graph.
// Nodes are (objectType, relation) pairs.
// Edges point from child relation to the parent relation it inherits from.
func buildParentGraph(types []TypeDefinition) map[relationNode][]relationNode {
	graph := make(map[relationNode][]relationNode)

	// Build a lookup for resolving linking relations to parent types
	// linkingRelTypes[objectType][linkingRel] = parentType
	linkingRelTypes := make(map[string]map[string]string)
	for _, t := range types {
		linkingRelTypes[t.Name] = make(map[string]string)
		for _, r := range t.Relations {
			// If this relation has direct subject types, record the first one
			// This is the type that the linking relation points to
			if len(r.SubjectTypes) > 0 {
				// Strip wildcard suffix if present (e.g., "user:*" → "user")
				st := r.SubjectTypes[0]
				if len(st) > 2 && st[len(st)-2:] == ":*" {
					st = st[:len(st)-2]
				}
				linkingRelTypes[t.Name][r.Name] = st
			}
		}
	}

	// Build the graph
	for _, t := range types {
		for _, r := range t.Relations {
			if r.ParentRelation == "" {
				continue
			}

			n := relationNode{objectType: t.Name, relation: r.Name}

			// ParentType is the linking relation name (e.g., "org")
			// We need to find what type that relation points to
			linkingRel := r.ParentType
			if parentType, ok := linkingRelTypes[t.Name][linkingRel]; ok {
				target := relationNode{objectType: parentType, relation: r.ParentRelation}
				graph[n] = append(graph[n], target)
			}
		}
	}

	return graph
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
