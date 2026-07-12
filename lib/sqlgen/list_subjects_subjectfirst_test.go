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

func TestSubjectFirstTTU_FallsBackWhenTargetHasWildcard(t *testing.T) {
	lookup := composableOrgAdmin()
	lookup["org.admin"].Features.HasWildcard = true
	plan, parent := subjectFirstPlan(lookup)

	if blocks := buildSubjectFirstTTUSubjectBlocks(plan, parent); blocks != nil {
		t.Errorf("wildcard target must fall back to subject_pool (subject-side '*' handling)")
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
