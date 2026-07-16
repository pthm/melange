// Package analysis provides relation analysis and strategy selection for SQL code generation.
package analysis

import "slices"

// GenerationCapabilities represents the unified generation eligibility for a relation.
// CheckAllowed gates specialized check SQL; ListAllowed gates specialized list SQL.
// When false, the respective dispatcher falls back to the generic implementation.
type GenerationCapabilities struct {
	// CheckAllowed is true if specialized check SQL can be generated.
	// When false, the check dispatcher falls back to check_permission_generic_internal.
	CheckAllowed bool

	// ListAllowed is true if specialized list SQL can be generated.
	// When false, the list dispatcher falls back to list_accessible_*_generic functions.
	ListAllowed bool

	// CheckReason explains why CheckAllowed is false (empty if allowed).
	// Used for debugging and diagnostic output.
	CheckReason string

	// ListReason explains why ListAllowed is false (empty if allowed).
	// Used for debugging and diagnostic output.
	ListReason string
}

// ListStrategy determines which list generation approach to use.
// Each strategy corresponds to a different code generation path optimized
// for specific authorization patterns.
type ListStrategy int

const (
	// ListStrategyDirect handles direct/implied tuple lookups with optional exclusions.
	// Used for relations like "viewer: [user]" or "viewer: [user] but not blocked".
	ListStrategyDirect ListStrategy = iota

	// ListStrategyUserset handles [group#member] pattern expansion via JOINs.
	// Used for relations like "viewer: [group#member]".
	ListStrategyUserset

	// ListStrategyRecursive handles TTU patterns with recursive CTEs or cross-type calls.
	// Used for relations like "viewer: viewer from parent".
	ListStrategyRecursive

	// ListStrategyIntersection handles AND patterns with INTERSECT queries.
	// Used for relations like "viewer: writer and editor".
	ListStrategyIntersection

	// ListStrategyDepthExceeded generates a function that immediately raises M2002.
	// Used for relations with userset chains >= 25 levels deep.
	ListStrategyDepthExceeded

	// ListStrategySelfRefUserset handles self-referential userset patterns.
	// Used for relations like "member: [group#member]" where the userset
	// references the same type and relation, requiring recursive CTEs.
	ListStrategySelfRefUserset

	// ListStrategyComposed handles indirect anchor patterns via function composition.
	// Used for pure TTU patterns that trace through to an anchor with direct grants.
	ListStrategyComposed
)

// String returns the strategy name for debugging and diagnostic output.
func (s ListStrategy) String() string {
	switch s {
	case ListStrategyDirect:
		return "Direct"
	case ListStrategyUserset:
		return "Userset"
	case ListStrategyRecursive:
		return "Recursive"
	case ListStrategyIntersection:
		return "Intersection"
	case ListStrategyDepthExceeded:
		return "DepthExceeded"
	case ListStrategySelfRefUserset:
		return "SelfRefUserset"
	case ListStrategyComposed:
		return "Composed"
	default:
		return "Unknown"
	}
}

// hasSelfReferentialParent reports whether the relation recurses on itself
// through a TTU parent arm — i.e. an arm "R from L" where L links through the
// relation's own object type AND R is the relation being defined (e.g.
// "local_admin: admin from org or local_admin from parent"). Only that shape
// chains across the parent edge and needs the Recursive strategy's CTE.
//
// An arm like "can_read: viewer from parent" (parent: [document, folder]) links
// through the object type too, but its target relation is viewer, not can_read,
// so it is a single hop — the Composed strategy handles it and the Recursive
// CTE would wrongly chain can_read through the parent. Hence the R == a.Relation
// guard, not a bare linking-type check.
func hasSelfReferentialParent(a RelationAnalysis) bool {
	isSelfRecursive := func(p ParentRelationInfo) bool {
		return p.Relation == a.Relation && slices.Contains(p.AllowedLinkingTypes, a.ObjectType)
	}
	return slices.ContainsFunc(a.ParentRelations, isSelfRecursive) ||
		slices.ContainsFunc(a.ClosureParentRelations, isSelfRecursive)
}

// DetermineListStrategy computes the appropriate list generation strategy
// for a relation based on its analysis data.
//
// Priority (highest to lowest):
//  1. DepthExceeded - relation exceeds userset depth limit
//  2. SelfRefUserset - has self-referential userset patterns
//  3. Composed - has indirect anchor (pure TTU reaching direct grants), but only
//     when there is no self-referential recursive parent — see below.
//  4. Intersection - has intersection patterns (AND groups)
//  5. Recursive - has TTU patterns or closure TTU patterns
//  6. Userset - has userset patterns or closure userset patterns
//  7. Direct - default (handles direct, implied, and exclusion patterns)
func DetermineListStrategy(a RelationAnalysis) ListStrategy {
	if a.ExceedsDepthLimit {
		return ListStrategyDepthExceeded
	}
	if a.HasSelfReferentialUserset {
		return ListStrategySelfRefUserset
	}
	// A relation like "local_admin: admin from org or local_admin from parent"
	// is pure TTU, so an indirect anchor gets computed for the cross-type arm
	// (admin from org). But the Composed strategy models a single anchor path
	// and cannot represent the self-referential "local_admin from parent" walk,
	// so it under-reports every object reached only through the parent chain.
	// Route these to Recursive, whose CTE covers both the cross-type anchor base
	// and the self-referential parent expansion.
	if a.IndirectAnchor != nil && !hasSelfReferentialParent(a) {
		return ListStrategyComposed
	}
	if a.Features.HasIntersection {
		return ListStrategyIntersection
	}
	if a.Features.HasRecursive || len(a.ClosureParentRelations) > 0 {
		return ListStrategyRecursive
	}
	if a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0 {
		return ListStrategyUserset
	}
	return ListStrategyDirect
}
