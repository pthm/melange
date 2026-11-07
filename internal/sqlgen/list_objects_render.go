package sqlgen

// RenderListObjectsFunction renders a complete list_objects function from plan and blocks.
func RenderListObjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	paginatedQuery := renderObjectsPage(nil, blockArms(blocks.Primary))

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}
	return fn.SQL(), nil
}
