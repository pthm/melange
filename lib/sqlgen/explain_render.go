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
	body = append(body, buildExplainImpliedAttempts(plan, blocks)...)
	body = append(body, buildExplainParentRelationAttempts(plan, blocks)...)
	body = append(body, buildExplainFinalFailure(plan)...)

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    explainFunctionName(plan.ObjectType, plan.Relation),
		Args:    explainFunctionArgs(),
		Returns: "JSONB",
		Decls:   explainFunctionDecls(plan, blocks),
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
// count regardless of which branch returned. v_child_trace is declared only
// when the body recurses into a sibling explain_* (implied function calls).
func explainFunctionDecls(plan CheckPlan, blocks CheckBlocks) []Decl {
	vKeyExpr := Concat{Parts: []Expr{
		Lit(plan.ObjectType + ":"),
		ObjectID,
		Lit(":" + plan.Relation),
	}}
	decls := []Decl{
		{Name: "v_key", Type: "TEXT := " + vKeyExpr.SQL()},
		{Name: "v_node_count", Type: "INTEGER := 0"},
		{Name: "v_evidence_tuple", Type: "RECORD"},
		{Name: "v_root", Type: "JSONB"},
		{Name: "v_attempts", Type: "JSONB := '[]'::JSONB"},
	}
	if len(blocks.ImpliedFunctionCalls) > 0 || len(blocks.ParentRelationBlocks) > 0 {
		decls = append(decls, Decl{Name: "v_child_trace", Type: "JSONB"})
	}
	if len(blocks.ParentRelationBlocks) > 0 {
		// PL/pgSQL requires the loop variable for FOR … IN <query> LOOP to
		// be a record (or list of scalars) declared in advance.
		decls = append(decls, Decl{Name: "v_parent_link", Type: "RECORD"})
	}
	return decls
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

// buildExplainImpliedAttempts emits, for each ComplexClosureRelation, a
// "call the sibling explain_*, return on success, record failure on miss"
// block.
//
//	v_child_trace := explain_{type}_{rel}(p_subject_type, p_subject_id, p_object_id, p_visited || ARRAY[v_key]);
//	v_node_count := v_node_count + COALESCE((v_child_trace->>'node_count')::INTEGER, 0);
//	IF (v_child_trace->>'result')::boolean THEN
//	    v_root := <NodeImplied wrapping child trace's root, result=true>;
//	    v_node_count := v_node_count + 1;
//	    RETURN <trace root, result=true>;
//	END IF;
//	v_node_count := v_node_count + 1;
//	v_attempts := v_attempts || jsonb_build_array(<NodeImplied wrapping child trace's root, result=false>);
//
// The call site uses the precomputed cost ordering from blocks.ImpliedFunctionCalls,
// so cheaper relations are tried first.
func buildExplainImpliedAttempts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	if len(blocks.ImpliedFunctionCalls) == 0 {
		return nil
	}

	stmts := []Stmt{Comment{Text: "Implied function call attempts"}}

	for _, call := range blocks.ImpliedFunctionCalls {
		explainFnName := explainFunctionName(plan.ObjectType, call.Relation)
		callExpr := fmt.Sprintf("%s(p_subject_type, p_subject_id, p_object_id, p_visited || ARRAY[v_key])",
			sqldsl.PrefixIdent(explainFnName, plan.DatabaseSchema))

		label := sqldsl.QuoteLiteral("implied via " + call.Relation)
		childAsArray := "jsonb_build_array(v_child_trace->'root')"

		successNode := BuildNodeJSON(TraceNodeImplied, NodeJSONArgs{
			Label:    label,
			Children: childAsArray,
			Result:   "true",
		})
		failureNode := BuildNodeJSON(TraceNodeImplied, NodeJSONArgs{
			Label:    label,
			Children: childAsArray,
			Result:   "false",
		})

		stmts = append(stmts, explainChildTraceAttempt(plan, callExpr, successNode, failureNode)...)
	}

	return stmts
}

