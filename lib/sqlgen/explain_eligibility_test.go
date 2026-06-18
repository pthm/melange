package sqlgen

import (
	"strings"
	"testing"
)

// Slice 1.2 introduced the transitive eligibility sweep so a relation's
// renderer can recurse into a sibling explain_* without the dispatcher ever
// naming a function that wasn't generated. The fixed point handles three
// cases: locally supported with no deps, locally supported with all deps
// supported, and locally supported but depended on an unsupported relation
// (the wrapper must be marked ineligible too).
//
// In Stage 1 slice 1.2 no real schema activates the implied-attempt code
// path because every relation that lands in ComplexClosureRelations has at
// least one feature (Userset/Exclusion/Intersection/Recursive) the
// renderer still gates out. These tests use synthetic analyses to pin the
// machinery; slice 1.3+ will exercise it via real fixtures once TTU lands.

func TestComputeExplainEligibility_LocalOnly(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("doc", "owner", RelationFeatures{HasDirect: true}, false),
		mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasImplied: true}, false),
		mkAnalysis("doc", "blocked", RelationFeatures{HasDirect: true, HasExclusion: true}, false),
	}
	got := ComputeExplainEligibility(analyses)
	if !got["doc"]["owner"] {
		t.Errorf("owner should be eligible (Direct only)")
	}
	if !got["doc"]["viewer"] {
		t.Errorf("viewer should be eligible (Direct+Implied, no complex deps)")
	}
	if got["doc"]["blocked"] {
		t.Errorf("blocked should be ineligible (HasExclusion)")
	}
}

