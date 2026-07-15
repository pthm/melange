package sqlgen

import (
	"strings"
	"testing"
)

// renderStmts joins the rendered PL/pgSQL of a stmt list for substring asserts.
func renderStmts(stmts []Stmt) string {
	var b strings.Builder
	for _, s := range stmts {
		b.WriteString(s.StmtSQL())
		b.WriteString("\n")
	}
	return b.String()
}

// Finding 1.1: the Case-2 (computed userset matching) block is gated
// per-relation, not model-wide. A relation whose satisfying set is disjoint
// from the type's stored-userset relations (e.g. a plain direct relation)
// emitted a provably dead block under the old model-wide UsersetRows gate.
// The gate now keys on this relation's own analysis: it needs EITHER direct
// UsersetPatterns OR ClosureUsersetPatterns (closure-only relations like
// can_view have empty own patterns but a live Case 2).
func TestCase2Gate_PerRelation(t *testing.T) {
	const case2 = "Case 2: Computed userset matching"

	// (1) Direct relation: no userset patterns of any kind → no Case 2.
	direct := mkAnalysis("document", "owner", RelationFeatures{HasDirect: true}, true)
	direct.SatisfyingRelations = []string{"owner"}

	// (2) Userset relation: owns a [group#member] pattern → live Case 2.
	userset := mkAnalysis("document", "viewer", RelationFeatures{HasUserset: true}, true)
	userset.SatisfyingRelations = []string{"viewer"}
	userset.UsersetPatterns = []UsersetPattern{{
		SubjectType:         "group",
		SubjectRelation:     "member",
		SatisfyingRelations: []string{"member"},
	}}

	// (3) Closure-userset relation: empty own patterns, closure carries the
	// userset (e.g. can_view: viewer, viewer: [group#member]) → live Case 2.
	closure := mkAnalysis("document", "can_view", RelationFeatures{HasImplied: true}, true)
	closure.SatisfyingRelations = []string{"can_view", "viewer"}
	closure.ClosureUsersetPatterns = []UsersetPattern{{
		SubjectType:         "group",
		SubjectRelation:     "member",
		SatisfyingRelations: []string{"member"},
	}}

	cases := []struct {
		name      string
		a         RelationAnalysis
		wantCase2 bool
	}{
		{"direct_owner", direct, false},
		{"userset_viewer", userset, true},
		{"closure_can_view", closure, true},
	}

	// The old gate keyed on model-wide UsersetRows, which is non-empty for any
	// type that has any userset relation. Populate it so the direct-relation
	// case is genuinely dead-but-emitted under the old gate — i.e. this test is
	// RED on the old gate and GREEN on the per-relation one.
	inline := InlineSQLData{
		UsersetRows: []ValuesRow{usersetRow("document", "viewer", "group", "member")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildCheckPlan(tc.a, inline, "", false)
			blocks, err := BuildCheckBlocks(plan)
			if err != nil {
				t.Fatalf("BuildCheckBlocks: %v", err)
			}
			sql := renderStmts(buildUsersetSubjectStmts(plan, blocks))
			if got := strings.Contains(sql, case2); got != tc.wantCase2 {
				t.Errorf("%s: Case 2 emitted=%v, want %v\n%s", tc.name, got, tc.wantCase2, sql)
			}
		})
	}
}
