package sqlgen

import (
	"fmt"
	"slices"
	"strings"
)

// PL/pgSQL variables of the breadth-first walk.
const (
	walkFrontier = "v_frontier" // propagating objects found at the current level
	walkSeen     = "v_seen"     // every propagating object found so far
	walkStatic   = "v_static"   // seed objects that do not propagate
	walkLevel    = "v_level"
	walkDeferred = "v_deferred" // propagating seeds one resolution step away
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

	fn.Decls = append(idWalkDecls(), Decl{Name: walkStatic, Type: "TEXT[]"})
	if deferredSeeds(blocks) {
		fn.Decls = append(fn.Decls, Decl{Name: walkDeferred, Type: "TEXT[]"})
	}
	// Only the seed depends on the subject kind; the walk and final query are shared.
	seed := seedBySubjectKind(renderWalkSeed(plainBlocks), renderWalkSeed(blocks), hasUserset)
	fn.Body = renderListObjectsWalk(plan, blocks, seed)
	return fn.SQL(), nil
}

// renderComposedListObjectsQuery renders the paginated query for a plan
// without a self-referential walk: one arm per block, each filtered like the
// accessible set (accessibleObjectsSelect).
func renderComposedListObjectsQuery(plan ListPlan, blocks RecursiveBlockSet) string {
	// An arm that lists another relation whole takes the page window too: the
	// cursor always, the limit only when no filter of the accessible set runs
	// after it (it could drop rows the limit counted).
	limit := Expr(Raw("p_limit + 1"))
	if accessibleObjectsSelect(plan, blocks, nil).Where != nil {
		limit = Null{}
	}
	arms := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		block.Query = withPageWindow(block.Query, limit)
		from := TableAs("", "(\n"+formatQueryBlockSQL(block.Comments, block.Query.SQL())+"\n)", "acc")
		arms = append(arms, accessibleObjectsSelect(plan, blocks, from).SQL())
	}
	// Hoisted list_*_obj CTEs come first so the arms can reference them.
	return finishListObjectsQuery(blocks, blocks.HoistedCTEs, arms)
}

// withPageWindow passes the page window (limit, p_after) to stmt when it
// lists another relation's objects whole (SELECT ... FROM list_*_obj(subject,
// NULL, NULL)), so that call returns only the window instead of its whole set.
func withPageWindow(stmt SelectStmt, limit Expr) SelectStmt {
	call, ok := stmt.FromExpr.(FunctionCallExpr)
	if !ok || len(call.Args) != 4 || !strings.HasSuffix(call.Name, "_obj") {
		return stmt
	}
	if _, isNull := call.Args[2].(Null); !isNull {
		return stmt
	}
	if stmt.Where != nil {
		limit = Null{} // the block's own filter runs after the call
	}
	call.Args = append(slices.Clone(call.Args[:2]), limit, Raw("p_after"))
	stmt.FromExpr = call
	return stmt
}

// seedBySubjectKind returns plainSeed, or, when hasUserset, a branch that runs
// usersetSeed instead for userset query subjects.
func seedBySubjectKind(plainSeed, usersetSeed Stmt, hasUserset bool) Stmt {
	if !hasUserset {
		return plainSeed
	}
	return If{
		Cond: HasUserset{Source: SubjectID},
		Then: []Stmt{usersetSeed},
		Else: []Stmt{plainSeed},
	}
}

// deferredSeeds reports whether some propagating seed block is Indirect.
func deferredSeeds(blocks RecursiveBlockSet) bool {
	return slices.ContainsFunc(blocks.BaseBlocks, func(b TypedQueryBlock) bool { return b.Propagatable && b.Indirect })
}

// renderWalkSeed selects the objects the subject reaches without the
// self-referential walk into the frontier (propagating), v_deferred
// (propagating, from Indirect blocks, when deferredSeeds) and v_static.
func renderWalkSeed(blocks RecursiveBlockSet) Stmt {
	deferred := deferredSeeds(blocks)
	seedSQL := make([]string, 0, len(blocks.BaseBlocks))
	for _, block := range blocks.BaseBlocks {
		flags := Bool(block.Propagatable).SQL() + " AS propagatable"
		if deferred {
			flags += ", " + Bool(block.Indirect).SQL() + " AS indirect"
		}
		seedSQL = append(seedSQL, formatQueryBlockSQL(block.Comments,
			wrapQueryWithSeedFlags(block.Query.SQL(), "base", flags)))
	}

	seedIDs := func(filter string) string {
		return "array_agg(DISTINCT seed.object_id) FILTER (WHERE " + filter + ")"
	}
	columns := []string{seedIDs("seed.propagatable")}
	into := []string{walkFrontier}
	if deferred {
		columns = []string{seedIDs("seed.propagatable AND NOT seed.indirect"), seedIDs("seed.propagatable AND seed.indirect")}
		into = append(into, walkDeferred)
	}
	columns = append(columns, seedIDs("NOT seed.propagatable"))
	into = append(into, walkStatic)

	seedQuery := WithCTE{
		CTEs: blocks.HoistedCTEs,
		Query: Raw(fmt.Sprintf("SELECT %s\nINTO %s\nFROM (\n%s\n) AS seed",
			strings.Join(columns, ",\n    "), strings.Join(into, ", "), joinUnionAllBlocksSQL(seedSQL))),
	}
	return RawStmt{SQLText: seedQuery.SQL() + ";"}
}

