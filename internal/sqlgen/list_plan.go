package sqlgen

// =============================================================================
// List Plan Layer
// =============================================================================
//
// This file implements the Plan layer for list function generation.
// The Plan layer computes flags and normalized inputs from RelationAnalysis
// and InlineSQLData, using unified analysis outputs (Capabilities + ListStrategy).
//
// Architecture: Plan → Blocks → Render
// - Plan: compute flags and normalized inputs (this file)
// - Blocks: build QueryBlock/SelectStmt values using DSL only
// - Render: produce SQL/PLpgSQL strings

// ListPlan contains all computed data needed to generate a list function.
// This separates plan computation from block building and rendering.
type ListPlan struct {
	// Input data
	Analysis RelationAnalysis
	Inline   InlineSQLData

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
	AllowWildcard bool            // Whether wildcards are allowed (list_objects)
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
	HasUsersetSubject  bool // Has userset subject matching capability
	HasUsersetPatterns bool // Has userset patterns to expand
	HasComplexUsersets bool // Has userset patterns requiring check_permission calls
}

// BuildListObjectsPlan creates a plan for generating a list_objects function.
// This extracts plan computation from the former ListObjectsBuilder constructor.
func BuildListObjectsPlan(a RelationAnalysis, inline InlineSQLData) ListPlan {
	plan := ListPlan{
		Analysis:     a,
		Inline:       inline,
		FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		ObjectType:   a.ObjectType,
		Relation:     a.Relation,

		// Computed lists
		RelationList:           buildTupleLookupRelations(a),
		AllSatisfyingRelations: buildAllSatisfyingRelationsList(a),
		AllowedSubjectTypes:    buildAllowedSubjectTypesList(a),
		ComplexClosure:         filterComplexClosureRelations(a),

		// Feature configuration
		AllowWildcard: a.Features.HasWildcard,

		// Feature flags
		HasUserset:      a.Features.HasUserset,
		HasExclusion:    a.Features.HasExclusion,
		HasIntersection: a.Features.HasIntersection,
		HasRecursive:    a.Features.HasRecursive,

		// From unified analysis
		Capabilities: a.Capabilities,
		Strategy:     a.ListStrategy,

		// Routing flags
		HasUsersetSubject:  a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0,
		HasUsersetPatterns: len(buildListUsersetPatternInputs(a)) > 0,
		HasComplexUsersets: a.HasComplexUsersetPatterns,
	}

	// Configure exclusions if the relation has exclusion features
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
			Col{Table: "t", Column: "object_id"},
			SubjectType,
			SubjectID,
		)
	}

	return plan
}

// BuildListSubjectsPlan creates a plan for generating a list_subjects function.
// This extracts plan computation from the former ListSubjectsBuilder constructor.
func BuildListSubjectsPlan(a RelationAnalysis, inline InlineSQLData) ListPlan {
	plan := ListPlan{
		Analysis:     a,
		Inline:       inline,
		FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		ObjectType:   a.ObjectType,
		Relation:     a.Relation,

		// Computed lists
		RelationList:           buildTupleLookupRelations(a),
		AllSatisfyingRelations: buildAllSatisfyingRelationsList(a),
		AllowedSubjectTypes:    buildAllowedSubjectTypesList(a),
		ComplexClosure:         filterComplexClosureRelations(a),

		// Feature configuration - list_subjects excludes wildcards
		AllowWildcard: a.Features.HasWildcard,

		// Feature flags
		HasUserset:      a.Features.HasUserset,
		HasExclusion:    a.Features.HasExclusion,
		HasIntersection: a.Features.HasIntersection,
		HasRecursive:    a.Features.HasRecursive,

		// From unified analysis
		Capabilities: a.Capabilities,
		Strategy:     a.ListStrategy,

		// Routing flags
		HasUsersetSubject:  a.Features.HasUserset || len(a.ClosureUsersetPatterns) > 0,
		HasUsersetPatterns: len(buildListUsersetPatternInputs(a)) > 0,
		HasComplexUsersets: a.HasComplexUsersetPatterns,
	}

	// Configure exclusions if the relation has exclusion features
	// For list_subjects, exclusions use object_id parameter
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
			ObjectID,
			Col{Table: "t", Column: "subject_type"},
			Col{Table: "t", Column: "subject_id"},
		)
	}

	return plan
}

// ExcludeWildcard returns true if wildcards should be excluded from results.
// This is the inverse of AllowWildcard, used by list_subjects.
func (p ListPlan) ExcludeWildcard() bool {
	return !p.AllowWildcard
}

// NeedsRecursiveStrategy returns true if the plan requires recursive/TTU handling.
func (p ListPlan) NeedsRecursiveStrategy() bool {
	return p.Strategy == ListStrategyRecursive ||
		len(p.Analysis.ClosureParentRelations) > 0 ||
		p.Analysis.Features.HasRecursive
}

// NeedsIntersectionStrategy returns true if the plan requires intersection handling.
func (p ListPlan) NeedsIntersectionStrategy() bool {
	return p.Strategy == ListStrategyIntersection || p.Analysis.Features.HasIntersection
}

// NeedsUsersetStrategy returns true if the plan requires userset handling.
func (p ListPlan) NeedsUsersetStrategy() bool {
	return p.Strategy == ListStrategyUserset ||
		p.Analysis.Features.HasUserset ||
		len(p.Analysis.ClosureUsersetPatterns) > 0
}

// NeedsDepthExceededStrategy returns true if the plan exceeds depth limits.
func (p ListPlan) NeedsDepthExceededStrategy() bool {
	return p.Strategy == ListStrategyDepthExceeded || p.Analysis.ExceedsDepthLimit
}

// NeedsSelfRefUsersetStrategy returns true if the plan has self-referential usersets.
func (p ListPlan) NeedsSelfRefUsersetStrategy() bool {
	return p.Strategy == ListStrategySelfRefUserset || p.Analysis.HasSelfReferentialUserset
}

// NeedsComposedStrategy returns true if the plan uses indirect anchor composition.
func (p ListPlan) NeedsComposedStrategy() bool {
	return p.Strategy == ListStrategyComposed || p.Analysis.IndirectAnchor != nil
}

// FeaturesString returns a human-readable description of enabled features.
func (p ListPlan) FeaturesString() string {
	return p.Analysis.Features.String()
}
