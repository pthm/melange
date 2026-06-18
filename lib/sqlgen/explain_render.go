package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// Stage 1 (slice 1) Explain codegen.
//
// This is the first end-to-end slice of explain_* function generation. It
// mirrors the shape of check_render.go but emits JSONB Trace nodes shaped to
// the contract pinned in melange/trace.go and lib/sqlgen/trace_blocks.go.
//
// What this slice handles:
//   - Direct-grant attempts → NodeDirect on hit, recorded as a failure node
//     on miss
//   - Cycle detection at the top of every function (NodeCycle on revisit)
//   - The M2002 depth-limit raise (kept identical to check_permission_internal
//     so callers can't tell Explain apart from Check at the depth boundary)
//   - The trace-root envelope (object, relation, subject, result, root,
//     truncated, node_count) — every return goes through buildExplainTraceRoot
//     so the shape never drifts
//
// What this slice deliberately defers:
//   - Implied function calls (will recursively call sibling explain_*)
//   - Userset patterns (NodeUserset wrapping)
//   - TTU / parent relations (NodeTTU wrapping)
//   - Intersection / exclusion
//   - p_max_nodes truncation
//
// Relations that need those paths will currently return a result=false trace
// with the direct attempt as the only recorded branch. That is wrong for
// implied / userset / TTU schemas but is syntactically a valid Trace —
// callers can still parse it and the dispatcher routes correctly. Subsequent
// slices fill in the gaps.

// RenderExplainFunction is the entry point for explain_* function generation.
// Mirrors RenderCheckFunction in shape; the body composes cycle detection,
// the direct-grant attempt, and a final failure return.
func RenderExplainFunction(plan CheckPlan, blocks CheckBlocks) (string, error) {
	body := buildExplainCycleDetection(plan)
	body = append(body, buildExplainDirectAttempt(plan, blocks)...)
	body = append(body, buildExplainFinalFailure(plan)...)

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    explainFunctionName(plan.ObjectType, plan.Relation),
		Args:    explainFunctionArgs(),
		Returns: "JSONB",
		Decls:   explainFunctionDecls(plan),
		Body:    body,
		Header:  explainFunctionHeader(plan),
		Cost:    explainFunctionCost(plan),
	}

	return fn.SQL() + "\n", nil
}

// explainFunctionArgs returns the per-relation explain function signature.
// Matches the check function shape so dispatcher routing is symmetric.
// p_max_nodes will be added in a follow-up slice when truncation lands.
func explainFunctionArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_subject_type", Type: "TEXT"},
		{Name: "p_subject_id", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_visited", Type: "TEXT []", Default: EmptyArray{}},
	}
}

func explainFunctionHeader(plan CheckPlan) []string {
	return []string{
		"Generated explain function for " + plan.ObjectType + "." + plan.Relation,
		"Features: " + plan.FeaturesString,
		"Stage 1 slice 1: direct-grant attempts + cycle detection only",
	}
}

// explainFunctionCost mirrors checkFunctionCost. Even though explain bodies
// are not on the request hot path, the planner sees them in EXISTS branches
// when used as evidence; matching the check tier prevents accidental
// reordering surprises.
func explainFunctionCost(plan CheckPlan) int {
	if plan.ComplexityByRelation[plan.ObjectType][plan.Relation] >= complexityRecursive {
		return recursiveCheckCost
	}
	return 0
}

// explainFunctionDecls declares the PL/pgSQL locals every Explain body needs.
// v_key drives cycle detection (mirrors recursiveCheckDecls' v_key). v_node_count
// tracks how many nodes the body emitted so the trace root reports an accurate
// count regardless of which branch returned.
func explainFunctionDecls(plan CheckPlan) []Decl {
	vKeyExpr := Concat{Parts: []Expr{
		Lit(plan.ObjectType + ":"),
		ObjectID,
		Lit(":" + plan.Relation),
	}}
	return []Decl{
		{Name: "v_key", Type: "TEXT := " + vKeyExpr.SQL()},
		{Name: "v_node_count", Type: "INTEGER := 0"},
		{Name: "v_evidence_tuple", Type: "RECORD"},
		{Name: "v_root", Type: "JSONB"},
		{Name: "v_attempts", Type: "JSONB := '[]'::JSONB"},
	}
}

