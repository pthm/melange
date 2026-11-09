package sqlgen

// RenderListObjectsSelfRefUsersetFunction renders a list_objects function for self-referential userset patterns.
//
// The objects are found by walking up nested usersets ([group#member] on
// group.member) breadth first from the subject's direct memberships, with
// renderIDWalk: each object is expanded once, and nesting walkDepthLimit
// levels deep raises M2002, as OpenFGA's ListObjects does, instead of
// returning a truncated list.
func RenderListObjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetBlockSet) (string, error) {
	plainBase, hasUserset := withoutUsersetSubjectBlocks(blocks.BaseBlocks)

	// Only the seed depends on the subject kind; the walk and final query are shared.
	seed := seedBySubjectKind(seedFrontier(plainBase), seedFrontier(blocks.BaseBlocks), hasUserset)
	walk := renderIDWalk(0, *blocks.RecursiveBlock, raiseTooDeep())
	body := make([]Stmt, 0, len(walk)+3)
	body = append(body, Comment{Text: "Seed: objects the subject is a direct member of"}, seed)
	body = append(body, walk...)
	body = append(body, ReturnQuery{Query: renderSelfRefUsersetListObjectsQuery(plan, blocks)})

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()+" (self-referential userset)"),
		Decls:   idWalkDecls(),
		Body:    body,
	}

	return fn.SQL(), nil
}

// renderSelfRefUsersetListObjectsQuery renders the paginated query over the
// objects the walk found. A userset subject's own object (group:g0 for
// group:g0#member) is added after the exclusions: OpenFGA holds a userset to
// be a member of its own object even when excluded.
func renderSelfRefUsersetListObjectsQuery(plan ListPlan, blocks SelfRefUsersetBlockSet) string {
	exclusionConfig := buildExclusionInput(
		plan.Analysis,
		plan.DatabaseSchema,
		Col{Table: "me", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	arms := []string{SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "me", Column: "object_id"}},
		FromExpr:    unnestObjectIDs(walkSeen, "me"),
		Where:       buildWhereFromPredicates(exclusionConfig.BuildPredicates()),
	}.SQL()}
	if blocks.SelfCandidateBlock != nil {
		arms = append(arms, renderTypedQueryBlock(*blocks.SelfCandidateBlock).Query.SQL())
	}
	return renderObjectsPage(nil, arms)
}
