package sqlgen

import (
	"strings"
	"testing"
)

// computeExplainEligibility runs a transitive sweep so a relation's
// renderer can recurse into a sibling explain_* without the dispatcher ever
// naming a function that wasn't generated. The fixed point handles three
// cases: locally supported with no deps, locally supported with all deps
// supported, and locally supported but depended on an unsupported relation
// (the wrapper must be marked ineligible too).
//
// The implied-attempt path is exercised via synthetic analyses here to pin
// the machinery; real schemas exercise it once TTU / userset / intersection
// fixtures land.

func TestComputeExplainEligibility_LocalOnly(t *testing.T) {
	blocked := mkAnalysis("doc", "blocked", RelationFeatures{HasDirect: true}, false)
	blocked.HasComplexUsersetPatterns = true
	analyses := []RelationAnalysis{
		mkAnalysis("doc", "owner", RelationFeatures{HasDirect: true}, false),
		mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasImplied: true}, false),
		blocked,
	}
	got := ComputeExplainEligibility(analyses)
	if !got["doc"]["owner"] {
		t.Errorf("owner should be eligible (Direct only)")
	}
	if !got["doc"]["viewer"] {
		t.Errorf("viewer should be eligible (Direct+Implied, no complex deps)")
	}
	if got["doc"]["blocked"] {
		t.Errorf("blocked should be ineligible (HasComplexUsersetPatterns)")
	}
}

func TestComputeExplainEligibility_TransitiveDownward(t *testing.T) {
	// wrapper depends on dep which is ineligible
	// (HasComplexUsersetPatterns — still gated). wrapper must also be
	// marked ineligible even though its own features are simple.
	dep := mkAnalysis("doc", "dep", RelationFeatures{HasDirect: true}, false)
	dep.HasComplexUsersetPatterns = true
	wrapper := mkAnalysis("doc", "wrapper", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	wrapper.ComplexClosureRelations = []string{"dep"}
	analyses := []RelationAnalysis{dep, wrapper}

	got := ComputeExplainEligibility(analyses)
	if got["doc"]["dep"] {
		t.Errorf("dep should be ineligible (HasComplexUsersetPatterns)")
	}
	if got["doc"]["wrapper"] {
		t.Errorf("wrapper should be downgraded — depends on ineligible dep")
	}
}

func TestComputeExplainEligibility_TransitiveChain(t *testing.T) {
	// Three relations in a chain where wrapper depends on middle, and
	// middle depends on bad. Bad is locally ineligible
	// (HasComplexUsersetPatterns); middle is locally fine but
	// transitively downgrades to ineligible; then in the next pass
	// wrapper itself gets downgraded.
	bad := mkAnalysis("doc", "bad", RelationFeatures{HasDirect: true}, false)
	bad.HasComplexUsersetPatterns = true
	middle := mkAnalysis("doc", "middle", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	middle.ComplexClosureRelations = []string{"bad"}
	wrapper := mkAnalysis("doc", "wrapper", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	wrapper.ComplexClosureRelations = []string{"middle"}
	analyses := []RelationAnalysis{bad, middle, wrapper}

	got := ComputeExplainEligibility(analyses)
	if got["doc"]["bad"] {
		t.Errorf("bad should be ineligible (HasComplexUsersetPatterns)")
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
	folderAdmin := mkAnalysis("folder", "can_admin", RelationFeatures{HasDirect: true}, false)
	folderAdmin.HasComplexUsersetPatterns = true
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
		t.Errorf("folder.can_admin should be ineligible (HasComplexUsersetPatterns)")
	}
	if got["repository"]["can_admin"] {
		t.Errorf("repository.can_admin should be ineligible — folder parent dragged it down")
	}
}

func TestComputeExplainEligibility_UsersetPattern(t *testing.T) {
	// document.viewer: [group#member] — wrapper depends on group.member.
	// When group.member is eligible the wrapper is too; flipping group.member
	// (e.g., HasComplexUsersetPatterns) cascades to the wrapper.
	groupMember := mkAnalysis("group", "member", RelationFeatures{HasDirect: true}, false)
	docViewer := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	docViewer.UsersetPatterns = []UsersetPattern{{
		SubjectType:     "group",
		SubjectRelation: "member",
	}}
	got := ComputeExplainEligibility([]RelationAnalysis{groupMember, docViewer})
	if !got["document"]["viewer"] {
		t.Errorf("document.viewer should be eligible — group.member is eligible")
	}

	// Now flip group.member to ineligible and verify the cascade.
	groupMember2 := mkAnalysis("group", "member", RelationFeatures{HasDirect: true}, false)
	groupMember2.HasComplexUsersetPatterns = true
	got = ComputeExplainEligibility([]RelationAnalysis{groupMember2, docViewer})
	if got["group"]["member"] {
		t.Errorf("group.member should be ineligible (HasComplexUsersetPatterns)")
	}
	if got["document"]["viewer"] {
		t.Errorf("document.viewer should be downgraded once group.member flipped")
	}
}

func TestComputeExplainEligibility_PerObjectTypeIsolation(t *testing.T) {
	// ComplexClosureRelations names are scoped to the wrapping relation's
	// object type. A "viewer" on document and a "viewer" on folder are
	// distinct entries; downgrading one must not affect the other.
	docViewer := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, false)
	folderViewer := mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true}, false)
	folderViewer.HasComplexUsersetPatterns = true
	analyses := []RelationAnalysis{docViewer, folderViewer}

	got := ComputeExplainEligibility(analyses)
	if !got["document"]["viewer"] {
		t.Errorf("document.viewer should not be touched by folder.viewer's ineligibility")
	}
	if got["folder"]["viewer"] {
		t.Errorf("folder.viewer should be ineligible (HasComplexUsersetPatterns)")
	}
}

