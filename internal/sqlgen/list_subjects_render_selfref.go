package sqlgen

// =============================================================================
// Self-Referential Userset Render Functions (List Subjects)
// =============================================================================

// RenderListSubjectsSelfRefUsersetFunction renders a list_subjects function for
// self-referential userset patterns ([group#member] on group.member).
//
// Both paths walk down the nested usersets breadth first with renderIDWalk,
// from the usersets named on the object: each is expanded once, and nesting
// walkDepthLimit hops deep raises M2002, as OpenFGA's ListUsers does, instead
// of returning a truncated list.
func RenderListSubjectsSelfRefUsersetFunction(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) (string, error) {
	usersetFilterPaginatedQuery := plan.wrapPaginationWildcardFirst(
		trimTrailingSemicolon(renderSelfRefUsersetFilterQuery(blocks)),
	)
	regularPaginatedQuery := plan.wrapPaginationWildcardFirst(
		trimTrailingSemicolon(renderSelfRefUsersetRegularQuery(plan, blocks)),
	)

	// Build the THEN branch (userset filter path): walk before its RETURN QUERY.
	thenBranch := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery,
		renderUsersetWalk(blocks.UsersetFilterBlocks, *blocks.UsersetFilterRecursiveBlock)...)

	// Build the ELSE branch (regular subject type path). Only subject types the
	// relation admits can match, so others skip the walk (and its depth limit).
	regularWalk := renderUsersetWalk([]TypedQueryBlock{*blocks.UsersetObjectsBaseBlock}, *blocks.UsersetObjectsRecursiveBlock)
	if len(plan.AllowedSubjectTypes) > 0 {
		regularWalk = []Stmt{If{Cond: In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}, Then: regularWalk}}
	}
	elseBranch := []Stmt{Comment{Text: "Regular subject type: find individual subjects via recursive userset expansion"}}
	elseBranch = append(elseBranch, regularWalk...)
	elseBranch = append(elseBranch, ReturnQuery{Query: regularPaginatedQuery})

	// Build main IF statement: check if subject_type is a userset filter
	mainIf := If{
		Cond: Gt{
			Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
			Right: Int(0),
		},
		Then: thenBranch,
		Else: elseBranch,
	}

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()+" (self-referential userset)"),
		Decls: append([]Decl{
			{Name: "v_filter_type", Type: "TEXT"},
			{Name: "v_filter_relation", Type: "TEXT"},
		}, idWalkDecls()...),
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			mainIf,
		},
	}

	return fn.SQL(), nil
}

// renderUsersetWalk seeds the walk with the usersets base selects and walks
// down with step.
func renderUsersetWalk(base []TypedQueryBlock, step TypedQueryBlock) []Stmt {
	return append([]Stmt{seedFrontier(base)}, renderIDWalk(1, step, raiseTooDeep())...)
}

// walkedUsersets is the CTE body reading the usersets the walk found.
func walkedUsersets() SQLer {
	return SelectStmt{ColumnExprs: []Expr{Raw("unnest(" + walkSeen + ") AS userset_object_id")}}
}

func renderSelfRefUsersetFilterQuery(blocks SelfRefUsersetSubjectsBlockSet) string {
	resultBlocks := []QueryBlock{{
		Comments: []string{"-- Userset filter: return normalized userset references"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Alias{
					Expr: Concat{Parts: []Expr{Col{Table: "ue", Column: "userset_object_id"}, Lit("#"), Param("v_filter_relation")}},
					Name: "subject_id",
				},
			},
			FromExpr: TableAs("", "userset_expansion", "ue"),
		},
	}}

	if blocks.UsersetFilterSelfBlock != nil {
		resultBlocks = append(resultBlocks, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}

	return WithCTE{
		CTEs:  []CTEDef{{Name: "userset_expansion", Columns: []string{"userset_object_id"}, Query: walkedUsersets()}},
		Query: Raw(RenderUnionBlocks(resultBlocks)),
	}.SQL()
}

func renderSelfRefUsersetRegularQuery(plan ListPlan, blocks SelfRefUsersetSubjectsBlockSet) string {
	baseResultsSQL := RenderUnionBlocks(renderTypedQueryBlocks(blocks.RegularBlocks))

	ctes := []CTEDef{
		{Name: "userset_objects", Columns: []string{"userset_object_id"}, Query: walkedUsersets()},
		// subjects is referenced by the has_wildcard EXISTS (when emitted) and
		// the outer tail SELECT, so materialize to compute it once. Named "subjects"
		// (not "base_results") to avoid shadowing the outer pagination-wrapper CTE.
		{Name: "subjects", Query: Raw(baseResultsSQL), Materialized: plan.MaterializeCTEs()},
	}

	// The has_wildcard CTE is read only by buildUsersetWildcardTailQuery's CROSS
	// JOIN, which it emits only when Features.HasWildcard — gate the CTE on the
	// same flag so def and ref stay lockstep (an unreferenced CTE is dead codegen).
	if plan.Analysis.Features.HasWildcard {
		ctes = append(ctes, CTEDef{Name: "has_wildcard", Query: SelectStmt{
			ColumnExprs: []Expr{
				Alias{
					Expr: Raw("EXISTS (SELECT 1 FROM subjects br WHERE br.subject_id = '*')"),
					Name: "has_wildcard",
				},
			},
		}})
	}

	cteQuery := MultiCTE(ctes, buildUsersetWildcardTailQuery(plan.Analysis, plan.DatabaseSchema))

	return cteQuery.SQL()
}
