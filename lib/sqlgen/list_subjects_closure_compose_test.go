package sqlgen

import (
	"strings"
	"testing"
)

// closureTTUPlan builds a minimal list_subjects ListPlan whose relation
// (doc.can_read) has an implied TTU that arrives as a closure pattern from the
// source relation reader ("reader: repo_admin from owner"). p_object_id is the
// target object; the closure-pattern subject_pool block checks reader on the
// SAME target object per subject, so it reduces to list_doc_reader_sub.
func closureTTUPlan(lookup map[string]*RelationAnalysis) (ListPlan, ListParentRelationData) {
	plan := ListPlan{
		DatabaseSchema: "",
		ObjectType:     "doc",
		Relation:       "can_read",
		Analysis:       RelationAnalysis{ObjectType: "doc", Relation: "can_read"},
		AnalysisLookup: lookup,
	}
	parent := ListParentRelationData{
		Relation:                 "repo_admin",
		LinkingRelation:          "owner",
		AllowedLinkingTypesSlice: []string{"repo"},
		IsClosurePattern:         true,
		SourceRelation:           "reader",
	}
	return plan, parent
}

func closureBlocksSQL(plan ListPlan, parent ListParentRelationData) string {
	var b strings.Builder
	for _, blk := range buildListSubjectsRecursiveTTUBlock(plan, parent) {
		b.WriteString(blk.Query.SQL())
		b.WriteString("\n")
	}
	return b.String()
}

func TestClosureTTUSubjects_ComposeWhenComposable(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"doc.can_read": {ObjectType: "doc", Relation: "can_read"},
		// reader forces subject_pool (has exclusion) and is a closure pattern.
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			Features:     RelationFeatures{HasExclusion: true},
		},
		// repo.repo_admin is what classifyParentRelation inspects; give it
		// exclusion so the parent is routed to subject_pool (closure branch).
		"repo.repo_admin": {
			ObjectType: "repo",
			Relation:   "repo_admin",
			Features:   RelationFeatures{HasExclusion: true},
		},
	}
	plan, parent := closureTTUPlan(lookup)

	sql := closureBlocksSQL(plan, parent)

	if !strings.Contains(sql, "list_doc_reader_sub(p_object_id, p_subject_type)") {
		t.Errorf("expected composed SELECT from list_doc_reader_sub, got:\n%s", sql)
	}
	if strings.Contains(sql, "check_permission_internal") {
		t.Errorf("composable closure TTU must not emit per-candidate check, got:\n%s", sql)
	}
	if strings.Contains(sql, "subject_pool") {
		t.Errorf("composable closure TTU must not scan subject_pool, got:\n%s", sql)
	}
}

func TestClosureTTUSubjects_FallbackWhenNotComposable(t *testing.T) {
	// reader is not list-generatable → keep subject_pool + per-candidate check.
	lookup := map[string]*RelationAnalysis{
		"doc.can_read": {ObjectType: "doc", Relation: "can_read"},
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: false},
		},
		"repo.repo_admin": {
			ObjectType: "repo",
			Relation:   "repo_admin",
			Features:   RelationFeatures{HasExclusion: true},
		},
	}
	plan, parent := closureTTUPlan(lookup)

	sql := closureBlocksSQL(plan, parent)

	if strings.Contains(sql, "list_doc_reader_sub") {
		t.Errorf("non-generatable target must fall back to subject_pool, got compose:\n%s", sql)
	}
	if !strings.Contains(sql, "subject_pool") {
		t.Errorf("expected subject_pool fallback, got:\n%s", sql)
	}
	if !strings.Contains(sql, "check_permission_internal(p_subject_type, sp.subject_id, 'reader', 'doc', p_object_id)") {
		t.Errorf("expected per-candidate closure check on target object, got:\n%s", sql)
	}
}

func TestClosureTTUSubjects_FallbackWhenWildcard(t *testing.T) {
	// Target reader HasWildcard → an IN-set drops concrete subjects granted only
	// via '*', so composition is gated out. Keep subject_pool.
	lookup := map[string]*RelationAnalysis{
		"doc.can_read": {ObjectType: "doc", Relation: "can_read"},
		"doc.reader": {
			ObjectType:   "doc",
			Relation:     "reader",
			Capabilities: GenerationCapabilities{ListAllowed: true},
			ListStrategy: ListStrategyRecursive,
			Features:     RelationFeatures{HasExclusion: true, HasWildcard: true},
		},
		"repo.repo_admin": {
			ObjectType: "repo",
			Relation:   "repo_admin",
			Features:   RelationFeatures{HasExclusion: true},
		},
	}
	plan, parent := closureTTUPlan(lookup)

	sql := closureBlocksSQL(plan, parent)

	if strings.Contains(sql, "list_doc_reader_sub") {
		t.Errorf("wildcard target must fall back to subject_pool, got compose:\n%s", sql)
	}
	if !strings.Contains(sql, "subject_pool") {
		t.Errorf("expected subject_pool fallback for wildcard target, got:\n%s", sql)
	}
}
