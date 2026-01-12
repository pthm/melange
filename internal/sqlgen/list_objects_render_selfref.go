package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Self-Referential Userset Render Functions (List Objects)
// =============================================================================
// RenderListObjectsSelfRefUsersetFunction renders a list_objects function for self-referential userset patterns.
func RenderListObjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetBlockSet) (string, error) {
	// Build CTE body from base blocks with depth wrapping
	cteBody := renderSelfRefUsersetCTEBody(blocks)

	// Build final exclusion predicates for the CTE result
	finalExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "me", Column: "object_id"},
		SubjectType,
		SubjectID,
	)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		whereExpr = And(exclusionPreds...)
	}

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "me", Column: "object_id"}},
		FromExpr:    TableAs("member_expansion", "me"),
		Where:       whereExpr,
	}

	// Build the CTE SQL using WithCTE type
	cteQuery := WithCTE{
		Recursive: true,
		CTEs: []CTEDef{{
			Name:    "member_expansion",
			Columns: []string{"object_id", "depth"},
			Query:   Raw(cteBody),
		}},
		Query: finalStmt,
	}
	cteSQL := cteQuery.SQL()

	// Build self-candidate SQL
	var selfCandidateSQL string
	if blocks.SelfCandidateBlock != nil {
		selfCandidateSQL = renderTypedQueryBlock(*blocks.SelfCandidateBlock).Query.SQL()
	}

	// Combine CTE and self-candidate with UNION
	var query string
	if selfCandidateSQL != "" {
		query = joinUnionBlocksSQL([]string{cteSQL, selfCandidateSQL})
	} else {
		query = cteSQL
	}

	paginatedQuery := wrapWithPagination(query, "object_id")

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_objects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s (self-referential userset)", plan.FeaturesString()),
		},
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}

	return fn.SQL(), nil
}

// renderSelfRefUsersetCTEBody renders the CTE body from base and recursive blocks.
func renderSelfRefUsersetCTEBody(blocks SelfRefUsersetBlockSet) string {
	// Render base blocks with depth wrapping
	baseBlocksSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		qb := renderTypedQueryBlock(block)
		wrappedSQL := wrapQueryWithDepthForRender(qb.Query.SQL(), "0", "base")
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, wrappedSQL))
	}

	// Join base blocks with UNION
	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block with UNION ALL if present
	if blocks.RecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.RecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.Query.SQL())
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveSQL
	}

	return cteBody
}
