package sqlgen

import "fmt"

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
	wildcardTail := renderSubjectsIntersectionWildcardTail(plan)

	return fmt.Sprintf(`WITH subject_candidates AS (
%s
        ),
        filtered_candidates AS (
            SELECT DISTINCT c.subject_id
            FROM subject_candidates c
            WHERE check_permission(p_subject_type, c.subject_id, '%s', '%s', p_object_id) = 1
        )%s`,
		indentLines(candidatesSQL, "        "),
		plan.Relation,
		plan.ObjectType,
		wildcardTail,
	)
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
	var selfSQL string
	if selfBlock != nil {
		rendered := renderTypedQueryBlock(*selfBlock)
		selfSQL = fmt.Sprintf(`

        UNION

%s`,
			formatQueryBlock(rendered.Comments, rendered.Query.SQL()))
	}

	return fmt.Sprintf(`WITH userset_candidates AS (
%s
        )
        SELECT DISTINCT c.subject_id
        FROM userset_candidates c
        WHERE check_permission(v_filter_type, c.subject_id, '%s', '%s', p_object_id) = 1%s`,
		candidatesSQL,
		plan.Relation,
		plan.ObjectType,
		selfSQL,
	)
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
