package sqlgen

import (
	"strings"
	"testing"
)

// The userset self-check ("is the subject a userset on THIS object type whose
// relation satisfies plan.Relation?") folds from a VALUES-scan over the whole
// model's closure to a compile-time IN-list, since object_type and relation are
// codegen constants and the satisfying set is plan.Analysis.SatisfyingRelations.
// These tests pin the folded shape and prove the closure VALUES no longer
// appears in the self-check, so its size is independent of unrelated relations.

// closureAlias is the fragment ClosureTable emits for its VALUES table; if the
// self-check still scanned the closure it would appear in the rendered SQL.
const closureAlias = "AS c(object_type"

func mkFoldAnalysis(objType, relation string, satisfying []string) RelationAnalysis {
	a := mkAnalysis(objType, relation, RelationFeatures{HasDirect: true}, true)
	a.SatisfyingRelations = satisfying
	return a
}

// bigClosure returns closure rows for the target relation plus many unrelated
// rows, so a VALUES-scan self-check would embed all of them.
func bigClosure(objType, relation string, satisfying []string) InlineSQLData {
	var rows []ValuesRow
	for _, s := range satisfying {
		rows = append(rows, closureRow(objType, relation, s))
	}
	for i := 0; i < 200; i++ {
		rows = append(rows, closureRow("aaa", "rel", "rel"))
	}
	return InlineSQLData{ClosureRows: rows}
}

func TestUsersetSelfCheck_CheckFunctionFold(t *testing.T) {
	a := mkFoldAnalysis("doc", "viewer", []string{"viewer", "editor"})
	plan := BuildCheckPlan(a, bigClosure("doc", "viewer", []string{"viewer", "editor"}), "", false)
	selfCheck, _ := buildUsersetSubjectChecks(plan)
	sql := selfCheck.SQL()

	if strings.Contains(sql, closureAlias) {
		t.Fatalf("check self-check still embeds closure VALUES:\n%s", sql)
	}
	if !strings.Contains(sql, "IN ('viewer', 'editor')") {
		t.Fatalf("check self-check missing folded IN-list:\n%s", sql)
	}
}

func TestUsersetSelfCheck_ListObjectsFold(t *testing.T) {
	a := mkFoldAnalysis("doc", "viewer", []string{"viewer"})
	plan := ListPlan{
		Analysis:   a,
		Inline:     bigClosure("doc", "viewer", []string{"viewer"}),
		ObjectType: "doc",
		Relation:   "viewer",
	}
	block := buildListObjectsSelfCandidateBlock(plan)
	sql := block.Query.SQL()

	if strings.Contains(sql, closureAlias) {
		t.Fatalf("list_objects self-candidate still embeds closure VALUES:\n%s", sql)
	}
	if !strings.Contains(sql, "IN ('viewer')") {
		t.Fatalf("list_objects self-candidate missing folded IN-list:\n%s", sql)
	}
}

func TestUsersetSelfCheck_ComposedObjectsFold(t *testing.T) {
	a := mkFoldAnalysis("doc", "viewer", []string{"viewer"})
	plan := ListPlan{
		Analysis:   a,
		Inline:     bigClosure("doc", "viewer", []string{"viewer"}),
		ObjectType: "doc",
		Relation:   "viewer",
	}
	block, err := buildComposedObjectsSelfBlock(plan)
	if err != nil {
		t.Fatal(err)
	}
	sql := block.Query.SQL()

	if strings.Contains(sql, closureAlias) {
		t.Fatalf("composed self-block still embeds closure VALUES:\n%s", sql)
	}
	if !strings.Contains(sql, "IN ('viewer')") {
		t.Fatalf("composed self-block missing folded IN-list:\n%s", sql)
	}
}

// Empty SatisfyingRelations must render FALSE (never matches), matching the
// empty VALUES-scan it replaces.
func TestUsersetSelfCheck_EmptySatisfyingRendersFalse(t *testing.T) {
	a := mkFoldAnalysis("doc", "viewer", nil)
	plan := BuildCheckPlan(a, InlineSQLData{}, "", false)
	selfCheck, _ := buildUsersetSubjectChecks(plan)
	if !strings.Contains(selfCheck.SQL(), "FALSE") {
		t.Fatalf("empty satisfying set must render FALSE:\n%s", selfCheck.SQL())
	}
}
