package sqlgen

import (
	"fmt"
	"strings"
)

// renderTypedQueryBlocks converts TypedQueryBlocks to QueryBlocks.
func renderTypedQueryBlocks(blocks []TypedQueryBlock) []QueryBlock {
	result := make([]QueryBlock, len(blocks))
	for i, block := range blocks {
		result[i] = renderTypedQueryBlock(block)
	}
	return result
}

// renderTypedQueryBlock converts a TypedQueryBlock to a QueryBlock.
func renderTypedQueryBlock(block TypedQueryBlock) QueryBlock {
	return QueryBlock{
		Comments: block.Comments,
		Query:    block.Query,
	}
}

// wrapQueryWithDepthForRender wraps a query to include depth column.
func wrapQueryWithDepthForRender(sql, depthExpr, alias string) string {
	return fmt.Sprintf("SELECT DISTINCT %s.object_id, %s AS depth\nFROM (\n%s\n) AS %s",
		alias, depthExpr, sql, alias)
}

// formatQueryBlockSQL formats a query block with comments.
func formatQueryBlockSQL(comments []string, sql string) string {
	lines := make([]string, 0, len(comments)+1)
	for _, comment := range comments {
		lines = append(lines, "    "+comment)
	}
	lines = append(lines, IndentLines(sql, "    "))
	return strings.Join(lines, "\n")
}

// joinUnionBlocksSQL joins multiple SQL blocks with UNION.
func joinUnionBlocksSQL(blocks []string) string {
	return strings.Join(blocks, "\n    UNION\n")
}

// joinUnionAllBlocksSQL joins multiple SQL blocks with UNION ALL.
// Used for recursive CTE bodies where duplicate elimination is handled elsewhere.
func joinUnionAllBlocksSQL(blocks []string) string {
	return strings.Join(blocks, "\n    UNION ALL\n")
}

// appendUnionAll joins two SQL strings with UNION ALL.
func appendUnionAll(base, additional string) string {
	return joinUnionAllBlocksSQL([]string{base, additional})
}

// appendUnion joins two SQL strings with UNION (deduplicates).
func appendUnion(base, additional string) string {
	return joinUnionBlocksSQL([]string{base, additional})
}

// buildDepthCheckSQLForRender builds the depth check SQL for recursive functions.
func buildDepthCheckSQLForRender(objectType string, linkingRelations []string) string {
	if len(linkingRelations) == 0 {
		return "    v_max_depth := 0;\n"
	}

	baseCase := SelectStmt{
		ColumnExprs: []Expr{Raw("NULL::TEXT"), Int(0)},
		Where:       Raw("FALSE"),
	}

	recursiveCase := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}, Add{Left: Col{Table: "d", Column: "depth"}, Right: Int(1)}},
		FromExpr:    TableAs("depth_check", "d"),
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On: And(
					Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(objectType)},
					In{Expr: Col{Table: "t", Column: "relation"}, Values: linkingRelations},
					Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(objectType)},
				),
			},
		},
		Where: Lt{Left: Col{Table: "d", Column: "depth"}, Right: Int(26)},
	}

	cteBody := UnionAll{
		Queries: []SQLer{
			CommentedSQL{Comment: "Base case: seed with empty set (we just need depth tracking)", Query: baseCase},
			CommentedSQL{Comment: "Track depth through all self-referential linking relations", Query: recursiveCase},
		},
	}

	cteQuery := RecursiveCTE("depth_check", []string{"object_id", "depth"}, cteBody, Raw("SELECT MAX(depth) FROM depth_check"))
	selectInto := SelectIntoVar{Query: cteQuery, Variable: "v_max_depth"}
	commented := MultiLineComment([]string{
		"Check for excessive recursion depth before running the query",
		"This matches check_permission behavior with M2002 error",
		"Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)",
	}, selectInto)

	return IndentLines(commented.SQL(), "    ") + ";\n"
}
