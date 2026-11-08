package sqlgen

import "fmt"

// TraceSQL is a model's explain and expand functions with their dispatchers,
// generated apart from a migration: for explain and expand on demand against
// a database migrated with those families off. See GenerateTraceSQL.
type TraceSQL struct {
	ExplainFunctions  []string
	ExplainDispatcher string
	ExpandFunctions   []string
	ExpandDispatcher  string
}

// Statements returns the CREATE FUNCTION statements in creation order: the
// per-relation functions, then each family's internal and public
// dispatchers, so a LANGUAGE sql wrapper is created after what it calls.
func (t TraceSQL) Statements() []string {
	var out []string
	out = append(out, t.ExplainFunctions...)
	if t.ExplainDispatcher != "" {
		out = append(out, t.ExplainDispatcher)
	}
	out = append(out, t.ExpandFunctions...)
	if t.ExpandDispatcher != "" {
		out = append(out, t.ExpandDispatcher)
	}
	return out
}

// TraceSQLOptions configures GenerateTraceSQL.
type TraceSQLOptions struct {
	// Explain and Expand select the families generated.
	Explain, Expand bool

	// FunctionSchema is the schema the functions are created in and call
	// each other through: "pg_temp" for functions that live for one
	// transaction. Empty means the data schema. PostgreSQL never resolves an
	// unqualified function name in pg_temp, so every call between the
	// generated functions is qualified with it.
	FunctionSchema string

	// Exposure is the exposure the database was migrated with. The set
	// generated is what a migration with that exposure and the selected
	// families switched on installs; the exposure's explain and expand flags
	// are ignored.
	Exposure Exposure
}

// GenerateTraceSQL generates the explain and/or expand functions a migration
// with opts.Exposure and those families on would install, and only those.
// Their bodies are the bodies migrate renders, except that the functions
// are created in, and call each other through, opts.FunctionSchema.
// Everything else stays as migrate renders it for databaseSchema: the check
// functions explain calls, expand's qualified melange_tuples, explain's SET
// search_path and the raising helper of a restricted exposure. With an empty
// databaseSchema those stay unqualified (and explain has no SET clause), as
// they are in a database migrated without a schema.
//
// The functions call only functions every migration with opts.Exposure
// installs (check functions and dispatchers, which are always generated for
// the exposed relations' closure), whatever its explain and expand flags.
func GenerateTraceSQL(analyses []RelationAnalysis, inline InlineSQLData, databaseSchema string, opts TraceSQLOptions) (TraceSQL, error) {
	if err := opts.Exposure.Validate(); err != nil {
		return TraceSQL{}, err
	}
	e := opts.Exposure
	e.Functions.Explain, e.Functions.Expand = nil, nil
	exposure := newExposureClosure(analyses, e)
	functionSchema := opts.FunctionSchema
	if functionSchema == "" {
		functionSchema = databaseSchema
	}

	var out TraceSQL
	if opts.Expand {
		// Eligibility and emission as in GenerateSQLWithOptions.
		eligible := make(map[string]map[string]bool, len(analyses))
		for _, a := range analyses {
			if !a.Capabilities.CheckAllowed {
				continue
			}
			fn, ok := generateExpandFunction(a, databaseSchema, opts.FunctionSchema)
			if !ok {
				continue
			}
			if eligible[a.ObjectType] == nil {
				eligible[a.ObjectType] = make(map[string]bool)
			}
			eligible[a.ObjectType][a.Relation] = true
			if exposure.installsExpand(a.ObjectType, a.Relation) {
				out.ExpandFunctions = append(out.ExpandFunctions, fn)
			}
		}
		out.ExpandDispatcher = generateExpandDispatcher(analyses, databaseSchema, functionSchema, eligible, exposure)
	}
	if opts.Explain {
		complexityByRelation := buildClosureComplexityIndex(analyses)
		eligible := ComputeExplainEligibility(analyses)
		inlineIx := newInlineIndex(inline)
		for _, a := range analyses {
			if !a.Capabilities.CheckAllowed || !eligible[a.ObjectType][a.Relation] || !exposure.installsExplain(a.ObjectType, a.Relation) {
				continue
			}
			fn, err := generateExplainFunction(a, inlineIx, databaseSchema, opts.FunctionSchema, complexityByRelation)
			if err != nil {
				return TraceSQL{}, fmt.Errorf("generating explain function: %w", err)
			}
			out.ExplainFunctions = append(out.ExplainFunctions, fn)
		}
		var err error
		out.ExplainDispatcher, err = generateExplainDispatcher(analyses, databaseSchema, functionSchema, eligible, exposure)
		if err != nil {
			return TraceSQL{}, fmt.Errorf("generating explain dispatcher: %w", err)
		}
	}
	return out, nil
}
