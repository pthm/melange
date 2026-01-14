package sqlgen

// ListPlan contains all computed data needed to generate a list function.
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
	HasUsersetSubject   bool // Has userset subject matching capability
	HasUsersetPatterns  bool // Has userset patterns to expand
	HasComplexUsersets  bool // Has userset patterns requiring check_permission calls
	HasStandaloneAccess bool // Has standalone access paths (not constrained by intersection)

	// Optimization flags
	UseCTEExclusion bool // Use CTE-based exclusion optimization (precompute + anti-join)
}

// BuildListObjectsPlan creates a plan for generating a list_objects function.
func BuildListObjectsPlan(a RelationAnalysis, inline InlineSQLData) ListPlan {
	plan := buildBasePlan(a, inline, listObjectsFunctionName(a.ObjectType, a.Relation))
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
func BuildListSubjectsPlan(a RelationAnalysis, inline InlineSQLData) ListPlan {
	plan := buildBasePlan(a, inline, listSubjectsFunctionName(a.ObjectType, a.Relation))
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
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

func buildBasePlan(a RelationAnalysis, inline InlineSQLData, functionName string) ListPlan {
	return ListPlan{
		Analysis:     a,
		Inline:       inline,
		FunctionName: functionName,
		ObjectType:   a.ObjectType,
		Relation:     a.Relation,

		RelationList:           buildTupleLookupRelations(a),
		AllSatisfyingRelations: buildAllSatisfyingRelationsList(a),
		AllowedSubjectTypes:    buildAllowedSubjectTypesList(a),
		ComplexClosure:         filterComplexClosureRelations(a),

		AllowWildcard: a.Features.HasWildcard,

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
	}
}

func (p ListPlan) ExcludeWildcard() bool {
	return !p.AllowWildcard
}

func (p ListPlan) FeaturesString() string {
	return p.Analysis.Features.String()
}
