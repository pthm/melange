package sqlgen

import (
	"slices"
	"strings"
	"testing"
)

// helper: a CheckAllowed/ListAllowed analysis with the given features.
func mkAnalysis(objType, relation string, features RelationFeatures, listAllowed bool) RelationAnalysis {
	return RelationAnalysis{
		ObjectType: objType,
		Relation:   relation,
		Features:   features,
		Capabilities: GenerationCapabilities{
			CheckAllowed: true,
			ListAllowed:  listAllowed,
		},
	}
}

func TestRecommendIndexes_DirectOnly(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}

	recs := RecommendIndexes(analyses)

	if got, want := len(recs), 2; got != want {
		t.Fatalf("got %d recommendations, want %d", got, want)
	}

	// Sorted by DDL: object_id (object-keyed) sorts before subject_type (subject-keyed)
	// because "object_id" < "subject_type" lexicographically inside the DDL string.
	objKeyed := findRec(t, recs, []string{"object_type", "object_id", "relation", "subject_type", "subject_id"}, "")
	if got := objKeyed.BenefitsFunctions; !slices.Contains(got, "check_document_viewer") {
		t.Errorf("object-keyed BenefitsFunctions = %v, missing check_document_viewer", got)
	}
	if got := objKeyed.BenefitsFunctions; !slices.Contains(got, "list_document_viewer_sub") {
		t.Errorf("object-keyed BenefitsFunctions = %v, missing list_document_viewer_sub", got)
	}

	subKeyed := findRec(t, recs, []string{"subject_type", "subject_id", "relation", "object_type", "object_id"}, "")
	if got, want := subKeyed.BenefitsFunctions, []string{"list_document_viewer_obj"}; !slices.Equal(got, want) {
		t.Errorf("subject-keyed BenefitsFunctions = %v, want %v", got, want)
	}

	// DDL sanity: each starts with the canonical prefix and targets melange_tuples.
	for _, rec := range recs {
		if !strings.HasPrefix(rec.DDL, "CREATE INDEX IF NOT EXISTS ") {
			t.Errorf("DDL missing IF NOT EXISTS prefix: %q", rec.DDL)
		}
		if !strings.Contains(rec.DDL, "ON melange_tuples (") {
			t.Errorf("DDL missing melange_tuples target: %q", rec.DDL)
		}
		if !strings.HasSuffix(rec.DDL, ";") {
			t.Errorf("DDL missing trailing semicolon: %q", rec.DDL)
		}
		if strings.Contains(rec.DDL, "WHERE") {
			t.Errorf("non-wildcard DDL should not have WHERE: %q", rec.DDL)
		}
	}
}

func TestRecommendIndexes_Wildcard(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "public", RelationFeatures{HasDirect: true, HasWildcard: true}, true),
	}

	recs := RecommendIndexes(analyses)

	if got, want := len(recs), 3; got != want {
		t.Fatalf("got %d recommendations, want %d", got, want)
	}

	wildcard := findRec(t, recs, []string{"object_type", "object_id", "relation"}, "subject_id = '*'")
	if !strings.Contains(wildcard.DDL, "WHERE subject_id = '*'") {
		t.Errorf("wildcard DDL missing predicate: %q", wildcard.DDL)
	}
	if !strings.Contains(wildcard.DDL, "_wildcard ON") {
		t.Errorf("wildcard DDL should have _wildcard suffix in index name: %q", wildcard.DDL)
	}

	// list_*_obj uses the subject-keyed path and is not enumerated by the
	// wildcard partial; that partial only covers check_* and list_*_sub.
	for _, fn := range wildcard.BenefitsFunctions {
		if strings.HasSuffix(fn, "_obj") {
			t.Errorf("wildcard partial should not benefit %s (subject-keyed family)", fn)
		}
	}
	if !slices.Contains(wildcard.BenefitsFunctions, "check_document_public") {
		t.Errorf("wildcard BenefitsFunctions = %v, missing check_document_public", wildcard.BenefitsFunctions)
	}
	if !slices.Contains(wildcard.BenefitsFunctions, "list_document_public_sub") {
		t.Errorf("wildcard BenefitsFunctions = %v, missing list_document_public_sub", wildcard.BenefitsFunctions)
	}
}

