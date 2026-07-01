package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// Explain orchestration. Mirrors check_functions.go but produces
// per-relation explain_* functions and the explain_permission dispatcher.
//
// Public entry: GenerateSQLWithOptions calls generateExplainFunction per
// CheckAllowed relation and generateExplainDispatcher once for the whole
// schema. The dispatcher routes (object_type, relation) → explain_{type}_{relation},
// or returns a "no entry" trace when nothing matches so callers can deserialise
// a stable shape even on unknown inputs.

// explainDispatcherInternalArgs is the explain dispatcher's internal
// signature. Mirrors explainFunctionArgs but with p_relation /
// p_object_type added; the dispatcher routes by these.
func explainDispatcherInternalArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
		{Name: "p_max_nodes", Type: "INTEGER", Default: Raw("NULL")},
	}
}

// explainDispatcherPublicArgs drops the internal-only p_visited (always
// starts empty at the public entry) and keeps p_max_nodes so SDK callers
// can request truncation without manipulating session state.
func explainDispatcherPublicArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_max_nodes", Type: "INTEGER", Default: Raw("NULL")},
	}
}

func generateExplainFunction(a RelationAnalysis, inline InlineSQLData, databaseSchema string, complexityByRelation map[string]map[string]int) (string, error) {
	plan := BuildCheckPlanWithOrdering(a, inline, databaseSchema, false, complexityByRelation)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		return "", fmt.Errorf("building check blocks for explain %s.%s: %w", a.ObjectType, a.Relation, err)
	}
	return RenderExplainFunction(plan, blocks)
}

// generateExplainDispatcher renders the public explain_permission function.
// Mirrors check_permission's structure: a public wrapper (SQL function) that
// hands off to an internal PL/pgSQL function. The internal function applies
// the M2002 depth check before routing, identical to check_permission_internal
// so depth behaviour is symmetric between Check and Explain.
//
// Routing returns a JSONB trace. When no case matches, the internal function
// returns a "no_entry" trace shape (result=false, root.type="union" with a
// label noting the unsupported pair) so callers get a structurally valid
// response rather than NULL or an error.
//
// eligible is the precomputed (object_type, relation) → bool map from
// ComputeExplainEligibility; only eligible pairs become CASE branches.
func generateExplainDispatcher(analyses []RelationAnalysis, databaseSchema string, eligible map[string]map[string]bool) (string, error) {
	cases := buildExplainDispatcherCases(analyses, databaseSchema, eligible)
	if len(cases) == 0 {
		return renderEmptyExplainDispatcher(databaseSchema), nil
	}
	return renderExplainDispatcherWithCases(databaseSchema, cases), nil
}

// buildExplainDispatcherCases mirrors buildDispatcherCases but only emits
// cases for relations whose explain renderer can produce a correct trace
// according to the precomputed eligibility map. Unsupported (type, relation)
// pairs fall through to the dispatcher's else branch — the no-entry sentinel.
func buildExplainDispatcherCases(analyses []RelationAnalysis, databaseSchema string, eligible map[string]map[string]bool) []DispatcherCase {
	var cases []DispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.CheckAllowed {
			continue
		}
		if !eligible[a.ObjectType][a.Relation] {
			continue
		}
		cases = append(cases, DispatcherCase{
			DatabaseSchema:    databaseSchema,
			ObjectType:        a.ObjectType,
			Relation:          a.Relation,
			CheckFunctionName: explainFunctionName(a.ObjectType, a.Relation),
		})
	}
	return cases
}

