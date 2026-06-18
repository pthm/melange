package sqlgen

import (
	"fmt"
	"slices"
	"strings"
)

// formatSQLStringList formats a list of strings as a SQL-safe list.
// For example, ["user", "org"] becomes "'user', 'org'".
// Returns empty string if the list is empty.
func formatSQLStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("'%s'", item)
	}
	return strings.Join(quoted, ", ")
}

func buildTupleLookupRelations(a RelationAnalysis) []string {
	// Build relation list from self + simple closure relations.
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	return relations
}

// GeneratedSQL contains all SQL generated for a schema.
// This is applied atomically during migration to ensure consistent state.
type GeneratedSQL struct {
	// Functions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized check function (check_{type}_{relation}).
	Functions []string

	// NoWildcardFunctions contains CREATE OR REPLACE FUNCTION statements
	// for no-wildcard variants (check_{type}_{relation}_nw).
	// These skip wildcard matching for performance-critical paths.
	NoWildcardFunctions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes requests to specialized functions based on object type and relation.
	Dispatcher string

	// DispatcherNoWildcard contains the check_permission_nw dispatcher.
	DispatcherNoWildcard string

	// BulkDispatcher contains the check_permission_bulk function that evaluates
	// multiple permission checks in a single SQL call using UNION ALL branches.
	BulkDispatcher string

	// ExplainFunctions contains CREATE OR REPLACE FUNCTION statements for the
	// per-relation explain_{type}_{relation} functions. Each returns JSONB
	// shaped to melange.Trace and is the codegen companion to check_*.
	// Stage 1 (slice 1): only direct-grant matches and cycle detection are
	// implemented in the body; implied / userset / TTU / intersection paths
	// will be filled in by subsequent slices.
	ExplainFunctions []string

	// ExplainDispatcher contains the explain_permission public + internal
	// functions that route to per-relation explain_* by (object_type, relation).
	// Returns a structurally valid JSONB Trace even for unknown pairs so
	// callers can deserialise without special-casing.
	ExplainDispatcher string

	// IndexRecommendations lists composite indexes that make the generated
	// functions efficient against melange_tuples. Advisory only — users
	// translate the DDL to their source tables. See RecommendIndexes.
	IndexRecommendations []IndexRecommendation
}

// GenerateSQLOptions tunes codegen behavior for GenerateSQLWithOptions and
// GenerateListSQLWithOptions. The zero value matches the default melange behavior.
type GenerateSQLOptions struct {
	// EnableMaterializedCTEs forces "AS MATERIALIZED" on multi-referenced CTEs
	// inside generated list functions. The default (false) lets PostgreSQL
	// decide whether to inline or materialize each CTE — which on benchmarked
	// production-scale workloads (100K+ tuples) outperformed forced
	// materialization by ~10% on heavy list_objects queries.
	//
	// Set true when profiling shows a workload where forced materialization
	// wins (typically queries whose inner CTE is recomputed many times due to
	// inlining and produces non-trivial row counts).
	EnableMaterializedCTEs bool
}

// GenerateSQL generates specialized SQL functions for all relations in the schema
// using default options. See GenerateSQLWithOptions for tunable behavior.
//
// For each relation, it generates:
//   - A specialized check function that evaluates permission checks efficiently
//   - A no-wildcard variant for scenarios where wildcards are disallowed
//   - Dispatcher functions that route to the appropriate specialized function
//
// The inline parameter provides precomputed closure and userset data that is
// inlined into the generated functions as VALUES clauses, eliminating runtime
// table joins for this metadata.
//
// Returns an error if any function fails to generate, though this is rare
// as the analysis phase validates generation feasibility.
func GenerateSQL(analyses []RelationAnalysis, inline InlineSQLData, databaseSchema string) (GeneratedSQL, error) {
	return GenerateSQLWithOptions(analyses, inline, databaseSchema, GenerateSQLOptions{})
}

