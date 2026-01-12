package sqlgen

// =============================================================================
// List Subjects Render Helpers
// =============================================================================
// renderUsersetFilterThenBranch builds the THEN branch statements for userset filter path.
func renderUsersetFilterThenBranch(usersetFilterPaginatedQuery string) []Stmt {
	// If there are no userset filter blocks, just return empty results
	if usersetFilterPaginatedQuery == "" {
		return []Stmt{Return{}}
	}

	// v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1)
	filterTypeAssign := Assign{
		Name: "v_filter_type",
		Value: Substring{
			Source: SubjectType,
			From:   Int(1),
			For: Sub{
				Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
				Right: Int(1),
			},
		},
	}

	// v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1)
	filterRelationAssign := Assign{
		Name: "v_filter_relation",
		Value: Substring{
			Source: SubjectType,
			From: Add{
				Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
				Right: Int(1),
			},
		},
	}

	return []Stmt{
		filterTypeAssign,
		filterRelationAssign,
		ReturnQuery{Query: usersetFilterPaginatedQuery},
	}
}

// renderRegularSubjectElseBranch builds the ELSE branch statements for regular subject type path.
func renderRegularSubjectElseBranch(plan ListPlan, regularPaginatedQuery string) []Stmt {
	var stmts []Stmt

	// Add type guard for non-userset templates
	if !plan.HasUsersetPatterns {
		typeGuard := If{
			Cond: NotIn{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			Then: []Stmt{Return{}},
		}
		stmts = append(stmts,
			Comment{Text: "Guard: return empty if subject type is not allowed by the model"},
			typeGuard,
			Comment{Text: "Regular subject type (no userset filter)"},
		)
	}

	stmts = append(stmts, ReturnQuery{Query: regularPaginatedQuery})
	return stmts
}
