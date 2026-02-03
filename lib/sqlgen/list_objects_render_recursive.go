package sqlgen

import "strings"

// RenderListObjectsRecursiveFunction renders a recursive list_objects function from plan and blocks.
// This handles TTU patterns with depth tracking and recursive CTEs.
func RenderListObjectsRecursiveFunction(plan ListPlan, blocks RecursiveBlockSet) (string, error) {
	cteBody := renderRecursiveCTEBody(blocks)

	exclusionConfig := buildExclusionInput(
		plan.Analysis,
		Col{Table: "acc", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "acc", Column: "object_id"}},
		FromExpr:    TableAs("accessible", "acc"),
		Where:       And(exclusionConfig.BuildPredicates()...),
	}

	cteQuery := WithCTE{
		Recursive: true,
		CTEs: []CTEDef{{
			Name:    "accessible",
			Columns: []string{"object_id", "depth"},
			Query:   Raw(cteBody),
		}},
		Query: finalStmt,
	}

	query := cteQuery.SQL()
	if blocks.SelfCandidateBlock != nil {
		selfSQL := blocks.SelfCandidateBlock.Query.SQL()
		query = joinUnionBlocksSQL([]string{query, selfSQL})
	}

	depthCheck := buildDepthCheckSQLForRender(plan.ObjectType, blocks.SelfRefLinkingRelations)
	paginatedQuery := wrapWithPagination(query, "object_id")

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls: []Decl{
			{Name: "v_max_depth", Type: "INTEGER"},
		},
		Body: []Stmt{
			RawStmt{SQLText: strings.TrimSpace(depthCheck)},
			If{
				Cond: Raw("v_max_depth >= 25"),
				Then: []Stmt{
					Raise{Message: "resolution too complex", ErrCode: "M2002"},
				},
			},
			ReturnQuery{Query: paginatedQuery},
		},
	}

	return fn.SQL(), nil
}

func renderRecursiveCTEBody(blocks RecursiveBlockSet) string {
	baseBlocksSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		wrappedSQL := wrapQueryWithDepthForRender(block.Query.SQL(), "0", "base")
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(block.Comments, wrappedSQL))
	}

	cteBody := joinUnionBlocksSQL(baseBlocksSQL)

	if blocks.RecursiveBlock != nil {
		recursiveSQL := formatQueryBlockSQL(blocks.RecursiveBlock.Comments, blocks.RecursiveBlock.Query.SQL())
		cteBody = appendUnionAll(cteBody, recursiveSQL)
	}

	return cteBody
}
