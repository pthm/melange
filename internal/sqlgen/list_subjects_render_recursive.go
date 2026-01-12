package sqlgen

// =============================================================================
// Recursive List Subjects Render Functions
// =============================================================================
// RenderListSubjectsRecursiveFunction renders a recursive list_subjects function from plan and blocks.
// This handles TTU patterns with subject_pool CTE and check_permission_internal calls.
func RenderListSubjectsRecursiveFunction(plan ListPlan, blocks SubjectsRecursiveBlockSet) (string, error) {
	// Render userset filter path query
	usersetFilterBlocks := renderTypedQueryBlocks(blocks.UsersetFilterBlocks)
	var usersetFilterParts []QueryBlock
	usersetFilterParts = append(usersetFilterParts, usersetFilterBlocks...)
	if blocks.UsersetFilterSelfBlock != nil {
		usersetFilterParts = append(usersetFilterParts, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}
	usersetFilterQuery := RenderUnionBlocks(usersetFilterParts)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Render regular path blocks
	regularBlocks := renderTypedQueryBlocks(blocks.RegularBlocks)
	ttuBlocks := renderTypedQueryBlocks(blocks.RegularTTUBlocks)

	// Build the regular query with subject_pool and base_results CTEs
	regularQuery := buildSubjectsRecursiveRegularQuery(plan, regularBlocks, ttuBlocks)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := []Stmt{
		Comment{Text: "Regular subject type: find direct subjects and expand usersets"},
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

// buildSubjectsRecursiveRegularQuery builds the regular path query with subject_pool and base_results CTEs.
func buildSubjectsRecursiveRegularQuery(plan ListPlan, regularBlocks, ttuBlocks []QueryBlock) string {
	// Build subject_pool CTE - pool of subjects matching the type constraint
	subjectPoolSQL := buildSubjectPoolCTESQL(plan)

	// Join all base blocks with UNION
	baseBlocksSQL := RenderUnionBlocks(regularBlocks)

	// Add TTU blocks to base results
	if len(ttuBlocks) > 0 {
		ttuBlocksSQL := RenderUnionBlocks(ttuBlocks)
		baseBlocksSQL = baseBlocksSQL + "\n    UNION\n" + ttuBlocksSQL
	}

	// Build the full CTE query with wildcard handling
	wildcardTailSQL := renderSubjectsWildcardTail(plan)

	return "WITH subject_pool AS (\n" +
		indentLines(subjectPoolSQL, "        ") + "\n" +
		"        ),\n" +
		"        base_results AS (\n" +
		indentLines(baseBlocksSQL, "        ") + "\n" +
		"        ),\n" +
		"        has_wildcard AS (\n" +
		"            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard\n" +
		"        )\n" +
		wildcardTailSQL
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

// renderSubjectsWildcardTail renders the final SELECT with wildcard handling.
// Note: No trailing semicolon - this gets wrapped in pagination CTEs.
func renderSubjectsWildcardTail(plan ListPlan) string {
	if plan.AllowWildcard {
		return "        -- Wildcard handling: when wildcard exists, filter non-wildcard subjects\n" +
			"        -- to only those with explicit (non-wildcard-derived) access\n" +
			"        SELECT br.subject_id\n" +
			"        FROM base_results br\n" +
			"        CROSS JOIN has_wildcard hw\n" +
			"        WHERE (NOT hw.has_wildcard)\n" +
			"           OR (br.subject_id = '*')\n" +
			"           OR (\n" +
			"               br.subject_id != '*'\n" +
			"               AND check_permission_no_wildcard(\n" +
			"                   p_subject_type,\n" +
			"                   br.subject_id,\n" +
			"                   '" + plan.Relation + "',\n" +
			"                   '" + plan.ObjectType + "',\n" +
			"                   p_object_id\n" +
			"               ) = 1\n" +
			"           )"
	}
	return "        SELECT br.subject_id FROM base_results br"
}
