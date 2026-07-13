package sqlgen

import (
	"strings"
	"testing"
)

// subjectFirstPlan builds a list_subjects ListPlan for object type "folder",
// relation "viewer", whose TTU parent (parentRel on parentType) forces the
// subject_pool strategy, with the given lookup for composition gating.
func subjectFirstPlan(lookup map[string]*RelationAnalysis) (ListPlan, ListParentRelationData) {
	plan := ListPlan{
		ObjectType: "folder",
		Relation:   "viewer",
		Analysis: RelationAnalysis{
			ObjectType: "folder",
			Relation:   "viewer",
		},
		AnalysisLookup: lookup,
	}
	parent := ListParentRelationData{
		Relation:                 "admin",
		LinkingRelation:          "org",
		AllowedLinkingTypesSlice: []string{"org"},
	}
	return plan, parent
}

func composableOrgAdmin() map[string]*RelationAnalysis {
	return map[string]*RelationAnalysis{
		// org.admin is complex (exclusion → forces subject_pool) but does not
		// reference folder.viewer, so composition is safe.
		"org.admin": {
			ObjectType:              "org",
			Relation:                "admin",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyDirect,
			Features:                RelationFeatures{HasExclusion: true},
			SimpleExcludedRelations: []string{"blocked"},
		},
		"org.blocked": {ObjectType: "org", Relation: "blocked"},
	}
}

func TestSubjectFirstTTU_ComposesWhenSafe(t *testing.T) {
	plan, parent := subjectFirstPlan(composableOrgAdmin())

	blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 subject-first block, got %d", len(blocks))
	}
	sql := blocks[0].Query.SQL()
	if !strings.Contains(sql, "CROSS JOIN LATERAL list_org_admin_sub(link.subject_id, p_subject_type) AS sub") {
		t.Errorf("expected LATERAL compose with list_org_admin_sub, got:\n%s", sql)
	}
	if strings.Contains(sql, "check_permission_internal") {
		t.Errorf("subject-first block must not call check_permission_internal:\n%s", sql)
	}
	if strings.Contains(sql, "subject_pool") {
		t.Errorf("subject-first block must not reference subject_pool:\n%s", sql)
	}
}

func TestSubjectFirstTTU_FallsBackWhenCyclic(t *testing.T) {
	lookup := composableOrgAdmin()
	// org.admin reaches back into folder.viewer → composition unsafe.
	lookup["org.admin"].ParentRelations = []ParentRelationInfo{{
		Relation: "viewer", LinkingRelation: "link", AllowedLinkingTypes: []string{"folder"},
	}}
	plan, parent := subjectFirstPlan(lookup)

	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks != nil {
		t.Errorf("cyclic composition must fall back to subject_pool, got %d blocks", len(blocks))
	}
}

func TestSubjectFirstTTU_ComposesWhenTargetHasWildcard(t *testing.T) {
	lookup := composableOrgAdmin()
	lookup["org.admin"].Features.HasWildcard = true
	plan, parent := subjectFirstPlan(lookup)

	// A wildcard-reaching target now composes: the '*' its list_subjects surfaces
	// flows into base_results and is verified by the wildcard-completion tail
	// (concrete subjects reachable only via '*' are represented by '*').
	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks == nil {
		t.Errorf("wildcard target must compose (tail verifies '*'), got subject_pool fallback")
	}
}

func TestSubjectFirstTTU_FallsBackForClosurePattern(t *testing.T) {
	plan, parent := subjectFirstPlan(composableOrgAdmin())
	parent.IsClosurePattern = true

	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks != nil {
		t.Errorf("closure pattern must fall back to subject_pool")
	}
}

func TestSubjectFirstTTU_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := composableOrgAdmin()
	lookup["org.admin"].Capabilities.ListAllowed = false
	plan, parent := subjectFirstPlan(lookup)

	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks != nil {
		t.Errorf("non-generatable target must fall back to subject_pool")
	}
}