// TestRenderExplainFunction_ImpliedFunctionCallEmits hand-builds an analysis
// with a ComplexClosureRelation entry so the implied-attempt block actually
// renders. Pins the SQL shape: assignment of v_child_trace, the
// (result->>'result')::boolean check, NodeImplied wrapping the child's root,
// and the failure-attempt append.
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
		// so a NULL return is normalised to an empty object (a malformed
		// callee surfaces as a failure attempt, not a null-children
		// NodeImplied).
		"v_child_trace := COALESCE(explain_doc_editor(p_subject_type, p_subject_id, p_object_id, p_visited || ARRAY[v_key], p_max_nodes), '{}'::jsonb)",
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

// TestRenderExplainFunction_TTUFlow exercises the TTU FOR-loop emission.
// The fixture is a TTU relation that recurses into the dispatcher
// (explain_permission_internal) for the parent's relation.
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
		"explain_permission_internal(p_subject_type, p_subject_id, 'can_admin', v_parent_link.parent_type, v_parent_link.parent_id, p_visited || ARRAY[v_key], p_max_nodes)",
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

// TestRenderExplainFunction_UsersetFlow exercises the FOR-loop emission for
// userset references ([group#member]). The fixture declares one userset
// pattern; the renderer must emit one FOR loop over grant tuples whose
// subject is a userset reference.
func TestRenderExplainFunction_UsersetFlow(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	a.SatisfyingRelations = []string{"viewer"}
	a.UsersetPatterns = []UsersetPattern{{
		SubjectType:         "group",
		SubjectRelation:     "member",
		SatisfyingRelations: []string{"member"},
	}}

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
		// New record decl + shared child trace decl
		"v_userset_grant RECORD",
		"v_child_trace JSONB",
		// FOR-loop driver projects the parent object id from the userset reference
		"FOR v_userset_grant IN",
		"split_part(grant_tuple.subject_id, '#', 1) AS group_id",
		// Filters: grant subject type matches, subject_id is a userset ref,
		// suffix matches the pattern's SubjectRelation
		"grant_tuple.subject_type = 'group'",
		"position('#' in grant_tuple.subject_id) > 0",
		"split_part(grant_tuple.subject_id, '#', 2) = 'member'",
		// Dispatcher call recurses into the membership relation
		"explain_permission_internal(p_subject_type, p_subject_id, 'member', 'group', v_userset_grant.group_id, p_visited || ARRAY[v_key], p_max_nodes)",
		// NodeUserset wrap with informative label
		"'type', 'userset'",
		"'via [group#member] → group:'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_UsersetSubjectPreCheck pins the renderer's
// pre-check for cases where the SUBJECT being checked is itself a userset
// reference. The block must guard on `position('#' in p_subject_id) > 0`,
// fire both the self-referential check and the exact-grant lookup, and
// return successful traces wrapped in NodeUserset before falling through
// to the regular Direct/Userset attempt flow.
func TestRenderExplainFunction_UsersetSubjectPreCheck(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true, HasUserset: true}, false)
	a.SatisfyingRelations = []string{"viewer"}
	a.UsersetPatterns = []UsersetPattern{{
		SubjectType:         "group",
		SubjectRelation:     "member",
		SatisfyingRelations: []string{"member"},
	}}

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
		// Outer guard
		"position('#' in p_subject_id) > 0",
		// v_userset_check now shared with check's pre-check SELECTs
		"v_userset_check INTEGER",
		// Case 1: self-referential — outer condition + SELECT INTO from the
		// shared blocks.UsersetSubjectSelfCheck SelectStmt so we agree with
		// Check on closure handling.
		"-- Case 1: self-referential userset",
		"p_subject_type = 'document'",
		"SELECT INTO v_userset_check",
		"'label', 'self-referential userset matches relation closure'",
		// Case 2: closure-aware computed userset — reuses
		// blocks.UsersetSubjectComputedCheck so closure-mismatched
		// usersets (e.g., `group:eng#admin` against a `[group#member]`
		// grant when admin→member) succeed as Check would.
		"-- Case 2: closure-aware computed userset match",
		"'label', 'userset subject matched via closure'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}

	a.UsersetPatterns = nil
	plan = BuildCheckPlanWithOrdering(a, InlineSQLData{}, "", false, nil)
	blocks, err = BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks without userset patterns: %v", err)
	}
	got, err = RenderExplainFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderExplainFunction without userset patterns: %v", err)
	}
	if strings.Contains(got, "-- Case 2: closure-aware computed userset match") {
		t.Errorf("relation without userset patterns should not emit computed-userset case:\n%s", got)
	}
}

