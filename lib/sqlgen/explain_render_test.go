package sqlgen

import (
	"strings"
	"testing"
)

// TestRenderExplainFunction_DirectOnly pins the shape of the explain function
// emitted for the simplest possible relation — `define viewer: [user]`.
// The body must:
//   - declare v_key, v_node_count, v_evidence_tuple, v_root, v_attempts
//   - perform cycle detection (returns NodeCycle trace when v_key in visited)
//   - SELECT INTO v_evidence_tuple from melange_tuples
//   - return a result=true trace with NodeDirect on FOUND
//   - record a NodeDirect{result: false} into v_attempts on miss
//   - return a result=false trace wrapping v_attempts in NodeUnion at the end
func TestRenderExplainFunction_DirectOnly(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true)
	a.SatisfyingRelations = []string{"viewer"}

	plan := BuildCheckPlanWithOrdering(a, InlineSQLData{}, "", false, nil)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}

	got, err := RenderExplainFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderExplainFunction: %v", err)
	}

	wants := []string{
		"CREATE OR REPLACE FUNCTION explain_document_viewer",
		"RETURNS JSONB",
		"v_key TEXT :=",
		"v_node_count INTEGER := 0",
		"v_evidence_tuple RECORD",
		"v_attempts JSONB := '[]'::JSONB",
		// cycle detection branch returns a cycle node
		"'type', 'cycle'",
		"resolution too complex",
		// direct attempt
		"SELECT INTO v_evidence_tuple",
		"IF FOUND THEN",
		// success path emits a direct node with evidence + result=true
		"'type', 'direct'",
		"'label', 'direct grant'",
		"'evidence'",
		"'result', true",
		// failure path records a direct node with result=false
		"'label', 'no direct grant'",
		"'result', false",
		// final fallthrough union
		"'type', 'union'",
		"'children', v_attempts",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_NoDirect verifies a relation with no direct path
// still produces a valid function — body skips the direct attempt and falls
// straight to the failure return. The function is callable; it just always
// returns false for now until later slices add implied/userset/TTU paths.
func TestRenderExplainFunction_NoDirect(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasUserset: true}, false)
	a.SatisfyingRelations = []string{"viewer"}

	plan := BuildCheckPlanWithOrdering(a, InlineSQLData{}, "", false, nil)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}

	got, err := RenderExplainFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderExplainFunction: %v", err)
	}

	if !strings.Contains(got, "CREATE OR REPLACE FUNCTION explain_document_viewer") {
		t.Errorf("missing function header in:\n%s", got)
	}
	if strings.Contains(got, "SELECT INTO v_evidence_tuple") {
		t.Errorf("relation with no direct path should not emit direct SELECT; got:\n%s", got)
	}
	if !strings.Contains(got, "'type', 'union'") {
		t.Errorf("final union root missing in:\n%s", got)
	}
}

// TestGenerateExplainDispatcher_Routes verifies the dispatcher CASE expression
// routes (object_type, relation) pairs to per-relation explain functions and
// falls through to a no-entry sentinel on unknown pairs.
func TestGenerateExplainDispatcher_Routes(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
		mkAnalysis("document", "editor", RelationFeatures{HasDirect: true}, true),
	}
	got, err := generateExplainDispatcher(analyses, "")
	if err != nil {
		t.Fatalf("generateExplainDispatcher: %v", err)
	}
	wants := []string{
		"explain_permission_internal",
		"explain_permission(",
		"explain_document_viewer",
		"explain_document_editor",
		// Sentinel for unknown / unsupported pairs — clearer wording so
		// callers understand the limitation instead of guessing.
		"explain not yet supported",
		// matches the same M2002 depth limit as check_permission_internal
		"resolution too complex",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in dispatcher SQL:\n%s", w, got)
		}
	}
}

// TestGenerateExplainDispatcher_Empty exercises the no-relations path.
// Output must still be a structurally valid Trace shape so the runtime can
// deserialise without special-casing.
func TestGenerateExplainDispatcher_Empty(t *testing.T) {
	got, err := generateExplainDispatcher(nil, "")
	if err != nil {
		t.Fatalf("generateExplainDispatcher(nil): %v", err)
	}
	wants := []string{
		"explain_permission_internal",
		"explain_permission(",
		"no relations defined",
		"'type', 'union'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in empty dispatcher SQL:\n%s", w, got)
		}
	}
}

// TestGenerateSQL_PopulatesExplainFields confirms the GenerateSQL wiring
// populates ExplainFunctions and ExplainDispatcher in lockstep with the check
// fields, which the migrator depends on to register the new functions.
func TestGenerateSQL_PopulatesExplainFields(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	out, err := GenerateSQL(analyses, InlineSQLData{}, "")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if len(out.ExplainFunctions) != 1 {
		t.Errorf("ExplainFunctions: got %d entries, want 1", len(out.ExplainFunctions))
	}
	if out.ExplainDispatcher == "" {
		t.Errorf("ExplainDispatcher should not be empty")
	}
	if !strings.Contains(out.ExplainDispatcher, "explain_document_viewer") {
		t.Errorf("dispatcher should route to per-relation function; got:\n%s", out.ExplainDispatcher)
	}
}
