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

// RenderListObjectsRecursiveFunction renders the list_objects function of a
// self-referential relation ("viewer from parent" in viewer), whose blocks
// have a RecursiveBlock. Other recursive plans are listed over their
// accessible function (recursiveAccessibleObjectsQuery).
//
// The relation is listed by a breadth-first walk over its linking relations.
// Each object is expanded once, at its shortest distance from a seed, so
// subtrees reached by several paths and cycles are walked once. A chain longer than walkDepthLimit levels raises
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
		Decls:   append(idWalkDecls(), Decl{Name: walkStatic, Type: "TEXT[]"}),
	}
	if deferredSeeds(blocks) {
		fn.Decls = append(fn.Decls, Decl{Name: walkDeferred, Type: "TEXT[]"})
	}
	// Only the seed depends on the subject kind; the walk and final query are shared.
	seed := seedBySubjectKind(renderWalkSeed(plainBlocks), renderWalkSeed(blocks), hasUserset)
	fn.Body = renderListObjectsWalk(plan, blocks, seed)
	return fn.SQL(), nil
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
	columns := make([]string, 0, 2)
	columns = append(columns, seedIDs("seed.propagatable"))
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
			Graph:       plan.graph(),
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
		plan.graph(),
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
			Graph:       plan.graph(),
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
