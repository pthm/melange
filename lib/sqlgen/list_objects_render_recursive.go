package sqlgen

// RenderListObjectsRecursiveFunction renders a recursive list_objects function from plan and blocks.
// This handles TTU patterns with depth tracking and recursive CTEs.
func RenderListObjectsRecursiveFunction(plan ListPlan, blocks RecursiveBlockSet) (string, error) {
	recursive := blocks.RecursiveBlock != nil
	cteBody := renderRecursiveCTEBody(blocks, recursive)

	exclusionConfig := buildExclusionInput(
		plan.Analysis,
		plan.DatabaseSchema,
		Col{Table: "acc", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	finalStmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "acc", Column: "object_id"}},
		FromExpr:    TableAs("", "accessible", "acc"),
	}
	if predicates := exclusionConfig.BuildPredicates(); len(predicates) > 0 {
		finalStmt.Where = And(predicates...)
	}

	// Only genuinely self-referential relations need the recursive machinery.
	// Without a recursive block, depth/propagatable are dead and RECURSIVE is unused.
	cteColumns := []string{"object_id"}
	if recursive {
		cteColumns = []string{"object_id", "depth", "propagatable"}
	}
	cteQuery := WithCTE{
		Recursive: recursive,
		CTEs: []CTEDef{{
			Name:    "accessible",
			Columns: cteColumns,
			Query:   Raw(cteBody),
		}},
		Query: finalStmt,
	}

	query := cteQuery.SQL()
	if blocks.SelfCandidateBlock != nil {
		selfSQL := blocks.SelfCandidateBlock.Query.SQL()
		query = joinUnionBlocksSQL([]string{query, selfSQL})
	}

	paginatedQuery := plan.wrapPagination(query, "object_id")

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		// Recursion is bounded inside the accessible CTE (WHERE a.depth < 25).
		// list_objects is best-effort to that depth: chains deeper than the bound
		// are truncated rather than raising M2002 the way check_permission does
		// (a pathological edge case that a global pre-check could not detect
		// per-query anyway without re-walking the whole graph on every call).
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}

	return fn.SQL(), nil
}

func renderRecursiveCTEBody(blocks RecursiveBlockSet, recursive bool) string {
	baseBlocksSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		var wrappedSQL string
		if recursive {
			wrappedSQL = wrapQueryWithDepthAndPropagatable(block.Query.SQL(), "0", "base", block.Propagatable)
		} else {
			wrappedSQL = block.Query.SQL()
		}
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(block.Comments, wrappedSQL))
	}

	cteBody := joinUnionBlocksSQL(baseBlocksSQL)

	if blocks.RecursiveBlock != nil {
		// Recursive block emits its own propagatable column (TRUE) via the Columns field
		recursiveSQL := formatQueryBlockSQL(blocks.RecursiveBlock.Comments, blocks.RecursiveBlock.Query.SQL())
		cteBody = appendUnionAll(cteBody, recursiveSQL)
	}

	return cteBody
}
