package sqlgen

import (
	"strings"
	"testing"
)

// Finding 6: the regular-path query for a self-referential userset list_subjects
// function is wrapped by wrapPaginationWildcardFirst, which itself emits an outer
// `WITH base_results AS (...)`. The inner CTE must therefore NOT also be named
// base_results (it would shadow the wrapper's). It is named `subjects` instead.
func TestSelfRefUsersetRegularQuery_InnerCTENotShadowed(t *testing.T) {
	blocks := SelfRefUsersetSubjectsBlockSet{
		RegularBlocks: []TypedQueryBlock{{
			Query: SelectStmt{
				ColumnExprs: []Expr{Alias{Expr: Lit("u1"), Name: "subject_id"}},
			},
		}},
		UsersetObjectsBaseBlock: &TypedQueryBlock{
			Query: SelectStmt{ColumnExprs: []Expr{Raw("'o' AS userset_object_id"), Raw("0 AS depth")}},
		},
	}

	for _, hasWildcard := range []bool{false, true} {
		plan := ListPlan{}
		plan.Analysis.Features.HasWildcard = hasWildcard

		sql := renderSelfRefUsersetRegularQuery(plan, blocks)

		if strings.Contains(sql, "base_results") {
			t.Errorf("hasWildcard=%v: inner CTE must not be named base_results (shadows wrapper), got:\n%s", hasWildcard, sql)
		}
		if !strings.Contains(sql, "subjects AS") {
			t.Errorf("hasWildcard=%v: expected inner CTE named `subjects`, got:\n%s", hasWildcard, sql)
		}
		if !strings.Contains(sql, "FROM subjects") {
			t.Errorf("hasWildcard=%v: tail must read from `subjects`, got:\n%s", hasWildcard, sql)
		}
	}
}
