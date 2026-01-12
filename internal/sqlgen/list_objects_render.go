package sqlgen

// =============================================================================
// List Objects Render Functions
// =============================================================================
// RenderListObjectsFunction renders a complete list_objects function from plan and blocks.
func RenderListObjectsFunction(plan ListPlan, blocks BlockSet) (string, error) {
	// Convert typed blocks to QueryBlocks with rendered SQL
	queryBlocks := renderTypedQueryBlocks(blocks.Primary)

	// Render the UNION of all primary blocks
	query := RenderUnionBlocks(queryBlocks)

	// Build the function using PlpgsqlFunction
	return renderListObjectsFunctionSQL(plan, query), nil
}

// renderListObjectsFunctionSQL builds the complete list_objects function.
func renderListObjectsFunctionSQL(plan ListPlan, query string) string {
	paginatedQuery := wrapWithPagination(query, "object_id")
	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Body: []Stmt{
			ReturnQuery{Query: paginatedQuery},
		},
	}
	return fn.SQL()
}
