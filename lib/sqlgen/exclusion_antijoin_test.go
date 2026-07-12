package sqlgen

import (
	"strings"
	"testing"
)

// exclusionWithCompose builds a list_objects-style ExclusionConfig for a single
// complex excluded relation, with the composition context enabled.
func exclusionWithCompose(lookup map[string]*RelationAnalysis) ExclusionConfig {
	return ExclusionConfig{
		DatabaseSchema:           "",
		ObjectType:               "doc",
		ObjectIDExpr:             Col{Table: "t", Column: "object_id"},
		SubjectTypeExpr:          SubjectType,
		SubjectIDExpr:            SubjectID,
		ComplexExcludedRelations: []string{"blocked"},
		Compose: &exclusionCompose{
			Lookup:   lookup,
			FromType: "doc",
			FromRel:  "can_read",
		},
	}
}

func predicatesSQL(c ExclusionConfig) string {
	var b strings.Builder
	for _, p := range c.BuildPredicates() {
		b.WriteString(p.SQL())
		b.WriteString("\n")
	}
	return b.String()
}

func TestComplexExclusion_AntiJoinWhenComposable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.blocked": {
			ObjectType:   "doc",
			Relation:     "blocked",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive, // complex, but acyclic below
			ParentRelations: []ParentRelationInfo{{
				Relation: "blocked", LinkingRelation: "org", AllowedLinkingTypes: []string{"org"},
			}},
		},
		"org.blocked": {ObjectType: "org", Relation: "blocked"},
	}

	sql := predicatesSQL(exclusionWithCompose(lookup))

	if !strings.Contains(sql, "t.object_id IN (SELECT excl_obj.object_id FROM list_doc_blocked_obj(p_subject_type, p_subject_id, NULL, NULL) excl_obj)") {
		t.Errorf("expected set-oriented anti-join against list_doc_blocked_obj, got:\n%s", sql)
	}
	// Userset-typed subjects keep a guarded per-candidate check for parity.
	if !strings.Contains(sql, "position('#' in p_subject_id) > 0") {
		t.Errorf("expected userset-subject parity guard, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'blocked', 'doc', t.object_id") {
		t.Errorf("expected guarded per-candidate check for userset subjects, got:\n%s", sql)
	}
	// The whole membership is negated: NOT ( ... IN ... OR ... ).
	if !strings.Contains(sql, "NOT ((t.object_id IN") {
		t.Errorf("expected negated membership, got:\n%s", sql)
	}
}

func TestComplexExclusion_FallsBackWhenCyclic(t *testing.T) {
	// doc.blocked reaches back into doc.can_read → composition unsafe.
	lookup := map[string]*RelationAnalysis{
		"doc.blocked": {
			ObjectType:              "doc",
			Relation:                "blocked",
			Capabilities:            GenerationCapabilities{ListAllowed: true},
			ListStrategy:            ListStrategyRecursive,
			ComplexClosureRelations: []string{"can_read"},
		},
	}

	sql := predicatesSQL(exclusionWithCompose(lookup))

	if strings.Contains(sql, "list_doc_blocked_obj") {
		t.Errorf("cyclic composition must fall back to per-candidate check, got anti-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, p_subject_id, 'blocked', 'doc', t.object_id") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComplexExclusion_FallsBackWhenNotListGeneratable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.blocked": {
			ObjectType:   "doc",
			Relation:     "blocked",
			Capabilities: GenerationCapabilities{ListAllowed: false}, // no list function
		},
	}

	sql := predicatesSQL(exclusionWithCompose(lookup))

	if strings.Contains(sql, "list_doc_blocked_obj") {
		t.Errorf("non-generatable target must fall back to per-candidate check, got anti-join:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check fallback, got:\n%s", sql)
	}
}

func TestComplexExclusion_CheckContextKeepsPerCandidate(t *testing.T) {
	// No Compose (check / list_subjects context) → always per-candidate check,
	// even with a composable lookup.
	c := exclusionWithCompose(map[string]*RelationAnalysis{
		"doc.blocked": {
			ObjectType:   "doc",
			Relation:     "blocked",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyDirect,
		},
	})
	c.Compose = nil

	sql := predicatesSQL(c)

	if strings.Contains(sql, "list_doc_blocked_obj") {
		t.Errorf("check/list_subjects context (Compose nil) must not emit anti-join, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal") {
		t.Errorf("expected per-candidate check, got:\n%s", sql)
	}
}

// TestBuildExclusionCTEQualifiesSubjectID guards issue #11: the excluded_subjects
// CTE must qualify subject_id with the tuples alias. An unqualified subject_id
// collides with the enclosing list_*_sub function's OUT parameter of the same
// name, raising "column reference subject_id is ambiguous" at query time.
func TestBuildExclusionCTEQualifiesSubjectID(t *testing.T) {
	c := ExclusionConfig{
		ObjectType:              "org",
		ObjectIDExpr:            Param("p_object_id"),
		SimpleExcludedRelations: []string{"blocked"},
	}

	sql := c.BuildExclusionCTE()

	if !strings.Contains(sql, "e.subject_id") {
		t.Errorf("exclusion CTE must qualify subject_id (e.subject_id), got:\n%s", sql)
	}
	if strings.Contains(sql, "SELECT subject_id") {
		t.Errorf("exclusion CTE must not select unqualified subject_id, got:\n%s", sql)
	}
}