// buildExplainCycleDetection emits the standard cycle / depth-limit guard.
// Same shape as buildCycleDetectionStmts but the cycle branch returns a Trace
// with a NodeCycle root instead of an integer 0.
func buildExplainCycleDetection(plan CheckPlan) []Stmt {
	return []Stmt{
		Comment{Text: "Cycle detection"},
		If{
			Cond: ArrayContains{Value: Raw("v_key"), Array: Visited},
			Then: []Stmt{
				Assign{Name: "v_root", Value: Raw(BuildCycleNode("v_key"))},
				Assign{Name: "v_node_count", Value: Raw("v_node_count + 1")},
				ReturnValue{Value: Raw(buildExplainTraceRoot(plan, "false", "v_root"))},
			},
		},
		If{
			Cond: Gte{Left: ArrayLength{Array: Visited}, Right: Int(25)},
			Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
		},
	}
}

// buildExplainDirectAttempt emits the direct-grant attempt block.
//
//	SELECT * INTO v_evidence_tuple FROM melange_tuples WHERE … LIMIT 1
//	IF FOUND THEN <emit success NodeDirect, RETURN success trace>
//	v_attempts := v_attempts || <failure NodeDirect>
//
// When the relation has no direct path the block is skipped entirely; the
// outer trace simply falls through to the final-failure return.
func buildExplainDirectAttempt(plan CheckPlan, blocks CheckBlocks) []Stmt {
	if !plan.HasDirect && !plan.HasImplied {
		return nil
	}
	if blocks.DirectCheck == nil {
		return nil
	}

	selectStmt := buildExplainDirectSelect(plan)

	successEvidence := "jsonb_build_array(" + BuildEvidenceJSON("v_evidence_tuple") + ")"
	// When the relation closure has only one entry, the evidence row's
	// relation always equals the requested relation. With multiple entries
	// (an implied chain like `viewer: editor`) the evidence may carry the
	// underlying relation name — surface that in the label so users see
	// "via editor" rather than a generic "direct grant".
	successLabel := sqldsl.QuoteLiteral("direct grant")
	if len(plan.RelationList) > 1 {
		successLabel = "('direct or implied grant via ' || v_evidence_tuple.relation)"
	}
	successNode := BuildNodeJSON(TraceNodeDirect, NodeJSONArgs{
		Label:    successLabel,
		Evidence: successEvidence,
		Result:   "true",
	})

	failureNode := BuildNodeJSON(TraceNodeDirect, NodeJSONArgs{
		Label:  sqldsl.QuoteLiteral("no direct grant"),
		Result: "false",
	})

	return []Stmt{
		Comment{Text: "Direct/Implied grant attempt"},
		SelectInto{Query: selectStmt, Variable: "v_evidence_tuple"},
		If{
			Cond: Raw("FOUND"),
			Then: []Stmt{
				Assign{Name: "v_root", Value: Raw(successNode)},
				Assign{Name: "v_node_count", Value: Raw("v_node_count + 1")},
				ReturnValue{Value: Raw(buildExplainTraceRoot(plan, "true", "v_root"))},
			},
		},
		Assign{Name: "v_node_count", Value: Raw("v_node_count + 1")},
		Assign{Name: "v_attempts", Value: Raw("v_attempts || jsonb_build_array(" + failureNode + ")")},
	}
}

// buildExplainDirectSelect renders the SELECT INTO query for the direct
// evidence lookup. Mirrors the predicate used by buildDirectCheck but as a
// SELECT … LIMIT 1 so we can capture a single evidence row rather than just
// proving existence.
func buildExplainDirectSelect(plan CheckPlan) SelectStmt {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		SelectCol("subject_type", "subject_id", "relation", "object_type", "object_id").
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Limit(1)
	if len(plan.AllowedSubjectTypes) > 0 {
		q.Where(In{Expr: Col{Table: "t", Column: "subject_type"}, Values: plan.AllowedSubjectTypes})
	}
	return q.Build()
}

