package sqlgen

import "fmt"

// =============================================================================
// Composed Strategy Render Functions (List Objects)
// =============================================================================
// RenderListObjectsComposedFunction renders a list_objects function for composed access.
// Composed functions handle indirect anchor patterns (TTU and userset composition).
func RenderListObjectsComposedFunction(plan ListPlan, blocks ComposedObjectsBlockSet) (string, error) {
	var selfSQL string
	if blocks.SelfBlock != nil {
		selfSQL = renderTypedQueryBlock(*blocks.SelfBlock).Query.SQL()
	}

	mainBlocks := renderTypedQueryBlocks(blocks.MainBlocks)
	mainQuery := RenderUnionBlocks(mainBlocks)

	// Wrap with pagination
	selfPaginatedSQL := wrapWithPagination(selfSQL, "object_id")
	mainPaginatedSQL := wrapWithPagination(mainQuery, "object_id")

	// Build the body using plpgsql DSL types
	body := []Stmt{
		Comment{Text: "Self-candidate check: when subject is a userset on the same object type"},
		If{
			Cond: Exists{Query: Raw(selfSQL)},
			Then: []Stmt{
				ReturnQuery{Query: selfPaginatedSQL},
				Return{},
			},
		},
		Comment{Text: "Type guard: only return results if subject type is allowed"},
		Comment{Text: "Skip the guard for userset subjects since composed inner calls handle userset subjects"},
		If{
			Cond: And(
				Eq{Left: Position{Needle: Lit("#"), Haystack: SubjectID}, Right: Int(0)},
				NotIn{Expr: SubjectType, Values: blocks.AllowedSubjectTypes},
			),
			Then: []Stmt{Return{}},
		},
		ReturnQuery{Query: mainPaginatedSQL},
	}

	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_objects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("Indirect anchor: %s.%s via %s", blocks.AnchorType, blocks.AnchorRelation, blocks.FirstStepType),
		},
		Body: body,
	}
	return fn.SQL(), nil
}
