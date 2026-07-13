package sqlgen

import (
	"strings"
	"testing"
)

// planWithIntersectionGroup builds a minimal list_objects ListPlan whose
// relation "can_edit" is an intersection of a positive part "editor" and a
// part "owner but not banned", driving buildIntersectionGroupBlock.
func planWithIntersectionGroup(lookup map[string]*RelationAnalysis) ListPlan {
	group := IntersectionGroupInfo{Parts: []IntersectionPart{
		{Relation: "editor"},
		{Relation: "owner", ExcludedRelation: "banned"},
	}}
	return ListPlan{
		DatabaseSchema: "",
		ObjectType:     "doc",
		Relation:       "can_edit",
		Analysis: RelationAnalysis{
			IntersectionGroups: []IntersectionGroupInfo{group},
		},
		AnalysisLookup: lookup,
	}
}

func groupBlockSQL(t *testing.T, plan ListPlan) string {
	t.Helper()
	block, err := buildIntersectionGroupBlock(plan, 0, plan.Analysis.IntersectionGroups[0])
	if err != nil {
		t.Fatalf("buildIntersectionGroupBlock: %v", err)
	}
	return block.Query.SQL()
}

func composableLookup() map[string]*RelationAnalysis {
	return map[string]*RelationAnalysis{
		"doc.editor": {
			ObjectType:   "doc",
			Relation:     "editor",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
		"doc.owner": {
			ObjectType:   "doc",
			Relation:     "owner",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
		"doc.banned": {
			ObjectType:   "doc",
			Relation:     "banned",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	}
}

func TestIntersectionGroup_ComposesWhenComposable(t *testing.T) {
	sql := groupBlockSQL(t, planWithIntersectionGroup(composableLookup()))

	// Positive part "editor" → semi-join against list_doc_editor_obj.
	if !strings.Contains(sql, "t.object_id IN (SELECT obj.object_id FROM list_doc_editor_obj(p_subject_type, p_subject_id, NULL, NULL) obj)") {
		t.Errorf("expected semi-join for positive part editor, got:\n%s", sql)
	}
	// Positive part keeps a userset-guarded per-candidate check for parity.
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'editor', 'doc', t.object_id") {
		t.Errorf("expected userset-guarded check for positive part, got:\n%s", sql)
	}
	// Nested exclusion "but not banned" → negated anti-join against list_doc_banned_obj.
	if !strings.Contains(sql, "NOT ((t.object_id IN (SELECT excl_obj.object_id FROM list_doc_banned_obj(") {
		t.Errorf("expected negated anti-join for excluded relation banned, got:\n%s", sql)
	}
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
}

func TestIntersectionGroup_FallsBackWhenNotComposable(t *testing.T) {
	// Targets are not list-generatable → keep per-candidate checks unchanged.
	lookup := map[string]*RelationAnalysis{
		"doc.editor": {ObjectType: "doc", Relation: "editor", Capabilities: GenerationCapabilities{ListAllowed: false}},
		"doc.banned": {ObjectType: "doc", Relation: "banned", Capabilities: GenerationCapabilities{ListAllowed: false}},
	}
	sql := groupBlockSQL(t, planWithIntersectionGroup(lookup))

	if strings.Contains(sql, "list_doc_editor_obj") || strings.Contains(sql, "list_doc_banned_obj") {
		t.Errorf("non-generatable targets must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'editor', 'doc', t.object_id") {
		t.Errorf("expected per-candidate check for positive part, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'banned', 'doc', t.object_id") {
		t.Errorf("expected per-candidate check for excluded relation, got:\n%s", sql)
	}
}

func TestIntersectionGroup_ComposesWhenTargetHasWildcard(t *testing.T) {
	// A wildcard-bearing target is complete for a plain query subject:
	// list_doc_editor_obj(subject) includes the objects the subject reaches via
	// the [user:*] grant, so the part composes rather than scanning every object.
	lookup := composableLookup()
	lookup["doc.editor"].Features.HasWildcard = true
	sql := groupBlockSQL(t, planWithIntersectionGroup(lookup))

	if !strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("wildcard target must compose, got:\n%s", sql)
	}
	if !strings.Contains(sql, "list_doc_banned_obj") {
		t.Errorf("expected non-wildcard excluded relation to still compose, got:\n%s", sql)
	}
}

func TestIntersectionGroup_ComposesWhenTargetIsRecursive(t *testing.T) {
	// Recursive list_objects is complete for plain subjects (the recursive-TTU
	// completeness work), so an intersection part composes against its
	// list_objects set — with a userset-parity check arm — instead of scanning
	// every candidate object of the type.
	lookup := composableLookup()
	lookup["doc.editor"].ListStrategy = ListStrategyRecursive
	lookup["doc.editor"].Features = RelationFeatures{HasDirect: true, HasUserset: true, HasRecursive: true}
	sql := groupBlockSQL(t, planWithIntersectionGroup(lookup))

	if !strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("recursive target must compose (list_objects is complete), got:\n%s", sql)
	}
	if !strings.Contains(sql, "list_doc_banned_obj") {
		t.Errorf("expected direct excluded relation to still compose, got:\n%s", sql)
	}
}

func TestIntersectionGroup_ComposesWhenTargetIsUserset(t *testing.T) {
	lookup := composableLookup()
	lookup["doc.editor"].ListStrategy = ListStrategyUserset
	lookup["doc.editor"].Features = RelationFeatures{HasDirect: true, HasUserset: true}
	sql := groupBlockSQL(t, planWithIntersectionGroup(lookup))

	if !strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("userset target must compose, got:\n%s", sql)
	}
	if !strings.Contains(sql, "list_doc_banned_obj") {
		t.Errorf("expected direct excluded relation to still compose, got:\n%s", sql)
	}
}

func TestIntersectionGroup_FallsBackWhenCyclic(t *testing.T) {
	// doc.editor reaches back into doc.can_edit → composition unsafe for that part;
	// doc.banned/doc.owner stay composable.
	lookup := composableLookup()
	lookup["doc.editor"] = &RelationAnalysis{
		ObjectType:              "doc",
		Relation:                "editor",
		Capabilities:            GenerationCapabilities{ListAllowed: true},
		ListStrategy:            ListStrategyRecursive,
		ComplexClosureRelations: []string{"can_edit"},
	}
	sql := groupBlockSQL(t, planWithIntersectionGroup(lookup))

	if strings.Contains(sql, "list_doc_editor_obj") {
		t.Errorf("cyclic positive part must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'editor', 'doc', t.object_id") {
		t.Errorf("expected per-candidate check for cyclic positive part, got:\n%s", sql)
	}
	// The independent excluded relation still composes.
	if !strings.Contains(sql, "list_doc_banned_obj") {
		t.Errorf("expected excluded relation banned to still compose, got:\n%s", sql)
	}
}
