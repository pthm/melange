package sqlgen

// RenderListObjectsFunction renders a complete list_objects function from plan and blocks.
func RenderListObjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	pushedDown := applyObjectFilter(plan, blocks.Primary)

	queryBlocks := renderTypedQueryBlocks(blocks.Primary)
	query := RenderUnionBlocks(queryBlocks)

	fn := newListObjectsFunction(plan,
		ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		ReturnQuery{Query: plan.wrapPaginationFiltered(query, pushedDown)},
	)
	return fn.SQL(), nil
}
