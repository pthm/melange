package sqlgen

// =============================================================================
// Intersection List Subjects Render Functions
// =============================================================================
// RenderListSubjectsIntersectionFunction renders an intersection list_subjects function from plan and blocks.
// Intersection gathers candidates then filters with check_permission at the end.
func RenderListSubjectsIntersectionFunction(plan ListPlan, blocks SubjectsIntersectionBlockSet) (string, error) {
	// Render regular candidate blocks
	regularCandidateBlocks := renderTypedQueryBlocks(blocks.RegularCandidateBlocks)
	regularCandidatesSQL := RenderUnionBlocks(regularCandidateBlocks)

	// Build regular query with check_permission filter
	regularQuery := buildIntersectionRegularQuery(plan, regularCandidatesSQL)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Render userset filter candidate blocks
	usersetCandidateBlocks := renderTypedQueryBlocks(blocks.UsersetFilterCandidateBlocks)
	usersetCandidatesSQL := RenderUnionBlocks(usersetCandidateBlocks)

	// Build userset filter query with check_permission filter and self block
	usersetFilterQuery := buildIntersectionUsersetFilterQuery(plan, usersetCandidatesSQL, blocks.UsersetFilterSelfBlock)
	usersetFilterPaginatedQuery := wrapWithPaginationWildcardFirst(usersetFilterQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := renderIntersectionRegularElseBranch(plan, regularPaginatedQuery)

	// Build main IF statement
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

// buildIntersectionRegularQuery builds the regular path query for intersection.
// It wraps candidates in a CTE and filters with check_permission.
func buildIntersectionRegularQuery(plan ListPlan, candidatesSQL string) string {
	// Build the filtered_candidates CTE query
	filteredCandidatesQuery := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "c", Column: "subject_id"}},
		FromExpr:    TableAs("subject_candidates", "c"),
		Where: CheckPermissionExpr(
			"check_permission",
			SubjectRef{Type: SubjectType, ID: Col{Table: "c", Column: "subject_id"}},
			plan.Relation,
			LiteralObject(plan.ObjectType, ObjectID),
			true,
		),
	}

	// Build the final query that selects from filtered_candidates
	finalQuery := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "fc", Column: "subject_id"}},
		FromExpr:    TableAs("filtered_candidates", "fc"),
	}

	// Build the full CTE query using MultiCTE
	cteQuery := MultiCTE(false, []CTEDef{
		{Name: "subject_candidates", Query: Raw(candidatesSQL)},
		{Name: "filtered_candidates", Query: filteredCandidatesQuery},
	}, finalQuery)

	return cteQuery.SQL()
}

// renderSubjectsIntersectionWildcardTail renders the wildcard handling for intersection.
// Unlike simple wildcard relations, intersections require all parts to be satisfied.
// The check_permission filter already correctly handles intersection logic, so we
// return filtered_candidates directly without additional wildcard filtering.
// For example: `viewer: [user:*] and allowed` - a user who gets viewer via the
// wildcard AND is in allowed should be returned, even though check_permission_no_wildcard
// would fail (since there's no direct viewer tuple for that user).
func renderSubjectsIntersectionWildcardTail(_ ListPlan) string {
	// For intersections, return all filtered candidates directly.
	// The check_permission filter in filtered_candidates already handles
	// intersection logic correctly, including wildcard components.
	return "\n        SELECT fc.subject_id FROM filtered_candidates fc"
}

// buildIntersectionUsersetFilterQuery builds the userset filter path query for intersection.
func buildIntersectionUsersetFilterQuery(plan ListPlan, candidatesSQL string, selfBlock *TypedQueryBlock) string {
	// Build the main query with check_permission filter
	mainQuery := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "c", Column: "subject_id"}},
		FromExpr:    TableAs("userset_candidates", "c"),
		Where: CheckPermissionExpr(
			"check_permission",
			SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "c", Column: "subject_id"}},
			plan.Relation,
			LiteralObject(plan.ObjectType, ObjectID),
			true,
		),
	}

	// Build the CTE
	cteQuery := SimpleCTE("userset_candidates", Raw(candidatesSQL), mainQuery)

	// If there's a self block, append it with UNION using UnionAll type
	if selfBlock != nil {
		rendered := renderTypedQueryBlock(*selfBlock)
		unionQuery := UnionAll{
			Queries: []SQLer{
				cteQuery,
				Raw(formatQueryBlock(rendered.Comments, rendered.Query.SQL())),
			},
		}
		return unionQuery.SQL()
	}

	return cteQuery.SQL()
}

// renderIntersectionRegularElseBranch builds the ELSE branch for intersection regular path.
func renderIntersectionRegularElseBranch(plan ListPlan, regularPaginatedQuery string) []Stmt {
	stmts := make([]Stmt, 0, 4)

	// Add type guard
	typeGuard := If{
		Cond: NotIn{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		Then: []Stmt{Return{}},
	}
	stmts = append(stmts,
		Comment{Text: "Regular subject type: gather candidates and filter with check_permission"},
		Comment{Text: "Guard: return empty if subject type is not allowed by the model"},
		typeGuard,
		ReturnQuery{Query: regularPaginatedQuery},
	)
	return stmts
}