func TestRecommendIndexes_DeduplicatesAcrossRelations(t *testing.T) {
	// Two relations on the same object type — both generate the same two
	// shapes, so we expect 2 recs total with BenefitsFunctions covering both.
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
		mkAnalysis("document", "editor", RelationFeatures{HasDirect: true}, true),
	}

	recs := RecommendIndexes(analyses)

	if got, want := len(recs), 2; got != want {
		t.Fatalf("got %d recommendations, want %d (one per index family, deduped)", got, want)
	}

	objKeyed := findRec(t, recs, []string{"object_type", "object_id", "relation", "subject_type", "subject_id"}, "")
	// viewer/editor are plain [user] direct relations that reach no wildcard, so
	// no _nw variant is emitted for them and none is listed as a beneficiary.
	wantFns := []string{
		"check_document_editor",
		"check_document_viewer",
		"list_document_editor_sub",
		"list_document_viewer_sub",
	}
	if got := objKeyed.BenefitsFunctions; !slices.Equal(got, wantFns) {
		t.Errorf("object-keyed BenefitsFunctions = %v, want %v", got, wantFns)
	}

	subKeyed := findRec(t, recs, []string{"subject_type", "subject_id", "relation", "object_type", "object_id"}, "")
	wantSub := []string{"list_document_editor_obj", "list_document_viewer_obj"}
	if got := subKeyed.BenefitsFunctions; !slices.Equal(got, wantSub) {
		t.Errorf("subject-keyed BenefitsFunctions = %v, want %v", got, wantSub)
	}
}

func TestRecommendIndexes_DeterministicOrder(t *testing.T) {
	// Two passes over the same input must produce byte-identical results.
	analyses := []RelationAnalysis{
		mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasWildcard: true}, true),
		mkAnalysis("doc", "editor", RelationFeatures{HasDirect: true}, true),
		mkAnalysis("folder", "owner", RelationFeatures{HasDirect: true, HasWildcard: true}, false),
	}

	a := RecommendIndexes(analyses)
	b := RecommendIndexes(analyses)

	if len(a) != len(b) {
		t.Fatalf("non-deterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].DDL != b[i].DDL {
			t.Errorf("non-deterministic order at index %d:\n  a=%s\n  b=%s", i, a[i].DDL, b[i].DDL)
		}
	}

	// Sorted-by-DDL invariant.
	for i := 1; i < len(a); i++ {
		if a[i-1].DDL >= a[i].DDL {
			t.Errorf("results not sorted by DDL at %d: %q >= %q", i, a[i-1].DDL, a[i].DDL)
		}
	}
}

func TestRecommendIndexes_SkipsUngeneratable(t *testing.T) {
	// Neither CheckAllowed nor ListAllowed → no recommendations.
	analyses := []RelationAnalysis{
		{
			ObjectType:   "ghost",
			Relation:     "nope",
			Features:     RelationFeatures{HasDirect: true},
			Capabilities: GenerationCapabilities{}, // both false
		},
	}
	if got := RecommendIndexes(analyses); len(got) != 0 {
		t.Errorf("expected no recommendations for ungeneratable relation, got %v", got)
	}
}

func TestRecommendIndexes_CheckOnlyNoListSub(t *testing.T) {
	// CheckAllowed but not ListAllowed: object-keyed only mentions check_* names,
	// and no subject-keyed recommendation is emitted.
	analyses := []RelationAnalysis{
		mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true}, false),
	}
	recs := RecommendIndexes(analyses)
	if got, want := len(recs), 1; got != want {
		t.Fatalf("got %d recommendations, want %d", got, want)
	}
	rec := recs[0]
	for _, fn := range rec.BenefitsFunctions {
		if strings.HasPrefix(fn, "list_") {
			t.Errorf("check-only relation should not list any list_ functions, got %s", fn)
		}
	}
}

// TestIndexName_SingleColumnFallback covers the defensive branch in indexName
// that handles len(cols) < 2. RecommendIndexes never emits an index with
// fewer than 2 columns today, but the fallback keeps the function total when
// a future caller supplies one. Test via the public renderer to avoid
// internal-impl coupling.
func TestIndexName_SingleColumnFallback(t *testing.T) {
	rec := IndexRecommendation{
		BaseTable: "melange_tuples",
		Columns:   []string{"object_id"},
	}
	rec.DDL = renderIndexDDL(rec)

	if !strings.Contains(rec.DDL, "idx_melange_tuples_object_id") {
		t.Errorf("single-column fallback must produce idx_melange_tuples_<col>; got: %s", rec.DDL)
	}
	// Should not produce the standard "_by_<a>_<b>" infix used for composites.
	if strings.Contains(rec.DDL, "_by_") {
		t.Errorf("single-column index name should not contain _by_; got: %s", rec.DDL)
	}
}

func TestGenerateSQL_PopulatesIndexRecommendations(t *testing.T) {
	// Smoke test: confirm GenerateSQL surfaces recommendations on its result.
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	out, err := GenerateSQL(analyses, InlineSQLData{}, "")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if len(out.IndexRecommendations) == 0 {
		t.Fatal("GenerateSQL did not populate IndexRecommendations")
	}
}

// findRec returns the recommendation matching the given columns and where
// clause. Failing the test is the only safe behavior if there's no match —
// it means the test setup or RecommendIndexes drifted.
func findRec(t *testing.T, recs []IndexRecommendation, cols []string, where string) IndexRecommendation {
	t.Helper()
	for _, rec := range recs {
		if slices.Equal(rec.Columns, cols) && rec.WhereClause == where {
			return rec
		}
	}
	t.Fatalf("no recommendation with columns=%v where=%q in %d recs", cols, where, len(recs))
	return IndexRecommendation{}
}