// buildExplainFinalFailure emits the bottom-of-function fallthrough — every
// attempted branch failed, so wrap v_attempts in a NodeUnion and return a
// result=false trace. When v_attempts is empty (relation has no recorded
// paths at all in this slice) the union has zero children; that is still
// structurally valid and signals "nothing matched" without surfacing a
// misleading success node.
func buildExplainFinalFailure(plan CheckPlan) []Stmt {
	unionNode := BuildNodeJSON(TraceNodeUnion, NodeJSONArgs{
		Children: "v_attempts",
		Result:   "false",
	})
	return []Stmt{
		Comment{Text: "All recorded attempts failed"},
		Assign{Name: "v_root", Value: Raw(unionNode)},
		Assign{Name: "v_node_count", Value: Raw("v_node_count + 1")},
		ReturnValue{Value: Raw(buildExplainTraceRoot(plan, "false", "v_root"))},
	}
}

// buildExplainTraceRoot emits the per-call Trace envelope. resultExpr is a
// SQL boolean (literal or column ref); rootExpr is a Node JSONB. Companion
// to explainNoEntrySentinelSQL, which emits the dispatcher's fallback
// envelope; the two together are the only writers of the wire shape.
func buildExplainTraceRoot(plan CheckPlan, resultExpr, rootExpr string) string {
	return fmt.Sprintf(`jsonb_build_object(
    'object', %s,
    'relation', %s,
    'subject', %s,
    'result', %s,
    'root', %s,
    'truncated', false,
    'node_count', v_node_count)`,
		BuildObjectIdentExpr(sqldsl.QuoteLiteral(plan.ObjectType), "p_object_id"),
		sqldsl.QuoteLiteral(plan.Relation),
		BuildObjectIdentExpr("p_subject_type", "p_subject_id"),
		resultExpr,
		rootExpr,
	)
}

// explainFunctionName mirrors functionName: explain_{type}_{relation}.
func explainFunctionName(objectType, relation string) string {
	return SafeIdentifier("explain_", objectType, relation, "")
}

// explainSupported reports whether the slice-1 explain renderer can produce a
// correct trace for the relation. Returns true when access resolves through
// the direct/implied tuple SELECT alone — for those, the closure relations
// list already covers every path Check would take. Returns false for
// relations that route through userset / TTU / intersection / exclusion /
// complex implied chains; the dispatcher routes those to a clearly-labelled
// "unsupported in this version" sentinel rather than emitting a trace that
// could disagree with Check.
//
// Subsequent Stage 1 slices add the missing branches; each slice can extend
// this predicate to expose the relations its renderer now handles.
func explainSupported(a RelationAnalysis) bool {
	if a.HasComplexUsersetPatterns {
		return false
	}
	f := a.Features
	if f.HasUserset || f.HasIntersection || f.HasExclusion || f.HasRecursive {
		return false
	}
	if len(a.ParentRelations) > 0 {
		return false
	}
	if len(a.ComplexClosureRelations) > 0 {
		return false
	}
	// Features.HasExclusion / HasUserset only reflect *this* relation's direct
	// definition. A relation like `viewer: editor` where `editor: [user] but
	// not blocked` carries the exclusion on the closure side — the wrapping
	// relation is technically Direct+Implied but resolving it correctly
	// requires running the exclusion. Same shape for userset / TTU patterns
	// transitively pulled in through the closure. Until the renderer
	// dispatches those branches we have to stay off any of these schemas.
	if len(a.ClosureExcludedRelations) > 0 {
		return false
	}
	if len(a.ClosureUsersetPatterns) > 0 {
		return false
	}
	if len(a.ClosureParentRelations) > 0 {
		return false
	}
	return f.HasDirect || f.HasImplied
}
