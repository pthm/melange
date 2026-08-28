package sqlgen

import "sort"

// ListPlan contains all computed data needed to generate a list function.
type ListPlan struct {
	// Input data
	Analysis       RelationAnalysis
	Inline         InlineSQLData
	DatabaseSchema string

	// Function identity
	FunctionName string
	ObjectType   string
	Relation     string

	// Computed relation lists
	RelationList           []string // Relations for tuple lookup (self + simple closure)
	AllSatisfyingRelations []string // All relations that satisfy this one (for list_subjects)
	AllowedSubjectTypes    []string // Subject types allowed for this relation
	ComplexClosure         []string // Complex closure relations (excluding intersection)

	// Feature configuration
	AllowWildcard bool            // Whether the relation can surface '*' (direct or TTU-reachable)
	Exclusions    ExclusionConfig // Exclusion rules configuration

	// Feature flags (derived from analysis)
	HasUserset      bool
	HasExclusion    bool
	HasIntersection bool
	HasRecursive    bool

	// Eligibility and strategy from unified analysis
	Capabilities GenerationCapabilities
	Strategy     ListStrategy

	// Additional flags for routing
	HasUsersetSubject   bool // Has userset subject matching capability
	HasUsersetPatterns  bool // Has userset patterns to expand
	HasComplexUsersets  bool // Has userset patterns requiring check_permission calls
	HasStandaloneAccess bool // Has standalone access paths (not constrained by intersection)

	// Optimization flags
	UseCTEExclusion bool // Use CTE-based exclusion optimization (precompute + anti-join)

	// FilterableRelations lists this object type's directly-assignable
	// relations, sorted. These are exactly the relations that can carry a
	// plain-subject tuple, so they are exactly the ones an object filter can
	// match. The generated guard rejects anything else, turning a filter on a
	// computed or misspelled relation into an error instead of an empty list.
	//
	// Empty when the plan was built without an analysis lookup (hand-built
	// plans in tests), in which case the guard omits the relation check rather
	// than rejecting every filter.
	FilterableRelations []string

	// Analysis lookup for checking parent relation complexity (TTU optimization)
	// Maps "objectType.relation" -> *RelationAnalysis
	// Used by TTU block generation to determine if parent relations are simple or complex
	AnalysisLookup map[string]*RelationAnalysis

	// EnableMaterializedCTEs, when true, emits AS MATERIALIZED on
	// multi-referenced CTEs in generated list functions. Default false: PG
	// chooses inlining vs materialization on its own, which on benchmarked
	// production-scale workloads outperformed forced materialization. Wired
	// from GenerateSQLOptions for callers that profile a workload where
	// forced materialization helps.
	EnableMaterializedCTEs bool
}

// MaterializeCTEs reports whether multi-referenced CTEs in generated list
// functions should render with "AS MATERIALIZED". Default is false (let PG
// decide); set GenerateSQLOptions.EnableMaterializedCTEs to opt in.
func (p ListPlan) MaterializeCTEs() bool {
	return p.EnableMaterializedCTEs
}

// wrapPaginationFiltered applies plan-aware materialization to the list_objects
// cursor pagination wrapper, ANDing the object filter into the paged CTE unless
// pushdown already constrained every arm of the wrapped query.
//
// The post-filter is the correctness floor: a strategy whose arms cannot take
// the predicate inline still honors p_filter, it just pays for the unfiltered
// enumeration first.
func (p ListPlan) wrapPaginationFiltered(query string, pushedDown bool) string {
	var qual Expr
	if !pushedDown {
		qual = objectFilterPredicate(p, Col{Table: "br", Column: "object_id"})
	}
	return wrapWithPaginationFilteredOpts(query, "object_id", p.MaterializeCTEs(), qual)
}

// wrapPaginationWildcardFirst applies plan-aware materialization to the
// wildcard-first pagination wrapper used by list_subjects.
func (p ListPlan) wrapPaginationWildcardFirst(query string) string {
	return wrapWithPaginationWildcardFirstOpts(query, p.MaterializeCTEs())
}

// wrapExclusionCTEAndPagination applies plan-aware materialization to the
// exclusion+pagination wrapper used when CTE-based exclusion is enabled.
func (p ListPlan) wrapExclusionCTEAndPagination(query, exclusionCTE string) string {
	return wrapWithExclusionCTEAndPaginationOpts(query, exclusionCTE, p.MaterializeCTEs())
}

