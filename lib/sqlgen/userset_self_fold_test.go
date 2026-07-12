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
	rows := make([]ValuesRow, 0, len(satisfying)+200)
	for _, s := range satisfying {
		rows = append(rows, closureRow(objType, relation, s))
	}
	for i := 0; i < 200; i++ {
		rows = append(rows, closureRow("aaa", "rel", "rel"))
	}
	return InlineSQLData{ClosureRows: rows}
}

// listFoldPlan builds a ListPlan for the doc#viewer self-candidate fold tests,
// with a big unrelated closure that a VALUES-scan would embed.
func listFoldPlan(satisfying []string) ListPlan {
	return ListPlan{
		Analysis:   mkFoldAnalysis("doc", "viewer", satisfying),
		Inline:     bigClosure("doc", "viewer", satisfying),
		ObjectType: "doc",
		Relation:   "viewer",
	}
}

// Each self-check site folds the whole-model closure VALUES scan to a
// compile-time IN-list: the rendered SQL must not embed the closure table and
// must carry the satisfying set as an IN-list.
func TestUsersetSelfCheck_Fold(t *testing.T) {
	cases := []struct {
		name   string
		wantIN string
		sql    func(t *testing.T) string
	}{
		{
			name:   "check function",
			wantIN: "IN ('viewer', 'editor')",
			sql: func(t *testing.T) string {
				a := mkFoldAnalysis("doc", "viewer", []string{"viewer", "editor"})
				plan := BuildCheckPlan(a, bigClosure("doc", "viewer", []string{"viewer", "editor"}), "", false)
				selfCheck, _ := buildUsersetSubjectChecks(plan)
				return selfCheck.SQL()
			},
		},
		{
			name:   "list_objects self-candidate",
			wantIN: "IN ('viewer')",
			sql: func(t *testing.T) string {
				return buildListObjectsSelfCandidateBlock(listFoldPlan([]string{"viewer"})).Query.SQL()
			},
		},
		{
			name:   "composed self-block",
			wantIN: "IN ('viewer')",
			sql: func(t *testing.T) string {
				block, err := buildComposedObjectsSelfBlock(listFoldPlan([]string{"viewer"}))
				if err != nil {
					t.Fatal(err)
				}
				return block.Query.SQL()
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := tc.sql(t)
			if strings.Contains(sql, closureAlias) {
				t.Fatalf("%s still embeds closure VALUES:\n%s", tc.name, sql)
			}
			if !strings.Contains(sql, tc.wantIN) {
				t.Fatalf("%s missing folded IN-list %q:\n%s", tc.name, tc.wantIN, sql)
			}
		})
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
