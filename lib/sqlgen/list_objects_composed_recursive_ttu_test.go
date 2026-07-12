package sqlgen

import (
	"strings"
	"testing"
)

// composedRecursiveTTUPlan builds a minimal list_objects plan for a pure-TTU
// relation (folder.viewer: viewer from parent) reaching a recursive same-type
// anchor. The AnalysisLookup entry for the parent (target) relation controls
// whether the recursive TTU composition is safe.
func composedRecursiveTTUPlan(lookup map[string]*RelationAnalysis) ListPlan {
	return ListPlan{
		ObjectType:     "folder",
		Relation:       "can_view",
		DatabaseSchema: "",
		AnalysisLookup: lookup,
	}
}

func composedRecursiveTTUAnchor() *IndirectAnchorInfo {
	return &IndirectAnchorInfo{
		AnchorType:     "folder",
		AnchorRelation: "viewer",
		Path: []AnchorPathStep{{
			Type:            "ttu",
			LinkingRelation: "parent",
			TargetRelation:  "viewer",
			RecursiveTypes:  []string{"folder"},
		}},
	}
}

func composedRecursiveTTUSQL(t *testing.T, lookup map[string]*RelationAnalysis) string {
	t.Helper()
	plan := composedRecursiveTTUPlan(lookup)
	anchor := composedRecursiveTTUAnchor()
	block, err := buildComposedRecursiveTTUObjectsBlock(plan, anchor, "folder", ExclusionConfig{})
	if err != nil {
		t.Fatalf("buildComposedRecursiveTTUObjectsBlock: %v", err)
	}
	return block.Query.SQL()
}

func TestComposedRecursiveTTU_SemiJoinWhenComposable(t *testing.T) {
	// folder.viewer is a plain acyclic target that does not reach back into
	// folder.can_view, so composition is safe.
	lookup := map[string]*RelationAnalysis{
		"folder.viewer": {
			ObjectType:   "folder",
			Relation:     "viewer",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	}

	sql := composedRecursiveTTUSQL(t, lookup)

	if !strings.Contains(sql, "t.subject_id IN (SELECT obj.object_id FROM list_folder_viewer_obj(p_subject_type, p_subject_id, NULL, NULL) obj)") {
		t.Errorf("expected set-oriented semi-join against list_folder_viewer_obj, got:\n%s", sql)
	}
	// Userset-typed subjects keep a guarded per-candidate check for parity.
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'folder', t.subject_id") {
		t.Errorf("expected guarded per-candidate check for userset subjects, got:\n%s", sql)
	}
}

func TestComposedRecursiveTTU_FallsBackWhenCyclic(t *testing.T) {
	// folder.viewer reaches back into folder.can_view → composition unsafe
	// (the classic self-referential recursive parent chain).
	lookup := map[string]*RelationAnalysis{
		"folder.viewer": {
			ObjectType:              "folder",
			Relation:                "viewer",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_view"},
		},
	}

	sql := composedRecursiveTTUSQL(t, lookup)

	if strings.Contains(sql, "list_folder_viewer_obj") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'viewer', 'folder', t.subject_id") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComposedRecursiveTTU_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"folder.viewer": {
			ObjectType:   "folder",
			Relation:     "viewer",
			Capabilities: GenerationCapabilities{ListAllowed: false}, // no list function
		},
	}

	sql := composedRecursiveTTUSQL(t, lookup)

	if strings.Contains(sql, "list_folder_viewer_obj") {
		t.Errorf("non-generatable target must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}