// BuildListObjectsPlanWithLookup creates a plan with analysis lookup for TTU optimization.
func BuildListObjectsPlanWithLookup(a RelationAnalysis, inline InlineSQLData, databaseSchema string, lookup map[string]*RelationAnalysis) ListPlan {
	plan := buildBasePlan(a, inline, databaseSchema, listObjectsFunctionName(a.ObjectType, a.Relation), lookup)
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
			databaseSchema,
			Col{Table: "t", Column: "object_id"},
			SubjectType,
			SubjectID,
		)
		// list_objects context: object_id is the per-row candidate column and
		// the subject exprs are query-constant params, so complex exclusions
		// may use a set-oriented anti-join against the excluded relation's
		// list_objects function instead of a per-candidate check.
		plan.Exclusions.Compose = &exclusionCompose{
			Lookup:   lookup,
			FromType: a.ObjectType,
			FromRel:  a.Relation,
		}
	}
	return plan
}

// BuildListSubjectsPlanWithLookup creates a plan with analysis lookup for TTU optimization.
func BuildListSubjectsPlanWithLookup(a RelationAnalysis, inline InlineSQLData, databaseSchema string, lookup map[string]*RelationAnalysis) ListPlan {
	plan := buildBasePlan(a, inline, databaseSchema, listSubjectsFunctionName(a.ObjectType, a.Relation), lookup)
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
			databaseSchema,
			ObjectID,
			Col{Table: "t", Column: "subject_type"},
			Col{Table: "t", Column: "subject_id"},
		)
		// Enable CTE optimization when eligible and relation has userset patterns
		// (JOIN expansion benefits from precomputed exclusions)
		plan.UseCTEExclusion = plan.Exclusions.CanUseCTEOptimization() && a.Features.HasUserset
	}
	return plan
}

func buildBasePlan(a RelationAnalysis, inline InlineSQLData, databaseSchema, functionName string, lookup map[string]*RelationAnalysis) ListPlan {
	return ListPlan{
		Analysis:       a,
		Inline:         inline,
		DatabaseSchema: databaseSchema,
		FunctionName:   functionName,
		ObjectType:     a.ObjectType,
		Relation:       a.Relation,

		RelationList:           buildTupleLookupRelations(a),
		AllSatisfyingRelations: buildAllSatisfyingRelationsList(a),
		AllowedSubjectTypes:    buildAllowedSubjectTypesList(a),
		ComplexClosure:         filterComplexClosureRelations(a),

		// A relation surfaces '*' either directly (Features.HasWildcard) or
		// through a TTU parent / closure whose target relation is wildcard-
		// granted — the analyzer does not propagate HasWildcard across those
		// edges, so walk the reference graph. Without this, list_*_sub for a
		// relation like folder.viewer ("... or viewer from org", org.viewer=
		// [user:*]) drops the wildcard granted on the parent (ExcludeWildcard
		// filters '*' from the parent-closure scan and the tail never expands).
		AllowWildcard: a.Features.HasWildcard || reachesWildcard(lookup, a.ObjectType, a.Relation),

		HasUserset:      a.Features.HasUserset,
		HasExclusion:    a.Features.HasExclusion,
		HasIntersection: a.Features.HasIntersection,
		HasRecursive:    a.Features.HasRecursive,

		Capabilities: a.Capabilities,
		Strategy:     a.ListStrategy,

		HasUsersetSubject:   a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0,
		HasUsersetPatterns:  len(buildListUsersetPatternInputs(a)) > 0,
		HasComplexUsersets:  a.HasComplexUsersetPatterns,
		HasStandaloneAccess: computeHasStandaloneAccess(a),

		FilterableRelations: directlyAssignableRelations(lookup, a.ObjectType),

		AnalysisLookup: lookup,
	}
}

// directlyAssignableRelations returns the relations on objectType that accept
// direct subject assignment ([user], [workspace]), sorted for deterministic
// codegen.
//
// Features.HasDirect is the right test: it is set from DirectSubjectTypes,
// which excludes userset restrictions, so the result is the set of relations
// that can appear in melange_tuples paired with a plain subject — which is what
// an object filter matches on.
func directlyAssignableRelations(lookup map[string]*RelationAnalysis, objectType string) []string {
	var rels []string
	for _, a := range lookup {
		if a.ObjectType == objectType && a.Features.HasDirect {
			rels = append(rels, a.Relation)
		}
	}
	sort.Strings(rels)
	return rels
}

func (p ListPlan) ExcludeWildcard() bool {
	return !p.AllowWildcard
}

func (p ListPlan) FeaturesString() string {
	return p.Analysis.Features.String()
}
