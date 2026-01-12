package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Composed Strategy Render Functions (List Subjects)
// =============================================================================
// RenderListSubjectsComposedFunction renders a list_subjects function for composed access.
func RenderListSubjectsComposedFunction(plan ListPlan, blocks ComposedSubjectsBlockSet) (string, error) {
	// Render self-candidate query
	var selfSQL string
	if blocks.SelfBlock != nil {
		qb := renderTypedQueryBlock(*blocks.SelfBlock)
		selfSQL = qb.Query.SQL()
	}

	// Render userset filter candidate blocks
	usersetFilterBlocksSQL := make([]string, 0, len(blocks.UsersetFilterBlocks))
	for _, block := range blocks.UsersetFilterBlocks {
		qb := renderTypedQueryBlock(block)
		usersetFilterBlocksSQL = append(usersetFilterBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}
	usersetFilterCandidates := strings.Join(usersetFilterBlocksSQL, "\n    UNION\n")

	// Render regular candidate blocks
	regularBlocksSQL := make([]string, 0, len(blocks.RegularBlocks))
	for _, block := range blocks.RegularBlocks {
		qb := renderTypedQueryBlock(block)
		regularBlocksSQL = append(regularBlocksSQL, formatQueryBlockSQL(qb.Comments, qb.Query.SQL()))
	}
	regularCandidates := strings.Join(regularBlocksSQL, "\n    UNION\n")

	// Build userset filter query using WithCTE
	usersetFilterQuery := WithCTE{
		Recursive: false,
		CTEs: []CTEDef{{
			Name:  "subject_candidates",
			Query: Raw(usersetFilterCandidates),
		}},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "sc", Column: "subject_id"}},
			FromExpr:    TableAs("subject_candidates", "sc"),
			Where: CheckPermission{
				Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "sc", Column: "subject_id"}},
				Relation:    plan.Relation,
				Object:      LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow: true,
			},
		},
	}.SQL()

	// Build regular query using WithCTE (with exclusions if needed)
	var regularQueryCTE WithCTE
	regularQueryCTE = WithCTE{
		Recursive: false,
		CTEs: []CTEDef{{
			Name:  "subject_candidates",
			Query: Raw(regularCandidates),
		}},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "sc", Column: "subject_id"}},
			FromExpr:    TableAs("subject_candidates", "sc"),
		},
	}
	if blocks.HasExclusions {
		exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
		exclusionPreds := exclusions.BuildPredicates()
		if len(exclusionPreds) > 0 {
			selectStmt := regularQueryCTE.Query.(SelectStmt)
			selectStmt.Where = And(exclusionPreds...)
			regularQueryCTE.Query = selectStmt
		}
	}
	regularQuery := regularQueryCTE.SQL()

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