// TestSubjectFirstTTU_ComposesWhenTargetReachesWildcardTransitively: org.admin
// has HasWildcard=false itself but reaches org2.viewer=[user:*] via a TTU parent,
// so list_org_admin_sub surfaces '*'. That '*' flows into base_results and is
// verified by the wildcard-completion tail, so composition is safe — subjects
// reachable only via the wildcard are represented by '*' (OpenFGA semantics).
func TestSubjectFirstTTU_ComposesWhenTargetReachesWildcardTransitively(t *testing.T) {
	lookup := composableOrgAdmin()
	lookup["org.admin"].ParentRelations = []ParentRelationInfo{{
		Relation: "viewer", LinkingRelation: "parent", AllowedLinkingTypes: []string{"org2"},
	}}
	lookup["org2.viewer"] = &RelationAnalysis{
		ObjectType: "org2", Relation: "viewer",
		Features: RelationFeatures{HasWildcard: true},
	}
	plan, parent := subjectFirstPlan(lookup)

	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks == nil {
		t.Errorf("transitively-wildcard-reaching target must compose (tail verifies '*')")
	}
}

// TestReachesWildcard_Transitive checks reachesWildcard walks TTU/closure edges.
func TestReachesWildcard_Transitive(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"folder.viewer": {
			ObjectType: "folder", Relation: "viewer",
			Features:        RelationFeatures{}, // HasWildcard false
			ParentRelations: []ParentRelationInfo{{Relation: "viewer", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"}}},
		},
		"org.viewer": {ObjectType: "org", Relation: "viewer", Features: RelationFeatures{HasWildcard: true}},
	}
	if !reachesWildcard(lookup, "folder", "viewer") {
		t.Error("folder.viewer must reach wildcard via org.viewer TTU parent")
	}
	if reachesWildcard(lookup, "org", "viewer") == false {
		t.Error("org.viewer HasWildcard directly")
	}
	delete(lookup, "org.viewer")
	if reachesWildcard(lookup, "folder", "viewer") {
		t.Error("no wildcard reachable once org.viewer removed")
	}
}

// TestComposableListSubjectsTarget pins the extracted subject-first gate (the
// single predicate now shared by complex-closure, complex-userset and TTU
// subject-first composition sites): composable when the target is
// list-generatable and cycle/DepthExceeded-safe. Wildcard-reachable targets ARE
// composable — the '*' their list_subjects surfaces is verified by the
// wildcard-completion tail.
func TestComposableListSubjectsTarget(t *testing.T) {
	plan, _ := subjectFirstPlan(composableOrgAdmin())
	if !composableListSubjectsTarget(plan, "org", "admin") {
		t.Fatal("safe, generatable target must be composable")
	}

	// Wildcard-reachable target: now composable — list_org_admin_sub surfaces '*'
	// and the wildcard-completion tail verifies it against the full relation.
	wc := composableOrgAdmin()
	wc["org.admin"].Features.HasWildcard = true
	planWC, _ := subjectFirstPlan(wc)
	if !composableListSubjectsTarget(planWC, "org", "admin") {
		t.Error("wildcard-reachable target must now be composable")
	}

	// Cyclic: target references back into the caller relation → unsafe.
	cyc := composableOrgAdmin()
	cyc["org.admin"].ParentRelations = []ParentRelationInfo{{
		Relation: "viewer", LinkingRelation: "link", AllowedLinkingTypes: []string{"folder"},
	}}
	planCyc, _ := subjectFirstPlan(cyc)
	if composableListSubjectsTarget(planCyc, "org", "admin") {
		t.Error("cyclic target must not be composable")
	}

	// Not list-generatable → unsafe.
	ng := composableOrgAdmin()
	ng["org.admin"].Capabilities.ListAllowed = false
	planNG, _ := subjectFirstPlan(ng)
	if composableListSubjectsTarget(planNG, "org", "admin") {
		t.Error("non-generatable target must not be composable")
	}
}
