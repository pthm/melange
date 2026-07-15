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

// Findings 1.2/1.3/4: the surviving Case-2 block must (a) reconstruct the tuple
// subject_id as a sargable equality on the indexed column, (b) parse userset IDs
// with the canonical split_part helpers (no substring), and (c) narrow the
// subj_c closure VALUES to the statically-compatible rows for the userset
// subject type — dropping unrelated closure rows without ever emptying to a
// never-matching sentinel.
func TestCase2_SargableNarrowedCanonical(t *testing.T) {
	// can_view: viewer, viewer: [group#member] — closure-only userset relation.
	a := mkAnalysis("document", "can_view", RelationFeatures{HasImplied: true}, true)
	a.SatisfyingRelations = []string{"can_view", "viewer"}
	a.ClosureUsersetPatterns = []UsersetPattern{{
		SubjectType:         "group",
		SubjectRelation:     "member",
		SatisfyingRelations: []string{"member"},
	}}

	inline := InlineSQLData{
		UsersetRows: []ValuesRow{usersetRow("document", "viewer", "group", "member")},
		ClosureRows: []ValuesRow{
			closureRow("group", "member", "member"),    // compatible → kept
			closureRow("document", "viewer", "viewer"), // wrong type → dropped
			closureRow("group", "admin", "admin"),      // relation not reachable → dropped
		},
	}

	plan := BuildCheckPlan(a, inline, "", false)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}
	sql := blocks.UsersetSubjectComputedCheck.SQL()

	// (a) sargable equality on the indexed t.subject_id.
	if !strings.Contains(sql, "t.subject_id = split_part(p_subject_id, '#', 1) || '#' || subj_c.relation") {
		t.Errorf("missing sargable subject_id equality:\n%s", sql)
	}
	// (b) canonical parse: no substring form survives.
	if strings.Contains(sql, "substring(") {
		t.Errorf("non-canonical substring parse survived:\n%s", sql)
	}
	// (c) narrowed VALUES: keep the one compatible row, drop the two unrelated.
	if !strings.Contains(sql, "('group', 'member', 'member')") {
		t.Errorf("compatible subj_c row missing:\n%s", sql)
	}
	if strings.Contains(sql, "'admin'") || strings.Contains(sql, "('document', 'viewer', 'viewer')") {
		t.Errorf("statically-incompatible closure rows not narrowed out:\n%s", sql)
	}
	if strings.Contains(sql, "NULL::TEXT, NULL::TEXT, NULL::TEXT") {
		t.Errorf("subj_c narrowed to never-matching NULL sentinel:\n%s", sql)
	}
}
