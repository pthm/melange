package sqlgen

import (
	"fmt"
	"slices"
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

// wrapQueryWithSeedFlags wraps a query to select its object_id with flags,
// the columns controlling how results from this block seed the recursive walk
// (e.g. "TRUE AS propagatable").
func wrapQueryWithSeedFlags(sql, alias, flags string) string {
	return fmt.Sprintf("SELECT %s.object_id, %s\nFROM (\n%s\n) AS %s",
		alias, flags, sql, alias)
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

// renderObjectsPage renders the paginated result of a list_objects function
// from arms, each a query returning object_id, after ctes.
//
// The page is a top-k query: every arm keeps only the IDs after the cursor,
// in order, up to p_limit + 1 (no limit when p_limit is NULL), so an arm
// served by an ordered index scan stops after a page of rows instead of
// producing its whole set. The arms are combined with UNION, which merges
// their sorted outputs and drops duplicates without sorting again. An arm's
// filters must all apply inside it: a filter over the union would have to see
// rows an arm's limit already dropped. The extra row tells whether another
// page exists; next_cursor is then the page's last ID.
func renderObjectsPage(ctes []CTEDef, arms []string) string {
	limited := make([]string, len(arms))
	for i, arm := range arms {
		limited[i] = fmt.Sprintf(`(SELECT a.object_id FROM (
%s
) AS a
WHERE a.object_id > COALESCE(p_after, '')
ORDER BY a.object_id
LIMIT p_limit + 1)`, IndentLines(arm, "    "))
	}
	page := CTEDef{Name: "page", Query: Raw(fmt.Sprintf(`SELECT c.object_id
FROM (
%s
) AS c
ORDER BY c.object_id
LIMIT p_limit + 1`, IndentLines(joinUnionBlocksSQL(limited), "    ")))}
	final := Raw(`SELECT p.object_id,
    CASE WHEN p_limit > 0 AND (SELECT count(*) FROM page) > p_limit
        THEN (SELECT x.object_id FROM page x ORDER BY x.object_id OFFSET p_limit - 1 LIMIT 1)
    END AS next_cursor
FROM page p
ORDER BY p.object_id
LIMIT p_limit`)
	return WithCTE{CTEs: append(slices.Clone(ctes), page), Query: final}.SQL()
}
