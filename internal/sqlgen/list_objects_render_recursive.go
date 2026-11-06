package sqlgen

import "fmt"

// PL/pgSQL variables of the breadth-first walk.
const (
	walkFrontier = "v_frontier" // propagating objects found at the current level
	walkSeen     = "v_seen"     // every propagating object found so far
	walkStatic   = "v_static"   // seed objects that do not propagate
	walkLevel    = "v_level"
)

// walkDepthLimit is the number of levels the walk may expand, matching
// OpenFGA's resolve node limit: ListObjects fails with "resolution too complex"
// on a parent chain of 25 hops and succeeds on 24.
const walkDepthLimit = 25

// RenderListObjectsRecursiveFunction renders a recursive list_objects function from plan and blocks.
//
// A self-referential relation ("viewer from parent" in viewer) is listed by a
// breadth-first walk over its linking relations. Each object is expanded once,
// at its shortest distance from a seed, so subtrees reached by several paths
// and cycles are walked once. A chain longer than walkDepthLimit levels raises
// M2002 (resolution too complex), as check_permission does, rather than
// returning a truncated list (see renderListObjectsWalk for walks over several
// linking relations).
func RenderListObjectsRecursiveFunction(plan ListPlan, blocks RecursiveBlockSet) (string, error) {
	plainBlocks := blocks
	var hasUserset bool
	plainBlocks.BaseBlocks, hasUserset = withoutUsersetSubjectBlocks(blocks.BaseBlocks)

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListObjectsArgs(),
		Returns: ListObjectsReturns(),
		Header:  ListObjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
	}

	if blocks.RecursiveBlock == nil {
		var usersetQuery string
		if hasUserset {
			usersetQuery = renderComposedListObjectsQuery(plan, blocks)
		}
		fn.Body = listObjectsBodyBySubjectKind(renderComposedListObjectsQuery(plan, plainBlocks), usersetQuery)
		return fn.SQL(), nil
	}

	fn.Decls = []Decl{
		{Name: walkFrontier, Type: "TEXT[]"},
		{Name: walkSeen, Type: "TEXT[]"},
		{Name: walkStatic, Type: "TEXT[]"},
		{Name: walkLevel, Type: "INT"},
	}
	// Only the seed depends on the subject kind; the walk and final query are shared.
	seed := renderWalkSeed(plainBlocks)
	if hasUserset {
		seed = If{
			Cond: HasUserset{Source: SubjectID},
			Then: []Stmt{renderWalkSeed(blocks)},
			Else: []Stmt{seed},
		}
	}
	fn.Body = renderListObjectsWalk(plan, blocks, seed)
	return fn.SQL(), nil
}

// renderComposedListObjectsQuery renders the paginated query for a plan
// without a self-referential walk: the union of its blocks.
func renderComposedListObjectsQuery(plan ListPlan, blocks RecursiveBlockSet) string {
	baseBlocksSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		baseBlocksSQL = append(baseBlocksSQL, formatQueryBlockSQL(block.Comments, block.Query.SQL()))
	}
	// Hoisted list_*_obj CTEs come first so the accessible CTE (and its base
	// blocks) can reference them.
	ctes := append([]CTEDef{}, blocks.HoistedCTEs...)
	ctes = append(ctes, CTEDef{
		Name:    "accessible",
		Columns: []string{"object_id"},
		Query:   Raw(joinUnionBlocksSQL(baseBlocksSQL)),
	})
	query := WithCTE{CTEs: ctes, Query: accessibleObjectsSelect(plan, blocks, TableAs("", "accessible", "acc"))}.SQL()
	return finishListObjectsQuery(plan, blocks, query)
}

// renderWalkSeed selects the objects the subject reaches without the
// self-referential walk into the frontier (propagating) and v_static.
func renderWalkSeed(blocks RecursiveBlockSet) Stmt {
	seedSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		seedSQL = append(seedSQL, formatQueryBlockSQL(block.Comments,
			wrapQueryWithPropagatable(block.Query.SQL(), "base", block.Propagatable)))
	}
	seedQuery := WithCTE{
		CTEs: blocks.HoistedCTEs,
		Query: Raw(fmt.Sprintf(`SELECT array_agg(DISTINCT seed.object_id) FILTER (WHERE seed.propagatable),
    array_agg(DISTINCT seed.object_id) FILTER (WHERE NOT seed.propagatable)
INTO %s, %s
FROM (
%s
) AS seed`, walkFrontier, walkStatic, joinUnionAllBlocksSQL(seedSQL))),
	}
	return RawStmt{SQLText: seedQuery.SQL() + ";"}
}

