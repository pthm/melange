package sqlgen

import (
	"strings"
	"testing"
)

// closurePlan builds a minimal list_objects ListPlan for object type "doc"
// relation "can_read" with a single complex-closure relation "reader", using
// the given analysis lookup to drive composability.
func closurePlan(lookup map[string]*RelationAnalysis) ListPlan {
	return ListPlan{
		ObjectType:          "doc",
		Relation:            "can_read",
		DatabaseSchema:      "",
		ComplexClosure:      []string{"reader"},
		AllowedSubjectTypes: []string{"user"},
		AnalysisLookup:      lookup,
	}
}

func complexClosureSQL(t *testing.T, plan ListPlan) string {
	t.Helper()
	blocks, err := buildTypedListObjectsComplexClosureBlocks(plan)
	if err != nil {
		t.Fatalf("build blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	return blocks[0].Query.SQL()
}

func TestComplexClosure_ComposesWhenComposable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive, // complex, but acyclic below
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	sql := complexClosureSQL(t, closurePlan(lookup))

	if !strings.Contains(sql, "t.object_id IN (SELECT obj.object_id FROM list_doc_reader_obj(p_subject_type, p_subject_id, NULL, NULL) obj)") {
		t.Errorf("expected set-oriented composition against list_doc_reader_obj, got:\n%s", sql)
	}
	// Userset-typed subjects keep a guarded per-candidate check for parity.
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'reader', 'doc', t.object_id") {
		t.Errorf("expected guarded per-candidate check for userset subjects, got:\n%s", sql)
	}
}

// TestComplexClosure_AdmitsUsersetQuerySubject pins the fix for issue #10: the
// complex-closure candidate arm must admit a userset-typed query subject
// (e.g. group:g2#member) whose access flows through the complex relation, not
// only plain subjects gated by AllowedSubjectTypes. Otherwise list_objects
// under-reports objects that check_permission allows for that userset subject.
func TestComplexClosure_AdmitsUsersetQuerySubject(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	sql := complexClosureSQL(t, closurePlan(lookup))

	// Plain-subject arm: type guard AND exact subject match.
	if !strings.Contains(sql, "p_subject_type IN ('user')") {
		t.Errorf("expected plain-subject type guard, got:\n%s", sql)
	}
	// Userset-subject arm: both sides must be usersets, admitted via exact
	// userset equality (OR closure). Gated by position('#'...) so it is a no-op
	// for plain subjects.
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0 AND position('#' in t.subject_id) > 0") {
		t.Errorf("expected userset query-subject candidate arm, got:\n%s", sql)
	}
	if !strings.Contains(sql, "t.subject_id = p_subject_id") {
		t.Errorf("expected exact userset equality in candidate arm, got:\n%s", sql)
	}
}

func TestComplexClosure_FallsBackWhenCyclic(t *testing.T) {
	// doc.reader reaches back into doc.can_read → composition unsafe.
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:              "doc",
			Relation:                "reader",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_read"},
		},
	}

	sql := complexClosureSQL(t, closurePlan(lookup))

	if strings.Contains(sql, "list_doc_reader_obj") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'reader', 'doc', t.object_id") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComplexClosure_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: false}, // no list function
		},
	}

	sql := complexClosureSQL(t, closurePlan(lookup))

	if strings.Contains(sql, "list_doc_reader_obj") {
		t.Errorf("non-generatable target must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

// closureSubjectsPlan builds a minimal recursive list_subjects ListPlan for
// object type "doc" relation "can_read" with a single complex-closure relation
// "reader", using the given analysis lookup to drive composability.
func closureSubjectsPlan(lookup map[string]*RelationAnalysis, self RelationFeatures) ListPlan {
	return ListPlan{
		ObjectType:          "doc",
		Relation:            "can_read",
		DatabaseSchema:      "",
		ComplexClosure:      []string{"reader"},
		AllowedSubjectTypes: []string{"user"},
		AnalysisLookup:      lookup,
		Analysis:            RelationAnalysis{ObjectType: "doc", Relation: "can_read", Features: self},
	}
}

func complexClosureSubjectsSQL(t *testing.T, plan ListPlan) string {
	t.Helper()
	blocks := buildListSubjectsRecursiveComplexClosureBlocks(plan)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	return blocks[0].Query.SQL()
}

func TestComplexClosureSubjects_ComposesWhenComposable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	sql := complexClosureSubjectsSQL(t, closureSubjectsPlan(lookup, RelationFeatures{}))

	if !strings.Contains(sql, "t.subject_id IN (SELECT sub.subject_id FROM list_doc_reader_sub(p_object_id, p_subject_type) sub)") {
		t.Errorf("expected set-oriented composition against list_doc_reader_sub, got:\n%s", sql)
	}
	if strings.Contains(sql, "check_permission_internal") {
		t.Errorf("composed block must not keep a per-candidate check, got:\n%s", sql)
	}
}

func TestComplexClosureSubjects_FallsBackWhenCyclic(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:              "doc",
			Relation:                "reader",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_read"},
		},
	}

	sql := complexClosureSubjectsSQL(t, closureSubjectsPlan(lookup, RelationFeatures{}))

	if strings.Contains(sql, "list_doc_reader_sub") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, t.subject_id, 'reader', 'doc', p_object_id") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComplexClosureSubjects_ComposesWhenTargetHasWildcard(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			Features:     RelationFeatures{HasWildcard: true},
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	sql := complexClosureSubjectsSQL(t, closureSubjectsPlan(lookup, RelationFeatures{}))

	// A wildcard-reaching target now composes: list_doc_reader_sub surfaces '*'
	// and the wildcard-completion tail verifies it, so no per-candidate check.
	if !strings.Contains(sql, "list_doc_reader_sub") {
		t.Errorf("wildcard target must compose (tail verifies '*'), got:\n%s", sql)
	}
}

func TestComplexClosureSubjects_FallsBackWhenSelfHasWildcard(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	sql := complexClosureSubjectsSQL(t, closureSubjectsPlan(lookup, RelationFeatures{HasWildcard: true}))

	if strings.Contains(sql, "list_doc_reader_sub") {
		t.Errorf("self HasWildcard must fall back to per-candidate check, got composition:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

// The recursive builder shares complexClosureMembership, so it composes too.
func TestComplexClosure_RecursiveComposes(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			ParentRelations: []ParentRelationInfo{{
				Relation: "admin", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.admin": {ObjectType: "org", Relation: "admin"},
	}

	block := buildRecursiveComplexClosureBlock(closurePlan(lookup), "reader")
	sql := block.Query.SQL()

	if !strings.Contains(sql, "list_doc_reader_obj(p_subject_type, p_subject_id, NULL, NULL)") {
		t.Errorf("expected recursive block to compose against list_doc_reader_obj, got:\n%s", sql)
	}
}
