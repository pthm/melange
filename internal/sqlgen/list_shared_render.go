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

// wrapQueryWithDepthAndPropagatable wraps a query to include depth and propagatable columns.
// The propagatable column controls whether results from this block seed the recursive step.
func wrapQueryWithDepthAndPropagatable(sql, depthExpr, alias string, propagatable bool) string {
	propVal := "FALSE"
	if propagatable {
		propVal = "TRUE"
	}
	return fmt.Sprintf("SELECT DISTINCT %s.object_id, %s AS depth, %s AS propagatable\nFROM (\n%s\n) AS %s",
		alias, depthExpr, propVal, sql, alias)
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
