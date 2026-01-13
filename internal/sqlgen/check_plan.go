package sqlgen

// CheckPlan contains all computed data needed to generate a check function.
// This separates plan computation from block building and rendering.
// Unlike CheckFunctionData, this contains no pre-rendered SQL fragments.
type CheckPlan struct {
	// Input data
	Analysis RelationAnalysis
	Inline   InlineSQLData

	// Function identity
	FunctionName              string
	InternalCheckFunctionName string // Dispatcher function for recursive calls
	ObjectType                string
	Relation                  string
	FeaturesString            string // Human-readable features for SQL comments

	// Feature configuration
	AllowWildcard bool            // Whether wildcards are allowed
	NoWildcard    bool            // True if this is a no-wildcard variant
	Exclusions    ExclusionConfig // Exclusion rules configuration

	// Feature flags (derived from analysis)
	HasDirect       bool
	HasImplied      bool
	HasUserset      bool
	HasExclusion    bool
	HasIntersection bool
	HasRecursive    bool

	// Derived computation flags
	HasStandaloneAccess    bool // Has access paths outside of intersections
	HasComplexUsersets     bool // Has userset patterns requiring function calls
	NeedsPLpgSQL           bool // Requires PL/pgSQL (not pure SQL)
	HasParentRelations     bool // Has TTU patterns
	HasImpliedFunctionCall bool // Has complex implied relations needing function calls

	// Eligibility from unified analysis
	Capabilities GenerationCapabilities

	// Relation lists for closure lookups
	RelationList        []string // Relations for tuple lookup (self + simple closure)
	ComplexClosure      []string // Complex closure relations
	AllowedSubjectTypes []string // Subject types allowed for this relation
}

// BuildCheckPlan creates a plan for generating a check function.
// Set noWildcard to true to generate a no-wildcard variant.
func BuildCheckPlan(a RelationAnalysis, inline InlineSQLData, noWildcard bool) CheckPlan {
	hasWildcard := a.Features.HasWildcard && !noWildcard

	// Determine function names
	funcName := functionName(a.ObjectType, a.Relation)
	internalFn := "check_permission_internal"
	if noWildcard {
		funcName = functionNameNoWildcard(a.ObjectType, a.Relation)
		internalFn = "check_permission_no_wildcard_internal"
	}

	plan := CheckPlan{
		Analysis:                  a,
		Inline:                    inline,
		FunctionName:              funcName,
		InternalCheckFunctionName: internalFn,
		ObjectType:                a.ObjectType,
		Relation:                  a.Relation,
		FeaturesString:            a.Features.String(),

		// Feature configuration
		AllowWildcard: hasWildcard,
		NoWildcard:    noWildcard,

		// Feature flags
		HasDirect:       a.Features.HasDirect,
		HasImplied:      a.Features.HasImplied,
		HasUserset:      a.Features.HasUserset,
		HasExclusion:    a.Features.HasExclusion,
		HasIntersection: a.Features.HasIntersection,
		HasRecursive:    a.Features.HasRecursive,

		// Derived flags
		HasStandaloneAccess:    computeHasStandaloneAccess(a),
		HasComplexUsersets:     a.HasComplexUsersetPatterns,
		NeedsPLpgSQL:           a.Features.NeedsPLpgSQL() || a.HasComplexUsersetPatterns,
		HasParentRelations:     len(a.ParentRelations) > 0,
		HasImpliedFunctionCall: len(a.ComplexClosureRelations) > 0,

		// Eligibility
		Capabilities: a.Capabilities,

		// Relation lists
		RelationList:        buildTupleLookupRelations(a),
		ComplexClosure:      filterComplexClosureRelations(a),
		AllowedSubjectTypes: buildAllowedSubjectTypesList(a),
	}

	// Configure exclusions if the relation has exclusion features
	if a.Features.HasExclusion {
		plan.Exclusions = buildExclusionInput(
			a,
			ObjectID, // p_object_id parameter
			SubjectType,
			SubjectID,
		)
	}

	return plan
}

// DetermineCheckFunctionType returns which type of check function to generate.
// Returns one of: "direct", "intersection", "recursive", "recursive_intersection"
func (p CheckPlan) DetermineCheckFunctionType() string {
	switch {
	case !p.NeedsPLpgSQL && !p.HasIntersection:
		return "direct"
	case !p.NeedsPLpgSQL && p.HasIntersection:
		return "intersection"
	case p.NeedsPLpgSQL && !p.HasIntersection:
		return "recursive"
	default:
		return "recursive_intersection"
	}
}
