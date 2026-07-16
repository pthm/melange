package sqlgen

import (
	"strings"
	"testing"
)

// Finding 5: LIMIT 1 inside an EXISTS subquery is pure noise — EXISTS
// short-circuits on the first row. The check builders that wrap a probe in
// Exists{} must not emit LIMIT 1, while SELECT INTO evidence queries (whose
// scalar cardinality is consumed) keep it.
func TestCheckExists_NoLimitOne(t *testing.T) {
	a := mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true)
	a.SatisfyingRelations = []string{"viewer"}

	plan := BuildCheckPlan(a, InlineSQLData{}, "", false)
	blocks, err := BuildCheckBlocks(plan)
	if err != nil {
		t.Fatalf("BuildCheckBlocks: %v", err)
	}

	// EXISTS-wrapped direct probe: no LIMIT 1.
	if got := blocks.DirectCheck.SQL(); !strings.Contains(got, "EXISTS (") {
		t.Fatalf("DirectCheck is not EXISTS-wrapped:\n%s", got)
	} else if strings.Contains(strings.ToUpper(got), "LIMIT 1") {
		t.Errorf("EXISTS subquery still contains LIMIT 1:\n%s", got)
	}

	// SELECT INTO evidence query keeps LIMIT 1 (cardinality matters).
	if got := blocks.UsersetSubjectSelfCheck.SQL(); !strings.Contains(strings.ToUpper(got), "LIMIT 1") {
		t.Errorf("SELECT INTO self-check dropped LIMIT 1:\n%s", got)
	}
}