// GenerateSQLWithOptions is the option-aware variant of GenerateSQL.
//
// Currently the option set only affects list-function codegen (via
// GenerateListSQLWithOptions); check-function output is independent of the
// options today. The option is accepted here to keep a single public surface
// the migrator can configure once.
func GenerateSQLWithOptions(analyses []RelationAnalysis, inline InlineSQLData, databaseSchema string, _ GenerateSQLOptions) (GeneratedSQL, error) {
	var result GeneratedSQL

	complexityByRelation := buildClosureComplexityIndex(analyses)

	// Generate specialized function for each relation
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		fn, err := generateCheckFunction(a, inline, databaseSchema, false, complexityByRelation)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating check function: %w", err)
		}
		result.Functions = append(result.Functions, fn)
		noWildcardFn, err := generateCheckFunction(a, inline, databaseSchema, true, complexityByRelation)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating no-wildcard check function: %w", err)
		}
		result.NoWildcardFunctions = append(result.NoWildcardFunctions, noWildcardFn)
		// Stage 1 slice 1: only generate explain_* for relations whose check
		// resolves through a single direct/implied SELECT. Schemas with
		// usersets, TTU, exclusion, intersection, or complex implied chains
		// would produce a trace that disagrees with Check's answer until
		// later slices implement those branches; rather than ship an
		// incorrect trace, the dispatcher routes unsupported relations to
		// the unsupported-in-this-version sentinel.
		if !explainSupported(a) {
			continue
		}
		explainFn, err := generateExplainFunction(a, inline, databaseSchema, complexityByRelation)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating explain function: %w", err)
		}
		result.ExplainFunctions = append(result.ExplainFunctions, explainFn)
	}

	// Generate dispatchers
	var err error
	result.Dispatcher, err = generateDispatcher(analyses, databaseSchema, false)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating dispatcher: %w", err)
	}
	result.DispatcherNoWildcard, err = generateDispatcher(analyses, databaseSchema, true)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating no-wildcard dispatcher: %w", err)
	}
	result.ExplainDispatcher, err = generateExplainDispatcher(analyses, databaseSchema)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating explain dispatcher: %w", err)
	}

	// Generate bulk dispatcher
	result.BulkDispatcher = generateBulkDispatcher(analyses, databaseSchema)

	// Index recommendations are advisory and derived from the same analyses;
	// emitting them here keeps the per-schema output self-contained.
	result.IndexRecommendations = RecommendIndexes(analyses)

	return result, nil
}

// functionName returns the name for a specialized check function.
func functionName(objectType, relation string) string {
	return SafeIdentifier("check_", objectType, relation, "")
}

func functionNameNoWildcard(objectType, relation string) string {
	return SafeIdentifier("check_", objectType, relation, "_nw")
}

// computeHasStandaloneAccess determines if the relation has access paths outside of intersections.
func computeHasStandaloneAccess(a RelationAnalysis) bool {
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Implied and recursive are always standalone, regardless of intersection.
	if a.Features.HasImplied || a.Features.HasRecursive {
		return true
	}

	// Check if any intersection group has a "This" part, meaning direct/userset access
	// is constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := slices.ContainsFunc(a.IntersectionGroups, func(g IntersectionGroupInfo) bool {
		return slices.ContainsFunc(g.Parts, func(p IntersectionPart) bool {
			return p.IsThis
		})
	})

	// Direct and userset are standalone only if not inside an intersection.
	return (a.Features.HasDirect || a.Features.HasUserset) && !hasIntersectionWithThis
}

// DispatcherData contains data for rendering the dispatcher template.
type DispatcherData struct {
	FunctionName            string
	HasSpecializedFunctions bool
	Cases                   []DispatcherCase
}

// DispatcherCase represents a single CASE WHEN branch in the dispatcher.
// Each case routes a specific (object_type, relation) pair to its specialized function.
type DispatcherCase struct {
	DatabaseSchema      string
	ObjectType          string
	Relation            string
	CheckFunctionName   string
	Inlineable          bool     // true if simple direct-assignment only (bulk dispatcher can inline EXISTS)
	DirectSubjectTypes  []string // subject types allowed for direct tuples (used in inline)
	SatisfyingRelations []string // relations in closure that satisfy this one (used in inline userset check)
}

// NamedFunction pairs a specialized function name with its generated SQL body.
// Dispatcher functions are excluded from this set; see CollectNamedFunctions.
// The SQL field is used verbatim for checksum computation and for emitting
// changed-only migrations.
type NamedFunction struct {
	Name string
	SQL  string
}

// CollectNamedFunctions returns all specialized functions paired with their SQL.
// Dispatchers are excluded — they always change when any relation changes and
// should be unconditionally included in migrations.
//
// The analyses slice must be the same slice, in the same order, passed to
// GenerateSQL and GenerateListSQL that produced generatedSQL and listSQL.
// The function walks all three in lockstep; mismatched ordering will silently
// produce incorrect name-to-SQL pairings.
func CollectNamedFunctions(
	generatedSQL GeneratedSQL,
	listSQL ListGeneratedSQL,
	analyses []RelationAnalysis,
) []NamedFunction {
	var result []NamedFunction
	checkIdx, noWildcardIdx, explainIdx := 0, 0, 0
	listObjIdx, listSubjIdx := 0, 0

	for _, a := range analyses {
		if a.Capabilities.CheckAllowed {
			result = append(result, NamedFunction{
				Name: functionName(a.ObjectType, a.Relation),
				SQL:  generatedSQL.Functions[checkIdx],
			})
			checkIdx++
			result = append(result, NamedFunction{
				Name: functionNameNoWildcard(a.ObjectType, a.Relation),
				SQL:  generatedSQL.NoWildcardFunctions[noWildcardIdx],
			})
			noWildcardIdx++
			if explainSupported(a) {
				result = append(result, NamedFunction{
					Name: explainFunctionName(a.ObjectType, a.Relation),
					SQL:  generatedSQL.ExplainFunctions[explainIdx],
				})
				explainIdx++
			}
		}
		if a.Capabilities.ListAllowed {
			result = append(result, NamedFunction{
				Name: listObjectsFunctionName(a.ObjectType, a.Relation),
				SQL:  listSQL.ListObjectsFunctions[listObjIdx],
			})
			listObjIdx++
			result = append(result, NamedFunction{
				Name: listSubjectsFunctionName(a.ObjectType, a.Relation),
				SQL:  listSQL.ListSubjectsFunctions[listSubjIdx],
			})
			listSubjIdx++
		}
	}

	return result
}