func renderExplainDispatcherWithCases(databaseSchema string, cases []DispatcherCase) string {
	caseExpr := buildExplainDispatcherCaseExpr(cases)

	internalFn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "explain_permission_internal",
		Args:    explainDispatcherInternalArgs(),
		Returns: "JSONB",
		Body: []Stmt{
			Comment{Text: "Depth limit check shared with check_permission_internal"},
			If{
				Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
				Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
			},
			ReturnValue{Value: Raw("(SELECT " + caseExpr.SQL() + ")")},
		},
		Header: []string{
			"Generated internal dispatcher for explain_permission",
			"Routes (object_type, relation) to specialised explain_* functions",
			"Returns a no-entry Trace JSONB when the pair is unknown so callers",
			"never see NULL",
		},
		// Same expensive tier as check_permission_internal: routes into
		// recursive bodies, so OR/AND chains should evaluate cheap branches
		// first.
		Cost: recursiveCheckCost,
	}

	publicFn := SqlFunction{
		Schema:  databaseSchema,
		Name:    "explain_permission",
		Args:    explainDispatcherPublicArgs(),
		Returns: "JSONB",
		Body:    Raw("SELECT " + sqldsl.PrefixIdent("explain_permission_internal", databaseSchema) + "(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[], p_max_nodes)"),
		Header: []string{
			"Generated public dispatcher for explain_permission",
			"Companion to check_permission — returns a JSONB Trace describing",
			"why the check decision was reached",
		},
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

// renderEmptyExplainDispatcher emits a no-op dispatcher when the schema has
// no eligible relations. Returns a structurally valid Trace with an empty
// union root so callers parsing the output don't choke on NULL.
func renderEmptyExplainDispatcher(databaseSchema string) string {
	body := "SELECT " + explainNoEntrySentinelSQL("no relations defined")

	internalFn := SqlFunction{
		Schema:  databaseSchema,
		Name:    "explain_permission_internal",
		Args:    explainDispatcherInternalArgs(),
		Returns: "JSONB",
		Body:    Raw(body),
		Header: []string{
			"Generated empty dispatcher for explain_permission",
			"(no relations defined — every request returns a deny trace)",
		},
	}

	publicFn := SqlFunction{
		Schema:  databaseSchema,
		Name:    "explain_permission",
		Args:    explainDispatcherPublicArgs(),
		Returns: "JSONB",
		Body:    Raw(body),
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

// buildExplainDispatcherCaseExpr mirrors buildDispatcherCaseExpr but with a
// JSONB no-entry sentinel in the ELSE branch. The shape matches the empty
// dispatcher's output so the unknown-pair handling is consistent in both
// paths.
func buildExplainDispatcherCaseExpr(cases []DispatcherCase) CaseExpr {
	whens := make([]CaseWhen, 0, len(cases))
	for _, c := range cases {
		cond := AndExpr{Exprs: []Expr{
			Eq{Left: ObjectType, Right: Lit(c.ObjectType)},
			Eq{Left: Raw("p_relation"), Right: Lit(c.Relation)},
		}}
		result := Func{
			Schema: c.DatabaseSchema,
			Name:   c.CheckFunctionName,
			Args:   []Expr{SubjectType, SubjectID, ObjectID, Visited, Raw("p_max_nodes")},
		}
		whens = append(whens, CaseWhen{Cond: cond, Result: result})
	}

	noEntry := Raw(explainNoEntrySentinelSQL(
		"explain not yet supported for this (object_type, relation) — no generated explain function for the requested pair. Confirm the pair exists in the migrated schema.",
	))

	return CaseExpr{Whens: whens, Else: noEntry}
}

// explainNoEntrySentinelSQL emits the JSONB Trace returned when the
// dispatcher has no CASE branch for the requested (object_type, relation).
// Routed through BuildNodeJSON so the wire shape stays consistent with
// every other emitted trace. The envelope is built inline because the
// relation field is the runtime `p_relation` column ref rather than a
// literal (BuildTraceRoot quotes its relation arg).
func explainNoEntrySentinelSQL(label string) string {
	root := BuildNodeJSON(TraceNodeUnion, NodeJSONArgs{
		Label:    sqldsl.QuoteLiteral(label),
		Result:   "false",
		Children: "'[]'::jsonb",
	})
	return fmt.Sprintf(`jsonb_build_object(
    'object', %s,
    'relation', p_relation,
    'subject', %s,
    'result', false,
    'root', %s,
    'truncated', false,
    'node_count', 1)`,
		BuildObjectIdentExpr("p_object_type", "p_object_id"),
		BuildObjectIdentExpr("p_subject_type", "p_subject_id"),
		root,
	)
}
