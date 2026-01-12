package sqlgen

// =============================================================================
// List Subjects Render Functions
// =============================================================================
// RenderListSubjectsFunction renders a complete list_subjects function from plan and blocks.
func RenderListSubjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	// Convert typed blocks to QueryBlocks with rendered SQL
	primaryBlocks := renderTypedQueryBlocks(blocks.Primary)
	secondaryBlocks := renderTypedQueryBlocks(blocks.Secondary)

	// Build userset filter path query (when p_subject_type contains '#')
	var usersetFilterPaginatedQuery string
	if len(secondaryBlocks) > 0 || blocks.SecondarySelf != nil {
		parts := append([]QueryBlock{}, secondaryBlocks...)
		if blocks.SecondarySelf != nil {
			parts = append(parts, renderTypedQueryBlock(*blocks.SecondarySelf))
		}
		usersetFilterQuery := RenderUnionBlocks(parts)
		usersetFilterPaginatedQuery = wrapWithPaginationWildcardFirst(usersetFilterQuery)
	}

	// Render regular path query
	regularQuery := RenderUnionBlocks(primaryBlocks)
	regularPaginatedQuery := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the THEN branch (userset filter path)
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery)

	// Build the ELSE branch (regular subject type path)
	elseBranch := renderRegularSubjectElseBranch(plan, regularPaginatedQuery)

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
			Comment{Text: "Check if subject_type is a userset filter (e.g., \"document#viewer\")"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}
