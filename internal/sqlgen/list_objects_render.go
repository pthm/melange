package sqlgen

// RenderListObjectsFunction renders a complete list_objects function from plan and blocks.
func RenderListObjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	pushedDown := applyObjectFilter(plan, blocks.Primary)

	queryBlocks := renderTypedQueryBlocks(blocks.Primary)
	query := RenderUnionBlocks(queryBlocks)
	paginatedQuery := plan.wrapPaginationFiltered(query, pushedDown)

	decls, prelude := objectFilterPrelude()

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls:   decls,
		Body: append(prelude,
			ReturnQuery{Query: paginatedQuery},
		),
	}
	return fn.SQL(), nil
}