func TestComputeExplainEligibility_TransitiveDownward(t *testing.T) {
	// wrapper depends on dep which is ineligible (HasUserset). wrapper must
	// also be marked ineligible even though its own features are simple.
	dep := mkAnalysis("doc", "dep", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	wrapper := mkAnalysis("doc", "wrapper", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	wrapper.ComplexClosureRelations = []string{"dep"}
	analyses := []RelationAnalysis{dep, wrapper}

	got := ComputeExplainEligibility(analyses)
	if got["doc"]["dep"] {
		t.Errorf("dep should be ineligible (HasUserset)")
	}
	if got["doc"]["wrapper"] {
		t.Errorf("wrapper should be downgraded — depends on ineligible dep")
	}
}

func TestComputeExplainEligibility_TransitiveChain(t *testing.T) {
	// Three relations in a chain where wrapper depends on middle, and
	// middle depends on bad. Bad is locally ineligible (HasUserset);
	// middle is locally fine but transitively downgrades to ineligible;
	// then in the next pass wrapper itself gets downgraded.
	bad := mkAnalysis("doc", "bad", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	middle := mkAnalysis("doc", "middle", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	middle.ComplexClosureRelations = []string{"bad"}
	wrapper := mkAnalysis("doc", "wrapper", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	wrapper.ComplexClosureRelations = []string{"middle"}
	analyses := []RelationAnalysis{bad, middle, wrapper}

	got := ComputeExplainEligibility(analyses)
	if got["doc"]["bad"] {
		t.Errorf("bad should be ineligible (HasUserset)")
	}
	if got["doc"]["middle"] {
		t.Errorf("middle should be transitively downgraded by bad")
	}
	if got["doc"]["wrapper"] {
		t.Errorf("wrapper should be transitively downgraded by middle in the next pass")
	}
}

func TestComputeExplainEligibility_CrossTypeTTU_AllParentsEligible(t *testing.T) {
	// repository.can_admin TTUs into organization.can_admin via the org
	// linking relation. Both parent types declared (only one here) are
	// individually eligible → wrapper is eligible.
	orgAdmin := mkAnalysis("organization", "can_admin", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	repoAdmin := mkAnalysis("repository", "can_admin", RelationFeatures{HasDirect: true, HasImplied: true, HasRecursive: true}, false)
	repoAdmin.ParentRelations = []ParentRelationInfo{{
		Relation:            "can_admin",
		LinkingRelation:     "org",
		AllowedLinkingTypes: []string{"organization"},
	}}
	got := ComputeExplainEligibility([]RelationAnalysis{orgAdmin, repoAdmin})
	if !got["organization"]["can_admin"] {
		t.Errorf("organization.can_admin should be eligible (Direct+Implied)")
	}
	if !got["repository"]["can_admin"] {
		t.Errorf("repository.can_admin should be eligible — parent is eligible")
	}
}

func TestComputeExplainEligibility_CrossTypeTTU_OneParentIneligible(t *testing.T) {
	// Conservative rule: ALL allowed parent types must have their relation
	// eligible. A single ineligible parent disables the whole TTU wrapper.
	orgAdmin := mkAnalysis("organization", "can_admin", RelationFeatures{HasDirect: true}, false)
	folderAdmin := mkAnalysis("folder", "can_admin", RelationFeatures{HasDirect: true, HasExclusion: true}, false)
	repoAdmin := mkAnalysis("repository", "can_admin", RelationFeatures{HasDirect: true, HasRecursive: true}, false)
	repoAdmin.ParentRelations = []ParentRelationInfo{{
		Relation:            "can_admin",
		LinkingRelation:     "org",
		AllowedLinkingTypes: []string{"organization", "folder"},
	}}
	got := ComputeExplainEligibility([]RelationAnalysis{orgAdmin, folderAdmin, repoAdmin})
	if !got["organization"]["can_admin"] {
		t.Errorf("organization.can_admin should be eligible")
	}
	if got["folder"]["can_admin"] {
		t.Errorf("folder.can_admin should be ineligible (HasExclusion)")
	}
	if got["repository"]["can_admin"] {
		t.Errorf("repository.can_admin should be ineligible — folder parent dragged it down")
	}
}

func TestComputeExplainEligibility_PerObjectTypeIsolation(t *testing.T) {
	// ComplexClosureRelations names are scoped to the wrapping relation's
	// object type. A "viewer" on document and a "viewer" on folder are
	// distinct entries; downgrading one must not affect the other.
	docViewer := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, false)
	folderViewer := mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	analyses := []RelationAnalysis{docViewer, folderViewer}

	got := ComputeExplainEligibility(analyses)
	if !got["document"]["viewer"] {
		t.Errorf("document.viewer should not be touched by folder.viewer's ineligibility")
	}
	if got["folder"]["viewer"] {
		t.Errorf("folder.viewer should be ineligible (HasUserset)")
	}
}

// TestRenderExplainFunction_ImpliedFunctionCallEmits hand-builds an analysis
// with a ComplexClosureRelation entry so the implied-attempt block actually
// renders. Pins the SQL shape: assignment of v_child_trace, the
// (result->>'result')::boolean check, NodeImplied wrapping the child's root,
// and the failure-attempt append. Real OpenFGA schemas don't hit this path
// in slice 1.2 (every ComplexClosureRelation candidate is itself ineligible)
// but the code is exercised here so the contract is locked.
func TestRenderExplainFunction_ImpliedFunctionCallEmits(t *testing.T) {
	a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	a.SatisfyingRelations = []string{"viewer", "editor"}
	a.ComplexClosureRelations = []string{"editor"}

	plan := BuildCheckPlanWithOrdering(a, InlineSQLData{}, "", false, nil)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}
	if len(blocks.ImpliedFunctionCalls) == 0 {
		t.Fatalf("expected ImpliedFunctionCalls populated for the test fixture; got 0")
	}

	got, err := RenderExplainFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderExplainFunction: %v", err)
	}

	wants := []string{
		// New local declared only when the body recurses
		"v_child_trace JSONB",
		// Recursive call to sibling explain function, guarded by COALESCE
		// so a NULL return is normalised to an empty object (slice 1.3+
		// will recurse for real and a malformed callee should surface as
		// a failure attempt, not a null-children NodeImplied).
		"v_child_trace := COALESCE(explain_doc_editor(p_subject_type, p_subject_id, p_object_id, p_visited || ARRAY[v_key]), '{}'::jsonb)",
		// Tally the child's node count into our running counter
		"v_node_count := v_node_count + COALESCE((v_child_trace->>'node_count')::INTEGER, 0)",
		// Success path: wrap child's root in NodeImplied
		"(v_child_trace->>'result')::boolean",
		"'type', 'implied'",
		"'label', 'implied via editor'",
		"jsonb_build_array(v_child_trace->'root')",
		"'result', true",
		// Failure path: append the same node with result=false to v_attempts
		"v_attempts := v_attempts || jsonb_build_array(jsonb_build_object('type', 'implied'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_TTUFlow exercises slice 1.3's FOR-loop
// emission. The fixture is a TTU relation that recurses into the
// dispatcher (explain_permission_internal) for the parent's relation.
func TestRenderExplainFunction_TTUFlow(t *testing.T) {
	a := mkAnalysis("repository", "can_admin", RelationFeatures{HasDirect: true, HasRecursive: true}, false)
	a.SatisfyingRelations = []string{"can_admin"}
	a.ParentRelations = []ParentRelationInfo{{
		Relation:            "can_admin",
		LinkingRelation:     "org",
		AllowedLinkingTypes: []string{"organization"},
	}}

	plan := BuildCheckPlanWithOrdering(a, InlineSQLData{}, "", false, nil)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}
	if len(blocks.ParentRelationBlocks) == 0 {
		t.Fatalf("expected ParentRelationBlocks populated; got 0")
	}

	got, err := RenderExplainFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderExplainFunction: %v", err)
	}

	wants := []string{
		// v_child_trace decl is shared with implied-recursion path
		"v_child_trace JSONB",
		// FOR loop drives over linking tuples
		"FOR v_parent_link IN",
		"link.subject_type AS parent_type",
		"link.subject_id AS parent_id",
		"link.relation IN ('org')",
		"link.subject_type IN ('organization')",
		// Dispatcher call carries the parent relation and threads visited
		"explain_permission_internal(p_subject_type, p_subject_id, 'can_admin', v_parent_link.parent_type, v_parent_link.parent_id, p_visited || ARRAY[v_key])",
		// NodeTTU wrapping
		"'type', 'ttu'",
		"'via org → '",
		"' ⇒ can_admin'",
		// Failure-attempt append path
		"v_attempts := v_attempts || jsonb_build_array(jsonb_build_object('type', 'ttu'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_NoImpliedCallsSkipsDecl verifies that the
// v_child_trace local is only declared when the body actually recurses
// into a sibling explain. Otherwise we'd carry an unused JSONB variable
// in every direct-only function, triggering PG NOTICE noise.
func TestRenderExplainFunction_NoImpliedCallsSkipsDecl(t *testing.T) {
	a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true}, false)
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
	if strings.Contains(got, "v_child_trace") {
		t.Errorf("direct-only relation should not declare v_child_trace; got:\n%s", got)
	}
}
