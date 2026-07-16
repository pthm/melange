package sqlgen

import (
	"strings"
	"testing"
)

// listAnalysisWithDirectAndUserset returns an analysis that generates a list
// function exercising the pagination CTE wrappers (paged + returned multi-ref).
func listAnalysisWithDirectAndUserset() RelationAnalysis {
	a := mkAnalysis("document", "viewer", RelationFeatures{
		HasDirect:  true,
		HasUserset: true,
	}, true)
	a.AllowedSubjectTypes = []string{"user"}
	a.SatisfyingRelations = []string{"viewer"}
	return a
}

func TestGenerateListSQL_MaterializedCTEsDefaultOff(t *testing.T) {
	// Default behavior: PG's own inlining/materialization decision wins on
	// production-scale benchmarks; we don't force AS MATERIALIZED unless
	// EnableMaterializedCTEs is set.
	analyses := []RelationAnalysis{listAnalysisWithDirectAndUserset()}

	out, err := GenerateListSQL(analyses, InlineSQLData{}, "")
	if err != nil {
		t.Fatalf("GenerateListSQL: %v", err)
	}
	if len(out.ListObjectsFunctions) == 0 || len(out.ListSubjectsFunctions) == 0 {
		t.Fatalf("expected list functions to be generated, got %d/%d",
			len(out.ListObjectsFunctions), len(out.ListSubjectsFunctions))
	}
	combined := strings.Join(out.ListObjectsFunctions, "\n") + "\n" + strings.Join(out.ListSubjectsFunctions, "\n")

	if strings.Contains(combined, "AS MATERIALIZED") {
		idx := strings.Index(combined, "AS MATERIALIZED")
		start := idx - 64
		if start < 0 {
			start = 0
		}
		end := idx + 64
		if end > len(combined) {
			end = len(combined)
		}
		t.Errorf("default GenerateListSQL must NOT force materialization; first hit context:\n%s", combined[start:end])
	}
}

func TestGenerateListSQL_EnableMaterializedCTEs(t *testing.T) {
	analyses := []RelationAnalysis{listAnalysisWithDirectAndUserset()}

	out, err := GenerateListSQLWithOptions(analyses, InlineSQLData{}, "", GenerateSQLOptions{
		EnableMaterializedCTEs: true,
	})
	if err != nil {
		t.Fatalf("GenerateListSQLWithOptions: %v", err)
	}
	combined := strings.Join(out.ListObjectsFunctions, "\n") + "\n" + strings.Join(out.ListSubjectsFunctions, "\n")

	for _, want := range []string{
		"paged AS MATERIALIZED (",
		"returned AS MATERIALIZED (",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("EnableMaterializedCTEs=true must emit %q", want)
		}
	}

	// base_results in the wrappers is singly referenced — even with the opt-in,
	// the wrapper-level CTE must not gain AS MATERIALIZED.
	if strings.Contains(combined, "WITH base_results AS MATERIALIZED") {
		t.Errorf("wrapper-level base_results must not be materialized (singly referenced)")
	}
}

func TestGenerateSQLWithOptions_AcceptsOptions(t *testing.T) {
	// Smoke: the options-aware variant produces the same shape as the default.
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	out, err := GenerateSQLWithOptions(analyses, InlineSQLData{}, "", GenerateSQLOptions{
		EnableMaterializedCTEs: true,
	})
	if err != nil {
		t.Fatalf("GenerateSQLWithOptions: %v", err)
	}
	if len(out.Functions) == 0 {
		t.Fatal("expected at least one check function")
	}
}