// renderListObjectsWalk renders the breadth-first walk from seed and the final query.
func renderListObjectsWalk(plan ListPlan, blocks RecursiveBlockSet, seed Stmt) []Stmt {
	stepSQL := formatQueryBlockSQL(blocks.RecursiveBlock.Comments, blocks.RecursiveBlock.Query.SQL())
	final := accessibleObjectsSelect(plan, blocks, unnestObjectIDs(walkSeen+" || "+walkStatic, "acc")).SQL()

	return []Stmt{
		Comment{Text: "Seed: objects the subject reaches without the self-referential walk"},
		seed,
		Assign{Name: walkSeen, Value: Raw(walkFrontier)},
		Assign{Name: walkLevel, Value: Int(0)},
		Comment{Text: "Walk breadth first, expanding each object once"},
		While{
			Cond: Gt{Left: Raw("cardinality(" + walkFrontier + ")"), Right: Int(0)},
			Body: []Stmt{
				If{
					Cond: Gte{Left: Raw(walkLevel), Right: Int(walkDepthLimit)},
					Then: renderWalkTooDeep(plan, blocks),
				},
				RawStmt{SQLText: fmt.Sprintf(`SELECT array_agg(fresh.object_id) INTO %s FROM (
%s
    EXCEPT
    SELECT unnest(%s)
) AS fresh(object_id);`, walkFrontier, stepSQL, walkSeen)},
				Assign{Name: walkSeen, Value: Raw(walkSeen + " || " + walkFrontier)},
				Assign{Name: walkLevel, Value: Raw(walkLevel + " + 1")},
			},
		},
		ReturnQuery{Query: finishListObjectsQuery(plan, blocks, final)},
	}
}

// renderWalkTooDeep renders what happens once the walk has expanded
// walkDepthLimit levels and objects remain.
//
// An exact walk (one linking relation) has found a longer chain: raise. A walk
// over several linking relations also follows edges its objects cannot pass the
// relation through, so raise only if the subject holds a walk relation on an
// object about to be expanded (check_permission itself raises M2002 when that
// object's path is too deep). A valid path past the limit must cross the
// frontier at such an object, so otherwise the walk is complete.
func renderWalkTooDeep(plan ListPlan, blocks RecursiveBlockSet) []Stmt {
	raise := Raise{Message: "resolution too complex", ErrCode: "M2002"}
	if !blocks.VerifyWithCheck {
		return []Stmt{raise}
	}

	frontierObject := LiteralObject(plan.ObjectType, Col{Table: "f", Column: "object_id"})
	holdsWalkRelation := make([]Expr, 0, len(blocks.WalkRelations))
	for _, rel := range blocks.WalkRelations {
		holdsWalkRelation = append(holdsWalkRelation, CheckPermission{
			Schema:      plan.DatabaseSchema,
			Subject:     SubjectParams(),
			Relation:    rel,
			Object:      frontierObject,
			ExpectAllow: true,
		})
	}
	return []Stmt{
		If{
			Cond: Exists{Query: SelectStmt{
				ColumnExprs: []Expr{Int(1)},
				FromExpr:    unnestObjectIDs(walkFrontier, "f"),
				Where:       Or(holdsWalkRelation...),
			}},
			Then: []Stmt{raise},
		},
		RawStmt{SQLText: "EXIT;"},
	}
}

// unnestObjectIDs expands the object ID array expression into alias(object_id).
func unnestObjectIDs(array, alias string) UnnestAs {
	return UnnestAs{Array: Raw(array), Alias: alias, Column: "object_id"}
}

// accessibleObjectsSelect selects the distinct objects of from that pass the
// relation's exclusions (and check_permission when the walk over-approximates).
func accessibleObjectsSelect(plan ListPlan, blocks RecursiveBlockSet, from TableExpr) SelectStmt {
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "acc", Column: "object_id"}},
		FromExpr:    from,
	}
	exclusionConfig := buildExclusionInput(
		plan.Analysis,
		plan.DatabaseSchema,
		Col{Table: "acc", Column: "object_id"},
		SubjectType,
		SubjectID,
	)
	predicates := exclusionConfig.BuildPredicates()
	if blocks.VerifyWithCheck {
		predicates = append(predicates, CheckPermission{
			Schema:      plan.DatabaseSchema,
			Subject:     SubjectParams(),
			Relation:    plan.Relation,
			Object:      LiteralObject(plan.ObjectType, Col{Table: "acc", Column: "object_id"}),
			ExpectAllow: true,
		})
	}
	if len(predicates) > 0 {
		stmt.Where = And(predicates...)
	}
	return stmt
}

// finishListObjectsQuery adds the self-candidate block and pagination.
func finishListObjectsQuery(plan ListPlan, blocks RecursiveBlockSet, query string) string {
	if blocks.SelfCandidateBlock != nil {
		query = joinUnionBlocksSQL([]string{query, blocks.SelfCandidateBlock.Query.SQL()})
	}
	return plan.wrapPagination(query, "object_id")
}
