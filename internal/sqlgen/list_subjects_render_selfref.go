package sqlgen

import (
	"strings"
)

// =============================================================================
// Self-Referential Userset Render Functions (List Subjects)
// =============================================================================
func RenderListSubjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) (string, error) {
	// Build userset filter path query
	usersetFilterQuery := renderSelfRefUsersetFilterQuery(blocks)
	usersetFilterQuery = trimTrailingSemicolon(usersetFilterQuery)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Build regular path query
	regularQuery := renderSelfRefUsersetRegularQuery(plan, blocks)
	regularQuery = trimTrailingSemicolon(regularQuery)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := []Stmt{
		Comment{Text: "Regular subject type: find individual subjects via recursive userset expansion"},
		ReturnQuery{Query: regularPaginatedQuery},
	}

	// Build main IF statement: check if subject_type is a userset filter
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()+" (self-referential userset)"),
		Decls: []Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

// renderSelfRefUsersetFilterQuery renders the userset filter path query with recursive CTE.
func renderSelfRefUsersetFilterQuery(blocks SelfRefUsersetSubjectsBlockSet) string {
	// Render base blocks for userset filter path
	baseBlocksSQL := make([]string, 0, len(blocks.UsersetFilterBlocks))
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}

	cteBody := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Add recursive block
	if blocks.UsersetFilterRecursiveBlock != nil {
		qb := renderTypedQueryBlock(*blocks.UsersetFilterRecursiveBlock)
		recursiveSQL := formatQueryBlockSQL(qb.Comments, qb.Query.SQL())
		cteBody = cteBody + "\n    UNION ALL\n" + recursiveSQL
	}

	// Build result blocks
	var resultBlocks []QueryBlock

	// Userset filter returns normalized references
	resultBlocks = append(resultBlocks, QueryBlock{
		Comments: []string{"-- Userset filter: return normalized userset references"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Alias{
					Expr: Concat{Parts: []Expr{Col{Table: "ue", Column: "userset_object_id"}, Lit("#"), Param("v_filter_relation")}},
					Name: "subject_id",
				},
			},
			FromExpr: TableAs("userset_expansion", "ue"),
		},
	})

	// Add self-candidate block if present
	if blocks.UsersetFilterSelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock)
		resultBlocks = append(resultBlocks, qb)
	}

	// Build the CTE with final UNION query
	finalQuery := Raw(RenderUnionBlocks(resultBlocks))

	cteQuery := RecursiveCTE(
		"userset_expansion",
		[]string{"userset_object_id", "depth"},
		Raw(cteBody),
		finalQuery,
	)

	return cteQuery.SQL()
}

// renderSelfRefUsersetRegularQuery renders the regular path query with userset_objects CTE.
func renderSelfRefUsersetRegularQuery(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) string {
	// Build userset_objects CTE
	var usersetObjectsCTE string
	if blocks.UsersetObjectsBaseBlock != nil {
		baseQB := renderTypedQueryBlock(*blocks.UsersetObjectsBaseBlock)
		baseSQL := formatQueryBlockSQL(baseQB.Comments, baseQB.Query.SQL())

		usersetObjectsCTE = baseSQL
		if blocks.UsersetObjectsRecursiveBlock != nil {
			recursiveQB := renderTypedQueryBlock(*blocks.UsersetObjectsRecursiveBlock)
			recursiveSQL := formatQueryBlockSQL(recursiveQB.Comments, recursiveQB.Query.SQL())
			usersetObjectsCTE = usersetObjectsCTE + "\n            UNION ALL\n" + recursiveSQL
		}
	}

	// Render regular blocks
	baseBlocksSQL := make([]string, 0, len(blocks.RegularBlocks))
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}

	baseResultsSQL := strings.Join(baseBlocksSQL, "\n    UNION\n")

	// Build the has_wildcard CTE query
	hasWildcardQuery := SelectStmt{
		ColumnExprs: []Expr{
			Alias{
				Expr: Raw("EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*')"),
				Name: "has_wildcard",
			},
		},
	}

	// Build the final query with wildcard handling
	wildcardTailQuery := buildUsersetWildcardTailQuery(plan.Analysis)

	// Build the full CTE query using MultiCTE
	cteQuery := MultiCTE(true, []CTEDef{
		{Name: "userset_objects", Columns: []string{"userset_object_id", "depth"}, Query: Raw(usersetObjectsCTE)},
		{Name: "base_results", Query: Raw(baseResultsSQL)},
		{Name: "has_wildcard", Query: hasWildcardQuery},
	}, wildcardTailQuery)

	return cteQuery.SQL()
}
