package sqldsl

import (
	"strings"
	"testing"
)

func TestCTEDef_SQL(t *testing.T) {
	tests := []struct {
		name     string
		cte      CTEDef
		contains []string
	}{
		{
			name: "simple CTE without columns",
			cte: CTEDef{
				Name:  "base_results",
				Query: Raw("SELECT 1"),
			},
			contains: []string{"base_results AS (", "SELECT 1"},
		},
		{
			name: "CTE with columns",
			cte: CTEDef{
				Name:    "accessible",
				Columns: []string{"object_id", "depth"},
				Query:   Raw("SELECT id, 0 FROM objects"),
			},
			contains: []string{"accessible(object_id, depth) AS (", "SELECT id, 0 FROM objects"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cte.SQL()
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("CTEDef.SQL() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestWithCTE_SQL(t *testing.T) {
	tests := []struct {
		name     string
		cte      WithCTE
		contains []string
	}{
		{
			name: "simple recursive CTE",
			cte: WithCTE{
				Recursive: true,
				CTEs: []CTEDef{{
					Name:    "accessible",
					Columns: []string{"object_id", "depth"},
					Query:   Raw("SELECT id, 0 FROM base"),
				}},
				Query: Raw("SELECT * FROM accessible"),
			},
			contains: []string{
				"WITH RECURSIVE",
				"accessible(object_id, depth) AS (",
				"SELECT id, 0 FROM base",
				"SELECT * FROM accessible",
			},
		},
		{
			name: "non-recursive CTE",
			cte: WithCTE{
				Recursive: false,
				CTEs: []CTEDef{{
					Name:  "filtered",
					Query: Raw("SELECT * FROM data WHERE active = TRUE"),
				}},
				Query: Raw("SELECT * FROM filtered"),
			},
			contains: []string{
				"WITH filtered AS (",
				"SELECT * FROM data WHERE active = TRUE",
				"SELECT * FROM filtered",
			},
		},
		{
			name: "multiple CTEs",
			cte: WithCTE{
				Recursive: false,
				CTEs: []CTEDef{
					{Name: "cte1", Query: Raw("SELECT 1")},
					{Name: "cte2", Query: Raw("SELECT 2")},
				},
				Query: Raw("SELECT * FROM cte1, cte2"),
			},
			contains: []string{
				"WITH cte1 AS (",
				"cte2 AS (",
				"SELECT * FROM cte1, cte2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cte.SQL()
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("WithCTE.SQL() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestRecursiveCTE(t *testing.T) {
	cte := RecursiveCTE(
		"accessible",
		[]string{"object_id", "depth"},
		Raw("SELECT id, 0 FROM base UNION ALL SELECT id, depth + 1 FROM accessible"),
		Raw("SELECT object_id FROM accessible"),
	)

	got := cte.SQL()
	if !strings.Contains(got, "WITH RECURSIVE") {
		t.Errorf("RecursiveCTE should produce WITH RECURSIVE, got: %s", got)
	}
	if !strings.Contains(got, "accessible(object_id, depth)") {
		t.Errorf("RecursiveCTE should include column names, got: %s", got)
	}
}

func TestSimpleCTE(t *testing.T) {
	cte := SimpleCTE("filtered", Raw("SELECT * FROM data"), Raw("SELECT * FROM filtered"))

	got := cte.SQL()
	if strings.Contains(got, "RECURSIVE") {
		t.Errorf("SimpleCTE should not produce RECURSIVE, got: %s", got)
	}
	if !strings.Contains(got, "WITH filtered AS (") {
		t.Errorf("SimpleCTE should produce WITH name AS, got: %s", got)
	}
}
