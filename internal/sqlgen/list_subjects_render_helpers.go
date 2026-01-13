package sqlgen

// renderUsersetFilterPaginatedQuery builds the paginated query for the userset filter path.
// Returns empty string if there are no secondary blocks.
func renderUsersetFilterPaginatedQuery(secondaryBlocks []QueryBlock, secondarySelf *TypedQueryBlock) string {
	if len(secondaryBlocks) == 0 && secondarySelf == nil {
		return ""
	}
	if secondarySelf != nil {
		secondaryBlocks = append(secondaryBlocks, renderTypedQueryBlock(*secondarySelf))
	}
	return wrapWithPaginationWildcardFirst(RenderUnionBlocks(secondaryBlocks))
}

// renderUsersetFilterThenBranch builds the THEN branch statements for userset filter path.
func renderUsersetFilterThenBranch(usersetFilterPaginatedQuery string) []Stmt {
	if usersetFilterPaginatedQuery == "" {
		return []Stmt{Return{}}
	}

	hashPos := Position{Needle: Lit("#"), Haystack: SubjectType}
	return []Stmt{
		Assign{
			Name: "v_filter_type",
			Value: Substring{
				Source: SubjectType,
				From:   Int(1),
				For:    Sub{Left: hashPos, Right: Int(1)},
			},
		},
		Assign{
			Name: "v_filter_relation",
			Value: Substring{
				Source: SubjectType,
				From:   Add{Left: hashPos, Right: Int(1)},
			},
		},
		ReturnQuery{Query: usersetFilterPaginatedQuery},
	}
}

// renderRegularSubjectElseBranch builds the ELSE branch statements for regular subject type path.
func renderRegularSubjectElseBranch(plan ListPlan, regularPaginatedQuery string) []Stmt {
	if plan.HasUsersetPatterns {
		return []Stmt{ReturnQuery{Query: regularPaginatedQuery}}
	}

	return []Stmt{
		Comment{Text: "Guard: return empty if subject type is not allowed by the model"},
		If{
			Cond: NotIn{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			Then: []Stmt{Return{}},
		},
		Comment{Text: "Regular subject type (no userset filter)"},
		ReturnQuery{Query: regularPaginatedQuery},
	}
}
