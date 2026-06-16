package sqldsl

import (
	"strings"
	"testing"
)

// The pagination wrappers below render long fmt.Sprintf templates. We don't
// need to assert the full SQL — that's verified end-to-end by the OpenFGA
// suite. Each test just makes sure the function executes without panicking
// for non-trivial input and that the MATERIALIZED toggle flows through.

func TestMaterializedKeyword(t *testing.T) {
	if got := materializedKeyword(true); got != " MATERIALIZED" {
		t.Errorf("materializedKeyword(true) = %q, want %q", got, " MATERIALIZED")
	}
	if got := materializedKeyword(false); got != "" {
		t.Errorf("materializedKeyword(false) = %q, want empty", got)
	}
}

func TestWrapWithPagination(t *testing.T) {
	out := WrapWithPagination("SELECT 1", "id")
	if !strings.Contains(out, "WITH base_results AS") {
		t.Errorf("expected base_results CTE; got: %s", out)
	}
	// Bare form defaults to materialize=true.
	if !strings.Contains(out, "paged AS MATERIALIZED") {
		t.Errorf("bare WrapWithPagination should emit AS MATERIALIZED; got: %s", out)
	}
}

func TestWrapWithPaginationOpts(t *testing.T) {
	matOn := WrapWithPaginationOpts("SELECT 1", "id", true)
	matOff := WrapWithPaginationOpts("SELECT 1", "id", false)

	if !strings.Contains(matOn, "paged AS MATERIALIZED") {
		t.Errorf("materialize=true must emit AS MATERIALIZED; got: %s", matOn)
	}
	if strings.Contains(matOff, "MATERIALIZED") {
		t.Errorf("materialize=false must NOT emit MATERIALIZED; got: %s", matOff)
	}
	if !strings.Contains(matOff, "paged AS (") {
		t.Errorf("materialize=false must still declare the paged CTE; got: %s", matOff)
	}
}

func TestWrapWithPaginationWildcardFirst(t *testing.T) {
	out := WrapWithPaginationWildcardFirst("SELECT 1")
	if !strings.Contains(out, "(CASE WHEN br.subject_id = '*' THEN 0 ELSE 1 END)") {
		t.Errorf("wildcard-first wrapper must emit the wildcard CASE; got: %s", out)
	}
	if !strings.Contains(out, "paged AS MATERIALIZED") {
		t.Errorf("bare wildcard-first wrapper defaults to MATERIALIZED; got: %s", out)
	}
}

func TestWrapWithPaginationWildcardFirstOpts(t *testing.T) {
	matOff := WrapWithPaginationWildcardFirstOpts("SELECT 1", false)
	if strings.Contains(matOff, "MATERIALIZED") {
		t.Errorf("materialize=false must NOT emit MATERIALIZED; got: %s", matOff)
	}
}

func TestWrapWithExclusionCTEAndPagination(t *testing.T) {
	out := WrapWithExclusionCTEAndPagination("SELECT 1 FROM candidates", "SELECT subject_id FROM tuples")
	if !strings.Contains(out, "excluded_subjects") {
		t.Errorf("exclusion wrapper must declare excluded_subjects CTE; got: %s", out)
	}
	if !strings.Contains(out, "paged AS MATERIALIZED") {
		t.Errorf("bare exclusion wrapper defaults to MATERIALIZED; got: %s", out)
	}
}

func TestWrapWithExclusionCTEAndPaginationOpts(t *testing.T) {
	matOff := WrapWithExclusionCTEAndPaginationOpts("SELECT 1", "SELECT 1", false)
	if strings.Contains(matOff, "MATERIALIZED") {
		t.Errorf("materialize=false must NOT emit MATERIALIZED; got: %s", matOff)
	}
	if !strings.Contains(matOff, "excluded_subjects") {
		t.Errorf("opt-off variant still needs the exclusion CTE; got: %s", matOff)
	}
}