// renderListObjectsWalk renders the breadth-first walk from seed and the final query.
//
// Deferred seeds join the walk at level 1: added to the frontier the first
// pass computes, or, without level-0 seeds, forming the frontier at level 1.
func renderListObjectsWalk(plan ListPlan, blocks RecursiveBlockSet, seed Stmt) []Stmt {
	final := []string{accessibleObjectsSelect(plan, blocks, unnestObjectIDs(walkSeen+" || "+walkStatic, "acc")).SQL()}

	stmts := []Stmt{
		Comment{Text: "Seed: objects the subject reaches without the self-referential walk"},
		seed,
	}
	step := *blocks.RecursiveBlock
	tooDeep := renderWalkTooDeep(plan, blocks)
	if deferredSeeds(blocks) {
		stepSQL := formatQueryBlockSQL(step.Comments, step.Query.SQL()) +
			"\n    UNION ALL\n    SELECT unnest(" + walkDeferred + ") WHERE " + walkLevel + " = 0"
		stmts = append(stmts,
			Comment{Text: "Seeds one resolution step away start at level 1"},
			Assign{Name: walkLevel, Value: Raw("CASE WHEN " + walkFrontier + " IS NULL THEN 1 ELSE 0 END")},
			Assign{Name: walkFrontier, Value: Raw("COALESCE(" + walkFrontier + ", " + walkDeferred + ")")},
			Assign{Name: walkSeen, Value: Raw(walkFrontier)},
		)
		stmts = append(stmts, renderIDWalkLoop(stepSQL, tooDeep)...)
	} else {
		stmts = append(stmts, renderIDWalk(0, step, tooDeep)...)
	}
	return append(stmts, ReturnQuery{Query: finishListObjectsQuery(blocks, nil, final)})
}

// idWalkDecls declares the variables of renderIDWalk.
func idWalkDecls() []Decl {
	return []Decl{
		{Name: walkFrontier, Type: "TEXT[]"},
		{Name: walkSeen, Type: "TEXT[]"},
		{Name: walkLevel, Type: "INT"},
	}
}

// seedFrontier selects the distinct IDs in the first column of the union of
// blocks into walkFrontier, ready for renderIDWalk.
func seedFrontier(blocks []TypedQueryBlock) Stmt {
	return RawStmt{SQLText: fmt.Sprintf(`SELECT array_agg(DISTINCT seed.id) INTO %s FROM (
%s
) AS seed(id);`, walkFrontier, RenderUnionBlocks(renderTypedQueryBlocks(blocks)))}
}

// renderIDWalk walks object IDs breadth first, from the IDs a preceding
// statement put in walkFrontier, at level firstLevel. Each pass replaces the
// frontier with the IDs step reaches from it (selected as one column and
// reading walkFrontier) that are not yet in walkSeen, so each object is
// expanded once, at its shortest distance. Objects remaining at
// walkDepthLimit run tooDeep. walkSeen ends up holding every object found.
func renderIDWalk(firstLevel int, step TypedQueryBlock, tooDeep []Stmt) []Stmt {
	return append([]Stmt{
		Assign{Name: walkSeen, Value: Raw(walkFrontier)},
		Assign{Name: walkLevel, Value: Int(firstLevel)},
	}, renderIDWalkLoop(formatQueryBlockSQL(step.Comments, step.Query.SQL()), tooDeep)...)
}

// renderIDWalkLoop renders the loop of renderIDWalk over the rendered stepSQL,
// once preceding statements have set walkSeen and walkLevel.
func renderIDWalkLoop(stepSQL string, tooDeep []Stmt) []Stmt {
	return []Stmt{
		Comment{Text: "Walk breadth first, expanding each object once"},
		While{
			Cond: Gt{Left: Raw("cardinality(" + walkFrontier + ")"), Right: Int(0)},
			Body: []Stmt{
				If{
					Cond: Gte{Left: Raw(walkLevel), Right: Int(walkDepthLimit)},
					Then: tooDeep,
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
	}
}

// raiseTooDeep raises M2002 (resolution too complex).
func raiseTooDeep() []Stmt {
	return []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}}
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
	if !blocks.VerifyWithCheck {
		return raiseTooDeep()
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
			Then: raiseTooDeep(),
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

// finishListObjectsQuery adds the self-candidate arm, which the accessible
// set's filters do not apply to, and paginates.
func finishListObjectsQuery(blocks RecursiveBlockSet, ctes []CTEDef, arms []string) string {
	if blocks.SelfCandidateBlock != nil {
		arms = append(arms, blocks.SelfCandidateBlock.Query.SQL())
	}
	return renderObjectsPage(ctes, arms)
}
