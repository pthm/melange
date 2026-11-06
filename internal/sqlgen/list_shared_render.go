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

// wrapQueryWithPropagatable wraps a query to include a propagatable column,
// which controls whether results from this block seed the recursive walk.
func wrapQueryWithPropagatable(sql, alias string, propagatable bool) string {
	propVal := "FALSE"
	if propagatable {
		propVal = "TRUE"
	}
	return fmt.Sprintf("SELECT %s.object_id, %s AS propagatable\nFROM (\n%s\n) AS %s",
		alias, propVal, sql, alias)
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
// Used where duplicate elimination is handled elsewhere (the list_objects
// walk seed).
func joinUnionAllBlocksSQL(blocks []string) string {
	return strings.Join(blocks, "\n    UNION ALL\n")
}