// explainChildTraceAttempt emits the canonical "recurse into a sibling
// explain_*, fold node-count, branch on result" sequence shared by every
// recursive attempt path (implied, parent, userset, intersection). The
// caller supplies the dispatcher/function callExpr plus the success and
// failure NodeJSON SQL strings; this helper handles the v_child_trace
// COALESCE, the success-return, and the failure-attempt append.
//
// COALESCE on the callExpr guards against a callee that somehow returns
// NULL — no eligible callee does today, but a malformed result should
// surface as a failure attempt with an empty subtree rather than a
// null-children parent node.
func explainChildTraceAttempt(plan CheckPlan, callExpr, successNode, failureNode string) []Stmt {
	return []Stmt{
		Assign{Name: "v_child_trace", Value: Raw("COALESCE(" + callExpr + ", '{}'::jsonb)")},
		Assign{Name: "v_node_count", Value: Raw("v_node_count + COALESCE((v_child_trace->>'node_count')::INTEGER, 0)")},
		If{
			Cond: Raw("COALESCE((v_child_trace->>'result')::boolean, FALSE)"),
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

// buildExplainParentRelationAttempts emits a PL/pgSQL FOR loop per parent
// relation block: enumerate linking tuples in melange_tuples, then for
// each one recurse into the dispatcher (explain_permission_internal) for
// the parent's relation. Each iteration wraps the child trace in a NodeTTU
// node — success path returns immediately; misses append a failure NodeTTU
// to v_attempts.
//
//	FOR v_parent_link IN
//	    SELECT subject_type AS parent_type, subject_id AS parent_id
//	    FROM melange_tuples
//	    WHERE object_type = '<this_type>' AND relation = '<linking>' AND object_id = p_object_id
//	      AND subject_type IN ('<allowed types>')
//	LOOP
//	    -- explainChildTraceAttempt: count + success branch + failure-attempt append
//	END LOOP;
//
// The label inlines the resolved parent identifier so the trace reads
// like "via org → organization:42 ⇒ can_admin" instead of an abstract
// path description.
func buildExplainParentRelationAttempts(plan CheckPlan, blocks CheckBlocks) []Stmt {
	if len(blocks.ParentRelationBlocks) == 0 {
		return nil
	}

	stmts := []Stmt{Comment{Text: "TTU / parent-relation attempts"}}

	for _, parent := range blocks.ParentRelationBlocks {
		stmts = append(stmts, buildExplainParentLoopStmt(plan, parent))
	}

	return stmts
}

// buildExplainParentLoopStmt assembles one FOR-loop block over the linking
// tuples for a single parent relation block.
func buildExplainParentLoopStmt(plan CheckPlan, parent ParentRelationBlock) Stmt {
	driverQuery := buildExplainParentLinkingSelect(plan, parent)

	dispatcherCall := fmt.Sprintf(
		"%s(p_subject_type, p_subject_id, %s, v_parent_link.parent_type, v_parent_link.parent_id, p_visited || ARRAY[v_key])",
		sqldsl.PrefixIdent("explain_permission_internal", plan.DatabaseSchema),
		sqldsl.QuoteLiteral(parent.ParentRelation),
	)

	// Label is computed per-iteration so the resolved parent identifier
	// surfaces in the trace. The leading literal carries the linking
	// relation and the trailing literal carries the parent relation; the
	// runtime concatenation pulls in the per-iteration parent type/id.
	labelExpr := fmt.Sprintf(
		"(%s || v_parent_link.parent_type || ':' || v_parent_link.parent_id || %s)",
		sqldsl.QuoteLiteral("via "+parent.LinkingRelation+" → "),
		sqldsl.QuoteLiteral(" ⇒ "+parent.ParentRelation),
	)
	childAsArray := "jsonb_build_array(v_child_trace->'root')"

	successNode := BuildNodeJSON(TraceNodeTTU, NodeJSONArgs{
		Label:    labelExpr,
		Children: childAsArray,
		Result:   "true",
	})
	failureNode := BuildNodeJSON(TraceNodeTTU, NodeJSONArgs{
		Label:    labelExpr,
		Children: childAsArray,
		Result:   "false",
	})

	return ForLoop{
		Variable: "v_parent_link",
		Query:    driverQuery,
		Body:     explainChildTraceAttempt(plan, dispatcherCall, successNode, failureNode),
	}
}

// buildExplainParentLinkingSelect renders the FROM/WHERE for the driving
// FOR-loop SELECT: every melange_tuples row that links this object to a
// parent via the linking relation, projected as (parent_type, parent_id).
func buildExplainParentLinkingSelect(plan CheckPlan, parent ParentRelationBlock) SelectStmt {
	q := Tuples(plan.DatabaseSchema, "link").
		ObjectType(plan.ObjectType).
		Relations(parent.LinkingRelation).
		SelectExpr(
			Alias{Expr: Col{Table: "link", Column: "subject_type"}, Name: "parent_type"},
			Alias{Expr: Col{Table: "link", Column: "subject_id"}, Name: "parent_id"},
		).
		Where(Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID})
	if len(parent.AllowedLinkingTypes) > 0 {
		q.Where(In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypes})
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

// explainLocalSupported is the local-only check: does this relation's
// renderer handle every feature the analysis surfaces? Used as the seed of
// ComputeExplainEligibility's fixed point. Transitive closure-relation
// dependencies are handled by the wrapper, not here.
//
// Subsequent Stage 1 slices drop conditions from this predicate as the
// renderer learns more branches. Slice 1.2 drops the
// ComplexClosureRelations gate — the renderer now recursively calls
// sibling explain_* — but the wrapper still gates relations whose
// dependencies are themselves not yet supported.
func explainLocalSupported(a RelationAnalysis) bool {
	if a.HasComplexUsersetPatterns {
		return false
	}
	f := a.Features
	if f.HasUserset || f.HasIntersection || f.HasExclusion {
		return false
	}
	// HasRecursive on Features and ParentRelations on the analysis both
	// mark TTU relations; their renderer (the FOR-loop recursing into
	// explain_permission_internal) lives in slice 1.3. Both pass the
	// local check — the transitive sweep verifies that every allowed
	// parent (type, relation) is itself eligible before the wrapping
	// relation stays in the eligible map.
	return f.HasDirect || f.HasImplied || f.HasRecursive
}

// ComputeExplainEligibility returns, for each (object_type, relation), whether
// the current explain renderer can produce a trace that agrees with Check.
//
// The result is the fixed point of:
//   - locally supported (explainLocalSupported)
//   - every ComplexClosureRelation is itself eligible
//     (same-object-type implied function call dependency)
//   - every parent (type, relation) in ParentRelations is eligible
//     (cross-object-type TTU dependency)
//
// Eligibility is monotonically non-increasing: each pass can downgrade a
// relation when a freshly-discovered ineligible dependency surfaces, but
// nothing ever flips back. The fixed point is reached when no relation
// changes in a pass.
//
// The conservative cross-type rule (ALL AllowedLinkingTypes must be
// eligible) means a TTU relation with even one ineligible parent type
// routes to the dispatcher's no-entry sentinel rather than emitting
// partial traces — explicit "not yet supported" beats silently-skipped
// linking tuples.
//
// Exported so callers that build a CollectFunctionNames input by hand can
// produce the same map GenerateSQL stashes on GeneratedSQL.ExplainEligible.
func ComputeExplainEligibility(analyses []RelationAnalysis) map[string]map[string]bool {
	eligible := make(map[string]map[string]bool, len(analyses))
	for _, a := range analyses {
		m, ok := eligible[a.ObjectType]
		if !ok {
			m = make(map[string]bool)
			eligible[a.ObjectType] = m
		}
		m[a.Relation] = explainLocalSupported(a)
	}

	for {
		changed := false
		for _, a := range analyses {
			if !eligible[a.ObjectType][a.Relation] {
				continue
			}
			if anyExplainDepIneligible(a, eligible) {
				eligible[a.ObjectType][a.Relation] = false
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return eligible
}

// anyExplainDepIneligible reports whether any of the relation's recursive
// dependencies — same-type implied function calls or cross-type parent
// relations — is currently marked ineligible. Used as the per-iteration
// downgrade trigger inside the eligibility fixed point.
//
// An empty AllowedLinkingTypes is treated as "no parents to verify" so the
// wrapper stays eligible. In practice the analyser always populates the
// list from the schema's `parent: [type, ...]` declaration; an empty list
// shouldn't occur. If it ever does, the FOR-loop driver query would
// enumerate every linking tuple regardless of parent_type, and the
// dispatcher would route any unknown parent_type to its no-entry
// sentinel — structurally valid but a weaker UX than the explicit
// "not yet supported" label.
func anyExplainDepIneligible(a RelationAnalysis, eligible map[string]map[string]bool) bool {
	for _, dep := range a.ComplexClosureRelations {
		if !eligible[a.ObjectType][dep] {
			return true
		}
	}
	for _, parent := range a.ParentRelations {
		for _, parentType := range parent.AllowedLinkingTypes {
			if !eligible[parentType][parent.Relation] {
				return true
			}
		}
	}
	return false
}

