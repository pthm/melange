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

	// Fold the same-type userset self-candidate (OpenFGA's userset-defines-itself
	// reflexivity: a userset subject "objectType:X#rel" satisfies rel on
	// objectType:X) into the main UNION rather than an IF EXISTS gate that
	// re-evaluated the identical predicate twice. Its WHERE requires
	// position('#' in p_subject_id) > 0, so the type guard below (which only
	// bails for plain subjects) never suppresses it. Mirrors the Recursive
	// renderer, which already unions its self-candidate arm.
	query := mainQuery
	if selfSQL != "" {
		query = joinUnionBlocksSQL([]string{mainQuery, selfSQL})
	}
	paginatedSQL := plan.wrapPagination(query, "object_id")

	// Build the body using plpgsql DSL types
	body := []Stmt{
		Comment{Text: "Type guard: only return results if subject type is allowed"},
		Comment{Text: "Skip the guard for userset subjects since composed inner calls handle userset subjects"},
		If{
			Cond: And(
				Eq{Left: Position{Needle: Lit("#"), Haystack: SubjectID}, Right: Int(0)},
				NotIn{Expr: SubjectType, Values: blocks.AllowedSubjectTypes},
			),
			Then: []Stmt{Return{}},
		},
		ReturnQuery{Query: paginatedSQL},
	}

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
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
