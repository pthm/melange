package sqlgen

// =============================================================================
// Recursive List Subjects Render Functions
// =============================================================================
// RenderListSubjectsRecursiveFunction renders a recursive list_subjects function from plan and blocks.
// This handles TTU patterns with subject_pool CTE and check_permission_internal calls.
func RenderListSubjectsRecursiveFunction(plan ListPlan, blocks SubjectsRecursiveBlockSet) (string, error) {
	usersetFilterPaginatedQuery := buildUsersetFilterQuery(blocks)
	regularPaginatedQuery := buildRegularPaginatedQuery(plan, blocks)

	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: renderUsersetFilterThenBranch(usersetFilterPaginatedQuery),
		Else: []Stmt{
			Comment{Text: "Regular subject type: find direct subjects and expand usersets"},
			ReturnQuery{Query: regularPaginatedQuery},
		},
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
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

func buildUsersetFilterQuery(blocks SubjectsRecursiveBlockSet) string {
	usersetFilterParts := renderTypedQueryBlocks(blocks.UsersetFilterBlocks)
	if blocks.UsersetFilterSelfBlock != nil {
		usersetFilterParts = append(usersetFilterParts, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}
	return wrapWithPaginationWildcardFirst(RenderUnionBlocks(usersetFilterParts))
}

func buildRegularPaginatedQuery(plan ListPlan, blocks SubjectsRecursiveBlockSet) string {
	regularBlocks := renderTypedQueryBlocks(blocks.RegularBlocks)
	ttuBlocks := renderTypedQueryBlocks(blocks.RegularTTUBlocks)
	regularQuery := buildSubjectsRecursiveRegularQuery(plan, regularBlocks, ttuBlocks)
	return wrapWithPaginationWildcardFirst(regularQuery)
}

// buildSubjectsRecursiveRegularQuery builds the regular path query with subject_pool and base_results CTEs.
func buildSubjectsRecursiveRegularQuery(plan ListPlan, regularBlocks, ttuBlocks []QueryBlock) string {
	// Build subject_pool CTE - pool of subjects matching the type constraint
	subjectPoolSQL := buildSubjectPoolCTESQL(plan)

	// Join all base blocks with UNION
	baseBlocksSQL := RenderUnionBlocks(regularBlocks)

	// Add TTU blocks to base results
	if len(ttuBlocks) > 0 {
		ttuBlocksSQL := RenderUnionBlocks(ttuBlocks)
		baseBlocksSQL = joinUnionBlocksSQL([]string{baseBlocksSQL, ttuBlocksSQL})
	}

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
	wildcardTailQuery := buildSubjectsWildcardTailQuery(plan)

	// Build the full CTE query using MultiCTE
	cteQuery := MultiCTE(false, []CTEDef{
		{Name: "subject_pool", Query: Raw(subjectPoolSQL)},
		{Name: "base_results", Query: Raw(baseBlocksSQL)},
		{Name: "has_wildcard", Query: hasWildcardQuery},
	}, wildcardTailQuery)

	return cteQuery.SQL()
}

// buildSubjectPoolCTESQL builds the subject_pool CTE SQL.
func buildSubjectPoolCTESQL(plan ListPlan) string {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		Select("t.subject_id").
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}).
		Distinct()

	if excludeWildcard {
		q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	return q.SQL()
}

// buildSubjectsWildcardTailQuery builds the final SELECT with wildcard handling as a typed query.
// Note: No trailing semicolon - this gets wrapped in pagination CTEs.
func buildSubjectsWildcardTailQuery(plan ListPlan) SQLer {
	if plan.AllowWildcard {
		// Build the wildcard handling query with permission check
		return SelectStmt{
			ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
			FromExpr:    TableAs("base_results", "br"),
			Joins: []JoinClause{
				{Type: "CROSS", Table: "has_wildcard", Alias: "hw"},
			},
			Where: Or(
				NotExpr{Expr: Col{Table: "hw", Column: "has_wildcard"}},
				Eq{Left: Col{Table: "br", Column: "subject_id"}, Right: Lit("*")},
				And(
					Ne{Left: Col{Table: "br", Column: "subject_id"}, Right: Lit("*")},
					NoWildcardPermissionCheckCall(plan.Relation, plan.ObjectType, Col{Table: "br", Column: "subject_id"}, ObjectID),
				),
			),
		}
	}
	return SelectStmt{
		ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
		FromExpr:    TableAs("base_results", "br"),
	}
}
