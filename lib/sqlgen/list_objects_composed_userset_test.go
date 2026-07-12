package sqlgen

import (
	"strings"
	"testing"
)

// composedUsersetPlan builds a minimal list_objects plan whose indirect anchor
// is a userset composition ([group#member] granted on doc). The AnalysisLookup
// entry for group.member (the target) controls whether composition is safe.
func composedUsersetPlan(lookup map[string]*RelationAnalysis) ListPlan {
	return ListPlan{
		ObjectType:     "doc",
		Relation:       "can_view",
		DatabaseSchema: "",
		RelationList:   []string{"viewer"},
		AnalysisLookup: lookup,
	}
}

func composedUsersetStep() AnchorPathStep {
	return AnchorPathStep{
		Type:            "userset",
		SubjectType:     "group",
		SubjectRelation: "member",
	}
}

func composedUsersetSQL(t *testing.T, lookup map[string]*RelationAnalysis) string {
	t.Helper()
	block, err := buildComposedUsersetObjectsBlock(composedUsersetPlan(lookup), composedUsersetStep(), ExclusionConfig{})
	if err != nil {
		t.Fatalf("buildComposedUsersetObjectsBlock: %v", err)
	}
	return block.Query.SQL()
}

func TestComposedUserset_SemiJoinWhenComposable(t *testing.T) {
	// group.member is a plain acyclic target that does not reach back into
	// doc.can_view, so composition is safe.
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	}

	sql := composedUsersetSQL(t, lookup)

	if !strings.Contains(sql, "split_part(t.subject_id, '#', 1) IN (SELECT obj.object_id FROM list_group_member_obj(p_subject_type, p_subject_id, NULL, NULL) obj)") {
		t.Errorf("expected set-oriented semi-join against list_group_member_obj, got:\n%s", sql)
	}
	// Userset-typed query subjects keep a guarded per-candidate check for parity;
	// plain subjects skip the check (the removed fan-out).
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'member', 'group', split_part(t.subject_id, '#', 1)") {
		t.Errorf("expected guarded per-candidate check for userset subjects, got:\n%s", sql)
	}
}

func TestComposedUserset_FallsBackWhenCyclic(t *testing.T) {
	// group.member reaches back into doc.can_view → composition unsafe.
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:              "group",
			Relation:                "member",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_view"}, // doc.can_view unreachable by name, but exercise cyclic path via same relation name
		},
	}
	// Make the cycle real: target reaches doc.can_view.
	lookup["group.member"].ParentRelations = []ParentRelationInfo{{
		Relation: "can_view", LinkingRelation: "in", AllowedLinkingTypes: []string{"doc"},
	}}

	sql := composedUsersetSQL(t, lookup)

	if strings.Contains(sql, "list_group_member_obj") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'member', 'group', split_part(t.subject_id, '#', 1)") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComposedUserset_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: false}, // no list function
		},
	}

	sql := composedUsersetSQL(t, lookup)

	if strings.Contains(sql, "list_group_member_obj") {
		t.Errorf("non-generatable target must fall back to per-candidate check, got semi-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}
