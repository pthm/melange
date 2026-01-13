package sqlgen

import "fmt"

// RenderListSubjectsComposedFunction renders a list_subjects function for composed access.
func RenderListSubjectsComposedFunction(plan ListPlan, blocks ComposedSubjectsBlockSet) (string, error) {
	var selfSQL string
	if blocks.SelfBlock != nil {
		selfSQL = blocks.SelfBlock.Query.SQL()
	}

	usersetFilterCandidates := renderBlocksUnion(blocks.UsersetFilterBlocks)
	regularCandidates := renderBlocksUnion(blocks.RegularBlocks)

	usersetFilterQuery := buildCandidatesCTE(usersetFilterCandidates, SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "sc", Column: "subject_id"}},
		FromExpr:    TableAs("subject_candidates", "sc"),
		Where: CheckPermission{
			Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "sc", Column: "subject_id"}},
			Relation:    plan.Relation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		},
	}).SQL()

	regularQuery := buildRegularQueryWithExclusions(plan, blocks, regularCandidates)

	// Wrap queries with pagination
	selfPaginatedSQL := wrapWithPaginationWildcardFirst(selfSQL)
	usersetFilterPaginatedSQL := wrapWithPaginationWildcardFirst(usersetFilterQuery)
	regularPaginatedSQL := wrapWithPaginationWildcardFirst(regularQuery)

	// Build the body using plpgsql DSL types
	body := []Stmt{
		// Determine if this is a userset filter request
		Assign{Name: "v_is_userset_filter", Value: Gt{Left: Position{Needle: Lit("#"), Haystack: SubjectType}, Right: Int(0)}},
		If{
			Cond: Param("v_is_userset_filter"),
			Then: []Stmt{
				// Extract filter type and relation from userset subject type
				Assign{Name: "v_filter_type", Value: Raw("split_part(p_subject_type, '#', 1)")},
				Assign{Name: "v_filter_relation", Value: Raw("split_part(p_subject_type, '#', 2)")},
				Comment{Text: "Self-candidate: when filter type matches object type"},
				If{
					Cond: Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
					Then: []Stmt{
						If{
							Cond: Exists{Query: Raw(selfSQL)},
							Then: []Stmt{
								ReturnQuery{Query: selfPaginatedSQL},
								Return{},
							},
						},
					},
				},
				Comment{Text: "Userset filter case"},
				ReturnQuery{Query: usersetFilterPaginatedSQL},
			},
			Else: []Stmt{
				Comment{Text: "Direct subject type case"},
				If{
					Cond: NotIn{Expr: SubjectType, Values: blocks.AllowedSubjectTypes},
					Then: []Stmt{Return{}},
				},
				ReturnQuery{Query: regularPaginatedSQL},
			},
		},
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_subjects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("Indirect anchor: %s.%s via %s", blocks.AnchorType, blocks.AnchorRelation, blocks.FirstStepType),
		},
		Decls: []Decl{
			{Name: "v_is_userset_filter", Type: "BOOLEAN"},
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		},
		Body: body,
	}
	return fn.SQL(), nil
}

func renderBlocksUnion(blocks []TypedQueryBlock) string {
	parts := make([]string, len(blocks))
	for i, block := range blocks {
		parts[i] = formatQueryBlockSQL(block.Comments, block.Query.SQL())
	}
	return joinUnionBlocksSQL(parts)
}

func buildCandidatesCTE(candidates string, query SelectStmt) WithCTE {
	return WithCTE{
		Recursive: false,
		CTEs: []CTEDef{{
			Name:  "subject_candidates",
			Query: Raw(candidates),
		}},
		Query: query,
	}
}

func buildRegularQueryWithExclusions(plan ListPlan, blocks ComposedSubjectsBlockSet, candidates string) string {
	query := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "sc", Column: "subject_id"}},
		FromExpr:    TableAs("subject_candidates", "sc"),
	}

	if blocks.HasExclusions {
		exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
		if preds := exclusions.BuildPredicates(); len(preds) > 0 {
			query.Where = And(preds...)
		}
	}

	return buildCandidatesCTE(candidates, query).SQL()
}
