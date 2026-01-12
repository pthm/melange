package sqlgen

import "strings"

// =============================================================================
// Recursive List Objects Render Functions
// =============================================================================
// RenderListObjectsRecursiveFunction renders a recursive list_objects function from plan and blocks.
// This handles TTU patterns with depth tracking and recursive CTEs.
func RenderListObjectsRecursiveFunction(plan ListPlan, blocks RecursiveBlockSet) (string, error) {
	// Build CTE body from base blocks
	cteBody := renderRecursiveCTEBody(blocks)

	// Build final exclusion predicates for the CTE result
	finalExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "acc", Column: "object_id"},
		SubjectType,
		SubjectID,
	)
	exclusionPreds := finalExclusions.BuildPredicates()

	var whereExpr Expr
	if len(exclusionPreds) > 0 {
		allPreds := append([]Expr{Bool(true)}, exclusionPreds...)
		whereExpr = And(allPreds...)
	}

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "acc", Column: "object_id"}},
		FromExpr:    TableAs("accessible", "acc"),
		Where:       whereExpr,
	}

	// Build the CTE SQL using WithCTE type
	cteQuery := WithCTE{
		Recursive: true,
		CTEs: []CTEDef{{
			Name:    "accessible",
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

	// Build depth check SQL
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

// renderRecursiveCTEBody renders the CTE body from base and recursive blocks.
func renderRecursiveCTEBody(blocks RecursiveBlockSet) string {
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
