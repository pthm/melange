package sqlgen

// RenderListSubjectsFunction renders a complete list_subjects function from plan and blocks.
func RenderListSubjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	primaryBlocks := renderTypedQueryBlocks(blocks.Primary)
	secondaryBlocks := renderTypedQueryBlocks(blocks.Secondary)

	usersetFilterPaginatedQuery := renderUsersetFilterPaginatedQuery(secondaryBlocks, blocks.SecondarySelf)

	regularQuery := RenderUnionBlocks(primaryBlocks)
	regularPaginatedQuery := renderRegularPaginatedQuery(plan, regularQuery)

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
