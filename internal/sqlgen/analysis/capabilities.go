// Package analysis provides relation analysis and strategy selection for SQL code generation.
package analysis

// GenerationCapabilities represents the unified generation eligibility for a relation.
// This consolidates the former separate CanGenerate (check) and CanGenerateListValue (list)
// flags into a single structure with clear semantics.
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

// DetermineListStrategy computes the appropriate list generation strategy
// based on the relation's analysis data. The priority order matches the
// former selectListObjectsTemplate/selectListSubjectsTemplate functions.
//
// Priority (highest to lowest):
// 1. DepthExceeded - relation exceeds userset depth limit
// 2. SelfRefUserset - has self-referential userset patterns
// 3. Composed - has indirect anchor (pure TTU reaching direct grants)
// 4. Intersection - has intersection patterns (AND groups)
// 5. Recursive - has TTU patterns or closure TTU patterns
// 6. Userset - has userset patterns or closure userset patterns
// 7. Direct - default (handles direct, implied, and exclusion patterns)
func DetermineListStrategy(a RelationAnalysis) ListStrategy {
	// Phase 9A: Depth exceeded takes highest priority
	// These immediately raise M2002 without any computation
	if a.ExceedsDepthLimit {
		return ListStrategyDepthExceeded
	}

	// Phase 9B: Self-referential userset patterns require recursive CTEs
	if a.HasSelfReferentialUserset {
		return ListStrategySelfRefUserset
	}

	// Phase 8: Indirect anchor patterns use composed template
	// These trace through TTU paths to reach an anchor with direct grants
	if a.IndirectAnchor != nil {
		return ListStrategyComposed
	}

	// Phase 6: Intersection patterns use INTERSECT queries
	// The intersection strategy handles all pattern combinations
	if a.Features.HasIntersection {
		return ListStrategyIntersection
	}

	// Phase 5: TTU patterns (direct or inherited) use recursive strategy
	// The recursive template handles direct, userset, exclusion, and TTU
	if a.Features.HasRecursive || len(a.ClosureParentRelations) > 0 {
		return ListStrategyRecursive
	}

	// Phase 4: Userset patterns (direct or inherited) use userset strategy
	// The userset template also handles exclusions if present
	if a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0 {
		return ListStrategyUserset
	}

	// Default: Direct strategy handles direct, implied, and exclusion patterns
	return ListStrategyDirect
}
