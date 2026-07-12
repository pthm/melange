package sqlgen

import (
	"strings"
	"testing"
)

// The complex-userset list_subjects block should compose with the target
// relation's list_subjects function (CROSS JOIN LATERAL list_..._sub) instead
// of joining melange_tuples members and calling check_permission_internal per
// member, when the target is list-generatable, cycle-safe, and wildcard-free.

func subjectFirstUsersetPlan(lookup map[string]*RelationAnalysis) (ListPlan, listUsersetPatternInput) {
	plan := ListPlan{
		ObjectType:     "org",
		Relation:       "view",
		DatabaseSchema: "",
		Analysis: RelationAnalysis{
			ObjectType:          "org",
			Relation:            "view",
			AllowedSubjectTypes: []string{"user"},
		},
		AnalysisLookup: lookup,
	}
	pattern := listUsersetPatternInput{
		SubjectType:     "group",
		SubjectRelation: "member",
		SourceRelations: []string{"view"},
		IsComplex:       true,
	}
	return plan, pattern
}

func TestComplexUsersetSubjects_ComposesWhenSafe(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	}
	plan, pattern := subjectFirstUsersetPlan(lookup)

	sql := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()

	if !strings.Contains(sql, "CROSS JOIN LATERAL list_group_member_sub(split_part(g.subject_id, '#', 1), p_subject_type)") {
		t.Errorf("expected lateral compose against list_group_member_sub, got:\n%s", sql)
	}
	if strings.Contains(sql, "check_permission_internal") {
		t.Errorf("composed block must not emit per-member check_permission_internal, got:\n%s", sql)
	}
}

func TestComplexUsersetSubjects_FallsBackWhenCyclic(t *testing.T) {
	// group.member reaches back into org.view -> composition unsafe.
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			ParentRelations: []ParentRelationInfo{{
				Relation: "view", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
	}
	plan, pattern := subjectFirstUsersetPlan(lookup)

	sql := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()

	if strings.Contains(sql, "list_group_member_sub") {
		t.Errorf("cyclic composition must fall back to per-member check, got lateral:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-member check fallback, got:\n%s", sql)
	}
}

func TestComplexUsersetSubjects_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: false},
		},
	}
	plan, pattern := subjectFirstUsersetPlan(lookup)

	sql := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()

	if strings.Contains(sql, "list_group_member_sub") {
		t.Errorf("non-generatable target must fall back to per-member check, got lateral:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-member check fallback, got:\n%s", sql)
	}
}

func TestComplexUsersetSubjects_FallsBackWhenTargetHasWildcard(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
			Features:     RelationFeatures{HasWildcard: true},
		},
	}
	plan, pattern := subjectFirstUsersetPlan(lookup)

	sql := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()

	if strings.Contains(sql, "list_group_member_sub") {
		t.Errorf("wildcard target must fall back to per-member check, got lateral:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-member check fallback, got:\n%s", sql)
	}
}

func TestComplexUsersetSubjects_FallsBackWhenPlanHasWildcard(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"group.member": {
			ObjectType:   "group",
			Relation:     "member",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	}
	plan, pattern := subjectFirstUsersetPlan(lookup)
	plan.Analysis.Features.HasWildcard = true

	sql := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()

	if strings.Contains(sql, "list_group_member_sub") {
		t.Errorf("wildcard plan must fall back to per-member check, got lateral:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-member check fallback, got:\n%s", sql)
	}
}
