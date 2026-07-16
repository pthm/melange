package sqlgen

import (
	"strings"
	"testing"
)

// Finding 7: recursive and recursive-intersection check renderers used to append
// a redundant `v_has_access := FALSE;` statement even though recursiveCheckDecls
// already initializes the variable to FALSE in the DECLARE block. The renderers
// no longer emit that dead re-assignment; the only FALSE-init is the declaration.
func TestRecursiveCheck_NoRedundantHasAccessInit(t *testing.T) {
	assertNoRedundantInit := func(t *testing.T, sql string) {
		t.Helper()
		// The DECLARE initializes it once — that form must survive.
		if !strings.Contains(sql, "v_has_access BOOLEAN := FALSE") {
			t.Fatalf("declaration init missing:\n%s", sql)
		}
		// The statement-form re-assignment must be gone.
		if strings.Contains(sql, "v_has_access := FALSE") {
			t.Errorf("redundant statement re-assignment survived:\n%s", sql)
		}
	}

	// recursive: HasRecursive → NeedsPLpgSQL, no intersection.
	rec := mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true, HasRecursive: true}, true)
	rec.SatisfyingRelations = []string{"viewer"}

	// recursive_intersection: recursive + intersection.
	recInt := mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true, HasRecursive: true, HasIntersection: true}, true)
	recInt.SatisfyingRelations = []string{"viewer"}

	for _, tc := range []struct {
		name string
		a    RelationAnalysis
	}{
		{"recursive", rec},
		{"recursive_intersection", recInt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildCheckPlan(tc.a, InlineSQLData{}, "", false)
			blocks, err := BuildCheckBlocks(plan)
			if err != nil {
				t.Fatalf("BuildCheckBlocks: %v", err)
			}
			sql, err := RenderCheckFunction(plan, blocks)
			if err != nil {
				t.Fatalf("RenderCheckFunction: %v", err)
			}
			assertNoRedundantInit(t, sql)
		})
	}
}