// TestRenderExplainFunction_IntersectionFlow exercises the intersection
// attempt block: each part recursively calls explain_permission_internal,
// results AND-aggregate, and the trace wraps part roots in NodeIntersection.
func TestRenderExplainFunction_IntersectionFlow(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true, HasIntersection: true}, false)
	a.SatisfyingRelations = []string{"viewer"}
	a.IntersectionGroups = []IntersectionGroupInfo{{
		Parts: []IntersectionPart{
			{Relation: "writer"},
			{Relation: "editor"},
		},
	}}
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
		"v_intersection_children JSONB",
		"v_intersection_pass BOOLEAN",
		"v_intersection_children := '[]'::jsonb",
		"v_intersection_pass := TRUE",
		"-- Intersection part: writer",
		"-- Intersection part: editor",
		"explain_permission_internal(p_subject_type, p_subject_id, 'writer', 'document', p_object_id, p_visited || ARRAY[v_key], p_max_nodes)",
		"explain_permission_internal(p_subject_type, p_subject_id, 'editor', 'document', p_object_id, p_visited || ARRAY[v_key], p_max_nodes)",
		"v_intersection_children := v_intersection_children || jsonb_build_array(v_child_trace->'root')",
		"v_intersection_pass := FALSE",
		"'type', 'intersection'",
		"'label', 'intersection group 1 (all parts must hold)'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_ExclusionWrapsSuccess pins the success-return
// helper's exclusion wrapping: every success path checks the exclusion
// predicate via blocks.ExclusionCheck and wraps the outcome in
// NodeExclusion (either result=true on pass, or result=false appended to
// v_attempts on excluded).
func TestRenderExplainFunction_ExclusionWrapsSuccess(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true, HasExclusion: true}, false)
	a.SatisfyingRelations = []string{"viewer"}
	a.ExcludedRelations = []string{"banned"}
	a.SimpleExcludedRelations = []string{"banned"}
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
		// The exclusion check is interposed at the success path
		"-- Exclusion fired — record failure attempt and continue",
		"'type', 'exclusion'",
		"'label', 'excluded — base satisfied but exclusion fired'",
		"'label', 'base satisfied; exclusion did not fire'",
		// blocks.ExclusionCheck for a simple exclusion is an EXISTS over excluded tuples
		"FROM melange_tuples AS excl",
		"excl.relation IN ('banned')",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_WildcardEmission pins the wildcard branch in
// the direct attempt: the success node is a CASE expression picking
// NodeWildcard when v_evidence_tuple.subject_id = '*' and NodeDirect
// otherwise.
func TestRenderExplainFunction_WildcardEmission(t *testing.T) {
	a := mkAnalysis("document", "banned", RelationFeatures{HasDirect: true, HasWildcard: true}, false)
	a.SatisfyingRelations = []string{"banned"}

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
		// CASE picks wildcard sentinel when the evidence carries '*'
		"CASE WHEN v_evidence_tuple.subject_id = '*' THEN",
		"'type', 'wildcard'",
		"'type', v_evidence_tuple.subject_type",
		"'id', '*'",
		// ELSE falls through to the standard direct node
		"'type', 'direct'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExplainFunction_TruncationDeclsAndCheck pins the truncation
// infrastructure: the function signature carries p_max_nodes, the body
// declares v_max_nodes resolving to COALESCE(p_max_nodes, GUC, 100),
// v_truncated tracks the bail flag, and the truncation check fires after
// each recursive accumulation.
func TestRenderExplainFunction_TruncationDeclsAndCheck(t *testing.T) {
	a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	a.SatisfyingRelations = []string{"viewer", "editor"}
	a.ComplexClosureRelations = []string{"editor"}
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
		// Signature
		"p_max_nodes INTEGER DEFAULT NULL",
		// Decls
		"v_max_nodes INTEGER := COALESCE(p_max_nodes, current_setting('melange.max_explain_nodes', true)::INTEGER, 100)",
		"v_truncated BOOLEAN := FALSE",
		// Top-of-body truncation guard
		"IF v_node_count >= v_max_nodes THEN",
		"'type', 'truncated'",
		"v_truncated := TRUE",
		// Per-call truncation check after accumulation
		"v_node_count := v_node_count + COALESCE((v_child_trace->>'node_count')::INTEGER, 0)",
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