// CollectFunctionNames returns all function names that will be generated for the given analyses.
// This is used for migration tracking and orphan detection to identify stale functions
// that need to be dropped when the schema changes.
//
// The returned list includes:
//   - Specialized check functions: check_{type}_{relation}
//   - No-wildcard check variants: check_{type}_{relation}_nw
//   - Specialized list functions: list_{type}_{relation}_obj, list_{type}_{relation}_sub
//   - Dispatcher functions (always included): check_permission, list_accessible_objects, etc.
func CollectFunctionNames(analyses []RelationAnalysis) []string {
	var names []string

	for _, a := range analyses {
		if a.Capabilities.CheckAllowed {
			names = append(names,
				functionName(a.ObjectType, a.Relation),
				functionNameNoWildcard(a.ObjectType, a.Relation),
			)
			if explainSupported(a) {
				names = append(names, explainFunctionName(a.ObjectType, a.Relation))
			}
		}
		if a.Capabilities.ListAllowed {
			names = append(names,
				listObjectsFunctionName(a.ObjectType, a.Relation),
				listSubjectsFunctionName(a.ObjectType, a.Relation),
			)
		}
	}

	// Dispatchers are always generated
	names = append(names,
		"check_permission",
		"check_permission_internal",
		"check_permission_nw",
		"check_permission_nw_internal",
		"check_permission_bulk",
		"explain_permission",
		"explain_permission_internal",
		"list_accessible_objects",
		"list_accessible_subjects",
	)

	return names
}

// buildClosureComplexityIndex returns, for each object type, a map from relation
// name to a cost score derived from the full RelationAnalysis. The score is
// then propagated along ComplexClosureRelations so that an implied wrapper
// inherits the cost of any recursive callee it delegates to. Used to order
// implied and parent function calls cheap-first in generated check functions.
func buildClosureComplexityIndex(analyses []RelationAnalysis) map[string]map[string]int {
	byType := make(map[string]map[string]int)
	for _, a := range analyses {
		m, ok := byType[a.ObjectType]
		if !ok {
			m = make(map[string]int)
			byType[a.ObjectType] = m
		}
		m[a.Relation] = relationComplexityScore(a)
	}

	// Propagate scores along ComplexClosureRelations until fixed point. Each
	// pass lifts a relation's score to the max of any complex callee on the
	// same object type, so wrappers like `a: b` inherit b's cost class.
	for {
		changed := false
		for _, a := range analyses {
			cur := byType[a.ObjectType][a.Relation]
			for _, rel := range a.ComplexClosureRelations {
				if callee, ok := byType[a.ObjectType][rel]; ok && callee > cur {
					cur = callee
				}
			}
			if cur > byType[a.ObjectType][a.Relation] {
				byType[a.ObjectType][a.Relation] = cur
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return byType
}

// Complexity tiers used by relationComplexityScore. The recursive tier marks
// relations whose generated body will invoke check_permission_internal and
// therefore sit behind the dispatcher's depth-limit raise. checkFunctionCost
// uses the same threshold to decide when to emit a COST hint.
const (
	complexityDirect       = 0
	complexityImplied      = 1
	complexityUserset      = 2 // also covers simple-only exclusion
	complexityIntersection = 3
	complexityRecursive    = 5
)

// relationComplexityScore returns a coarse cost class for a relation. Higher
// scores indicate more expensive — and potentially recursive — evaluation.
// Keeping the recursive tier above intersection/userset preserves the cheap
// resolution path at the front of OR/AND chains so deep schemas don't trade
// a successful deny for an M2002.
func relationComplexityScore(a RelationAnalysis) int {
	if invokesInternalCheck(a) {
		return complexityRecursive
	}
	f := a.Features
	switch {
	case f.HasIntersection:
		return complexityIntersection
	case f.HasUserset, f.HasExclusion:
		return complexityUserset
	case f.HasImplied:
		return complexityImplied
	default:
		return complexityDirect
	}
}

// invokesInternalCheck reports whether the relation's generated body will emit
// at least one check_permission_internal call. Such bodies sit behind the
// dispatcher's depth-limit check, so callers should treat them as expensive
// regardless of the relation's top-level feature flags.
func invokesInternalCheck(a RelationAnalysis) bool {
	f := a.Features
	if f.HasRecursive || a.HasComplexUsersetPatterns {
		return true
	}
	if len(a.ComplexExcludedRelations) > 0 ||
		len(a.ExcludedParentRelations) > 0 ||
		len(a.ExcludedIntersectionGroups) > 0 {
		return true
	}
	for _, g := range a.IntersectionGroups {
		for _, p := range g.Parts {
			if p.ParentRelation != nil {
				return true
			}
			if !p.IsThis && !p.IsSimple {
				return true
			}
			if p.ExcludedRelation != "" && !p.IsExcludedSimple {
				return true
			}
		}
	}
	return false
}
