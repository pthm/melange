package sqlgen

import (
	"strings"
)

// RenderListObjectsSelfRefUsersetFunction renders a list_objects function for self-referential userset patterns.
func RenderListObjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetBlockSet) (string, error) {
	cteBody := renderSelfRefUsersetCTEBody(blocks)

	exclusionConfig := buildExclusionInput(
		plan.Analysis,
		Col{Table: "me", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "me", Column: "object_id"}},
		FromExpr:    TableAs("member_expansion", "me"),
		Where:       buildWhereFromPredicates(exclusionConfig.BuildPredicates()),
	}

	cteSQL := WithCTE{
		Recursive: true,
		CTEs: []CTEDef{{
			Name:    "member_expansion",
			Columns: []string{"object_id", "depth"},
			Query:   Raw(cteBody),
		}},
		Query: finalStmt,
	}.SQL()

	query := cteSQL
	if blocks.SelfCandidateBlock != nil {
		selfCandidateSQL := renderTypedQueryBlock(*blocks.SelfCandidateBlock).Query.SQL()
		query = joinUnionBlocksSQL([]string{cteSQL, selfCandidateSQL})
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()+" (self-referential userset)"),
		Body: []Stmt{
			ReturnQuery{Query: wrapWithPagination(query, "object_id")},
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
		cteBody = appendUnionAll(cteBody, recursiveSQL)
	}

	return cteBody
}
