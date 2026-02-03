package sqlgen

import "strings"

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

// buildSubjectsRecursiveRegularQuery builds the regular path query with parent_closure and base_results CTEs.
func buildSubjectsRecursiveRegularQuery(plan ListPlan, regularBlocks, ttuBlocks []QueryBlock) string {
	// Join all base blocks with UNION
	baseBlocksSQL := RenderUnionBlocks(regularBlocks)

	// Build CTEs list
	ctes := []CTEDef{}

	// Check if any TTU blocks need subject_pool (complex parent relations)
	needsSubjectPool := false
	needsParentClosure := false
	if len(ttuBlocks) > 0 {
		ttuBlocksSQL := RenderUnionBlocks(ttuBlocks)
		// Check if any TTU block uses subject_pool or parent_closure
		needsSubjectPool = containsSubjectPool(ttuBlocksSQL)
		needsParentClosure = containsParentClosure(ttuBlocksSQL)

		baseBlocksSQL = joinUnionBlocksSQL([]string{baseBlocksSQL, ttuBlocksSQL})
	}

	// Add subject_pool CTE if needed (for complex parent relations)
	if needsSubjectPool {
		subjectPoolSQL := buildSubjectPoolCTESQL(plan)
		ctes = append(ctes, CTEDef{Name: "subject_pool", Query: Raw(subjectPoolSQL)})
	}

	// Add parent_closure CTE if needed (for simple parent relations with optimization)
	if needsParentClosure {
		parentClosureSQL := buildParentClosureCTESQL(plan)
		ctes = append(ctes, CTEDef{Name: "parent_closure", Query: Raw(parentClosureSQL)})
	}

	// Add base_results CTE
	ctes = append(ctes, CTEDef{Name: "base_results", Query: Raw(baseBlocksSQL)})

	// Build the has_wildcard CTE query
	hasWildcardQuery := SelectStmt{
		ColumnExprs: []Expr{
			Alias{
				Expr: Raw("EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*')"),
				Name: "has_wildcard",
			},
		},
	}
	ctes = append(ctes, CTEDef{Name: "has_wildcard", Query: hasWildcardQuery})

	// Build the final query with wildcard handling
	wildcardTailQuery := buildSubjectsWildcardTailQuery(plan)

	// Build the full CTE query - use RECURSIVE if we have parent_closure
	recursive := needsParentClosure
	cteQuery := MultiCTE(recursive, ctes, wildcardTailQuery)

	return cteQuery.SQL()
}

// containsSubjectPool checks if SQL contains reference to subject_pool table.
func containsSubjectPool(sql string) bool {
	return strings.Contains(sql, "subject_pool")
}

// containsParentClosure checks if SQL contains reference to parent_closure table.
func containsParentClosure(sql string) bool {
	return strings.Contains(sql, "parent_closure")
}

// buildParentClosureCTESQL builds the recursive parent_closure CTE SQL.
// This CTE walks the parent chain starting from the target object.
// Returns (object_type, object_id, depth) for each ancestor in the parent chain.
func buildParentClosureCTESQL(plan ListPlan) string {
	// Get the parent relations info from the plan
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return ""
	}

	// For simplicity, use the first parent relation's linking relation
	// TODO: Handle multiple different linking relations if needed
	parent := parentRelations[0]

	// Base case: immediate parents (subject becomes the parent object)
	baseWhere := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		baseWhere = append(baseWhere, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	baseQuery := SelectStmt{
		ColumnExprs: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Raw("0 AS depth"),
		},
		FromExpr: TableAs("melange_tuples", "link"),
		Where:    And(baseWhere...),
	}

	// Recursive case: walk parent chains
	// Join on the parent's subject (which becomes the new object to search from)
	recursiveQuery := SelectStmt{
		ColumnExprs: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Raw("p.depth + 1 AS depth"),
		},
		FromExpr: TableAs("parent_closure", "p"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "link",
			On: And(
				Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Col{Table: "p", Column: "subject_type"}},
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: Col{Table: "p", Column: "subject_id"}},
			),
		}},
		Where: And(
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
			Lt{Left: Col{Table: "p", Column: "depth"}, Right: Int(25)},
		),
	}

	// Union base and recursive cases
	return baseQuery.SQL() + "\n        UNION\n        " + recursiveQuery.SQL()
}

// buildSubjectPoolCTESQL builds the subject_pool CTE SQL.
// Note: This is deprecated in favor of parent_closure for TTU cases.
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
