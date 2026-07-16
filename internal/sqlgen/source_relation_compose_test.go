package sqlgen

import (
	"strings"
	"testing"
)

// sourceRelationPlan builds a list_objects ListPlan for object type "doc",
// relation "can_read", with a lookup that makes the source relation "editor"
// composable or not depending on the supplied lookup.
func sourceRelationPlan(lookup map[string]*RelationAnalysis) ListPlan {
	return ListPlan{
		DatabaseSchema: "",
		ObjectType:     "doc",
		Relation:       "can_read",
		AnalysisLookup: lookup,
	}
}

var sourceParent = ListParentRelationData{
	SourceRelation:   "editor",
	IsClosurePattern: true,
}

func TestSourceRelationCheck_SemiJoinWhenComposable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.editor": {
			ObjectType:   "doc",
			Relation:     "editor",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive, // complex, but acyclic below
			ParentRelations: []ParentRelationInfo{{
				Relation: "editor", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.editor": {ObjectType: "org", Relation: "editor"},
	}

	sql := sourceRelationCheck(sourceRelationPlan(lookup), sourceParent).SQL()

	if !strings.Contains(sql, "child.object_id IN (SELECT src_obj.object_id FROM list_doc_editor_obj(p_subject_type, p_subject_id, NULL, NULL) src_obj)") {
		t.Errorf("expected set-oriented semi-join against list_doc_editor_obj, got:\n%s", sql)
	}
	// Userset-typed subjects keep a guarded per-candidate check for parity.
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'editor', 'doc', child.object_id") {
		t.Errorf("expected guarded per-candidate check for userset subjects, got:\n%s", sql)
	}
}

func TestSourceRelationCheck_FallsBackWhenCyclic(t *testing.T) {
	// doc.editor reaches back into doc.can_read → composition unsafe.
	lookup := map[string]*RelationAnalysis{
		"doc.editor": {
			ObjectType:              "doc",
			Relation:                "editor",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_read"},
		},
	}

	sql := sourceRelationCheck(sourceRelationPlan(lookup), sourceParent).SQL()

	if strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'editor', 'doc', child.object_id") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestSourceRelationCheck_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.editor": {
			ObjectType:   "doc",
			Relation:     "editor",
			Capabilities: GenerationCapabilities{ListAllowed: false}, // no list function
		},
	}

	sql := sourceRelationCheck(sourceRelationPlan(lookup), sourceParent).SQL()

	if strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("non-generatable target must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestSourceRelationCheck_FallsBackWhenNoLookup(t *testing.T) {
	sql := sourceRelationCheck(sourceRelationPlan(nil), sourceParent).SQL()

	if strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("nil lookup must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}
