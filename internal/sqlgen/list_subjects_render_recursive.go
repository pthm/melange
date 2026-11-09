package sqlgen

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// PL/pgSQL variables of the parent_closure walk. The closure and the frontier
// are (subject_type, subject_id, via) rows held as parallel arrays.
const (
	closureType  = "v_pc_type"
	closureID    = "v_pc_id"
	closureVia   = "v_pc_via"
	closureDepth = "v_pc_depth" // hops from the listed object, minus one
	frontierType = "v_fr_type"
	frontierID   = "v_fr_id"
	frontierVia  = "v_fr_via"

	closureArrays  = closureType + ", " + closureID + ", " + closureVia
	frontierArrays = frontierType + ", " + frontierID + ", " + frontierVia
)

// closureColumns name the columns of the parent_closure walk's rows.
var closureColumns = []string{"subject_type", "subject_id", "via"}

// =============================================================================
// Recursive List Subjects Render Functions
// =============================================================================
// RenderListSubjectsRecursiveFunction renders a recursive list_subjects function from plan and blocks.
// This handles TTU patterns with subject_pool CTE and check_permission_internal calls.
func RenderListSubjectsRecursiveFunction(plan ListPlan, blocks SubjectsRecursiveBlockSet) (string, error) {
	usersetFilterPaginatedQuery := buildUsersetFilterQuery(plan, blocks)
	regularPaginatedQuery, walksParents := buildRegularPaginatedQuery(plan, blocks)

	decls := []Decl{
		{Name: "v_filter_type", Type: "TEXT"},
		{Name: "v_filter_relation", Type: "TEXT"},
	}
	regular := []Stmt{Comment{Text: "Regular subject type: find direct subjects and expand usersets"}}
	if walksParents {
		// A subject type the relation cannot grant lists nothing: skip the walk
		// (and its depth limit), as OpenFGA does.
		regular = append(regular, If{
			Cond: In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			Then: renderParentClosureWalk(plan, blocks.ParentClosureWalks, false),
		})
	}
	regular = append(regular, ReturnQuery{Query: regularPaginatedQuery})

	// The userset-filter blocks start from the sources the walk collects.
	var sourcesWalk []Stmt
	selfWalks := blocks.UsersetFilterSelfWalks
	if len(selfWalks) > 0 {
		sourcesWalk = []Stmt{
			If{Cond: usersetFilterReachable(plan), Then: renderParentClosureWalk(plan, selfWalks, true)},
			renderUsersetFilterSources(plan, selfWalks),
		}
		decls = append(decls, Decl{Name: usersetFilterSources, Type: "TEXT[]"})
	}
	walksClosure := walksParents || len(selfWalks) > 0
	if blocks.UsersetFilterChecked {
		sourcesWalk = append(sourcesWalk, renderGrantReachWalk(plan)...)
		decls = append(decls, grantReachWalkDecls()...)
		// parentClosureWalkDecls declares v_level when the closure is walked.
		if !walksClosure {
			decls = append(decls, Decl{Name: walkLevel, Type: "INT"})
		}
	}
	usersetFilter := renderUsersetFilterThenBranch(usersetFilterPaginatedQuery, sourcesWalk...)
	if walksClosure {
		decls = append(decls, parentClosureWalkDecls()...)
	}

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header:  ListSubjectsFunctionHeader(plan.ObjectType, plan.Relation, plan.FeaturesString()),
		Decls:   decls,
		Body: []Stmt{
			Comment{Text: "Check if p_subject_type is a userset filter (contains '#')"},
			If{
				Cond: Gt{
					Left:  Position{Needle: Lit("#"), Haystack: SubjectType},
					Right: Int(0),
				},
				Then: usersetFilter,
				Else: regular,
			},
		},
	}

	return fn.SQL(), nil
}

func buildUsersetFilterQuery(plan ListPlan, blocks SubjectsRecursiveBlockSet) string {
	usersetFilterParts := renderTypedQueryBlocks(blocks.UsersetFilterBlocks)
	if blocks.UsersetFilterSelfBlock != nil {
		usersetFilterParts = append(usersetFilterParts, renderTypedQueryBlock(*blocks.UsersetFilterSelfBlock))
	}
	wrapped := plan.wrapPaginationWildcardFirst(RenderUnionBlocks(usersetFilterParts))
	return hoistClosureCTE(wrapped, plan.Inline.ClosureRows)
}

// buildRegularPaginatedQuery returns the regular-subject query and whether it
// reads parent_closure, which the function must then compute first.
func buildRegularPaginatedQuery(plan ListPlan, blocks SubjectsRecursiveBlockSet) (string, bool) {
	regularBlocks := renderTypedQueryBlocks(blocks.RegularBlocks)
	ttuBlocks := renderTypedQueryBlocks(blocks.RegularTTUBlocks)
	regularQuery, walksParents := buildSubjectsRecursiveRegularQuery(plan, regularBlocks, ttuBlocks)
	return plan.wrapPaginationWildcardFirst(regularQuery), walksParents
}

// buildSubjectsRecursiveRegularQuery builds the regular path query with
// parent_closure and base_results CTEs, and reports whether it reads
// parent_closure.
func buildSubjectsRecursiveRegularQuery(plan ListPlan, regularBlocks, ttuBlocks []QueryBlock) (string, bool) {
	// Join all base blocks with UNION
	baseBlocksSQL := RenderUnionBlocks(regularBlocks)

	// Build CTEs list
	ctes := []CTEDef{}

	// Check if any TTU blocks need subject_pool (complex parent relations)
	needsSubjectPool := false
	needsParentClosure := false
	if len(ttuBlocks) > 0 {
		ttuBlocksSQL := RenderUnionBlocks(ttuBlocks)
		// Check if any TTU block uses subject_pool or parent_closure
		needsSubjectPool = containsSubjectPool(ttuBlocksSQL)
		needsParentClosure = containsParentClosure(ttuBlocksSQL)

		baseBlocksSQL = joinUnionBlocksSQL([]string{baseBlocksSQL, ttuBlocksSQL})
	}

	// Add subject_pool CTE if needed (for complex parent relations)
	if needsSubjectPool {
		subjectPoolSQL := buildSubjectPoolCTESQL(plan)
		ctes = append(ctes, CTEDef{Name: "subject_pool", Query: Raw(subjectPoolSQL)})
	}

	// parent_closure reads the ancestors the walk collected (renderParentClosureWalk).
	if needsParentClosure {
		ctes = append(ctes, CTEDef{Name: "parent_closure", Query: SelectStmt{
			ColumnExprs: []Expr{Raw("*")},
			FromExpr: TableAs("", fmt.Sprintf("unnest(%s, %s, %s, %s)", closureType, closureID, closureDepth, closureVia),
				"p(subject_type, subject_id, depth, via)"),
		}})
	}

	// Add base_results CTE.
	// base_results is referenced twice in this query: from the has_wildcard CTE
	// (EXISTS subquery on '*') and from the outer wildcard-tail SELECT (FROM
	// base_results br). Force materialization so the expensive UNION inside is
	// computed once instead of inlined into both reference sites.
	ctes = append(ctes, CTEDef{Name: "base_results", Query: Raw(baseBlocksSQL), Materialized: plan.MaterializeCTEs()})

	// Build the has_wildcard CTE query. Only the wildcard tail reads it, and it
	// only emits the CROSS JOIN has_wildcard when plan.AllowWildcard — gate the
	// CTE on the same flag so def and ref stay lockstep (an unreferenced ordinary
	// CTE is dead codegen).
	if plan.AllowWildcard {
		hasWildcardQuery := SelectStmt{
			ColumnExprs: []Expr{
				Alias{
					Expr: Raw("EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*')"),
					Name: "has_wildcard",
				},
			},
		}
		ctes = append(ctes, CTEDef{Name: "has_wildcard", Query: hasWildcardQuery})
	}

	// Build the final query with wildcard handling
	wildcardTailQuery := buildSubjectsWildcardTailQuery(plan)

	return MultiCTE(ctes, wildcardTailQuery).SQL(), needsParentClosure
}

func containsSubjectPool(sql string) bool {
	return strings.Contains(sql, "subject_pool")
}

func containsParentClosure(sql string) bool {
	return strings.Contains(sql, "parent_closure")
}

func parentClosureWalkDecls() []Decl {
	return []Decl{
		{Name: closureType, Type: "TEXT[]"},
		{Name: closureID, Type: "TEXT[]"},
		{Name: closureVia, Type: "TEXT[]"},
		{Name: frontierType, Type: "TEXT[]"},
		{Name: frontierID, Type: "TEXT[]"},
		{Name: frontierVia, Type: "TEXT[]"},
		{Name: closureDepth, Type: "INT[]"},
		{Name: walkLevel, Type: "INT"},
	}
}

// PL/pgSQL variables of renderGrantReachWalk: the reached (object, relation)
// states and the frontier still to expand.
const (
	reachType         = "v_rw_type"
	reachID           = "v_rw_id"
	reachRelation     = "v_rw_rel"
	reachNextType     = "v_rn_type"
	reachNextID       = "v_rn_id"
	reachNextRelation = "v_rn_rel"

	reachArrays     = reachType + ", " + reachID + ", " + reachRelation
	reachNextArrays = reachNextType + ", " + reachNextID + ", " + reachNextRelation
)

// grantReachWalkDecls declares the arrays of renderGrantReachWalk; walkLevel
// is shared with the parent_closure walk.
func grantReachWalkDecls() []Decl {
	return []Decl{
		{Name: reachType, Type: "TEXT[]"},
		{Name: reachID, Type: "TEXT[]"},
		{Name: reachRelation, Type: "TEXT[]"},
		{Name: reachNextType, Type: "TEXT[]"},
		{Name: reachNextID, Type: "TEXT[]"},
		{Name: reachNextRelation, Type: "TEXT[]"},
	}
}

// planGrantEdges returns the grant edges of the plan relation on its type.
func planGrantEdges(plan ListPlan) []grantEdge {
	return plan.graph().grantEdgesFrom(plan.ObjectType, plan.Relation)
}

// grantEdgesTable is the VALUES table of the grant edges; target_type is NULL
// on the edges that follow userset subjects.
func grantEdgesTable(edges []grantEdge, alias string) TypedValuesTable {
	rows := make([]ValuesRow, len(edges))
	for i, e := range edges {
		target := []Expr{Raw("NULL"), Raw("NULL")}
		if e.TargetType != "" {
			target = []Expr{Lit(e.TargetType), Lit(e.TargetRelation)}
		}
		rows[i] = append(ValuesRow{Lit(e.Type), Lit(e.Relation), Lit(e.TupleRelation)}, target...)
	}
	return TypedValuesTable{Rows: rows, Alias: alias, Columns: []string{"type", "relation", "tuple_relation", "target_type", "target_relation"}}
}

// grantEdgeTuples joins the states unnested as states(type, id, relation) to
// their grant edges e among edges and the tuples t those edges read.
func grantEdgeTuples(edges []grantEdge, states, alias string) (from TableExpr, joins []JoinClause) {
	state := func(column string) Expr { return Col{Table: alias, Column: column} }
	return TableAs("", "unnest("+states+")", alias+"(type, id, relation)"), []JoinClause{
		{Type: "INNER", TableExpr: grantEdgesTable(edges, "e"), On: And(
			Eq{Left: Col{Table: "e", Column: "type"}, Right: state("type")},
			Eq{Left: Col{Table: "e", Column: "relation"}, Right: state("relation")},
		)},
		{Type: "INNER", Table: "melange_tuples", Alias: "t", On: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: state("type")},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: state("id")},
			Eq{Left: Col{Table: "t", Column: "relation"}, Right: Col{Table: "e", Column: "tuple_relation"}},
		)},
	}
}

// grantEdgeFollows holds when grant edge e follows tuple t: a userset subject
// on an edge without a target type, else a plain subject of the target type.
func grantEdgeFollows() Expr {
	tuple := func(column string) Expr { return Col{Table: "t", Column: column} }
	targetType := Col{Table: "e", Column: "target_type"}
	return Or(
		And(IsNull{Expr: targetType}, HasUserset{Source: tuple("subject_id")}),
		And(Eq{Left: tuple("subject_type"), Right: targetType}, NoUserset{Source: tuple("subject_id")}),
	)
}

// renderGrantReachWalk collects into the v_rw_* arrays the (object, relation)
// states permission resolution reaches from the listed relation on the listed
// object, breadth first along its grant edges: a parent lookup's linking
// tuple leads to the parent holding the looked-up relation, a userset grant
// to the userset's object holding its relation, and a plain subject ends the
// path. Every tuple that can grant the relation lies on a reached state.
// States still to expand walkDepthLimit hops away raise M2002, as resolution
// (and OpenFGA's ListUsers) following those same tuples would.
func renderGrantReachWalk(plan ListPlan) []Stmt {
	tuple := func(column string) Expr { return Col{Table: "t", Column: column} }
	edges := planGrantEdges(plan)
	from, joins := grantEdgeTuples(edges, reachNextArrays, "f")
	step := SelectStmt{
		ColumnExprs: []Expr{
			tuple("subject_type"),
			UsersetObjectID{Source: tuple("subject_id")},
			Raw("COALESCE(e.target_relation, " + UsersetRelation{Source: tuple("subject_id")}.SQL() + ")"),
		},
		FromExpr: from,
		Joins:    joins,
		Where: And(
			Ne{Left: tuple("subject_id"), Right: Lit("*")},
			grantEdgeFollows(),
		),
	}
	fresh := IndentLines(step.SQL(), "    ") + "\n    EXCEPT\n    SELECT * FROM unnest(" + reachArrays + ")"
	walk := []Stmt{
		Assign{Name: reachNextType, Value: Raw("ARRAY[" + Lit(plan.ObjectType).SQL() + "]")},
		Assign{Name: reachNextID, Value: Raw("ARRAY[p_object_id]")},
		Assign{Name: reachNextRelation, Value: Raw("ARRAY[" + Lit(plan.Relation).SQL() + "]")},
		Assign{Name: reachType, Value: Raw(reachNextType)},
		Assign{Name: reachID, Value: Raw(reachNextID)},
		Assign{Name: reachRelation, Value: Raw(reachNextRelation)},
		Assign{Name: walkLevel, Value: Int(0)},
		While{
			Cond: Gt{Left: Raw("cardinality(" + reachNextType + ")"), Right: Int(0)},
			Body: []Stmt{
				If{Cond: Gte{Left: Raw(walkLevel), Right: Int(walkDepthLimit)}, Then: raiseTooDeep()},
				aggregateRowsInto(reachNextArrays, "n", fresh, "type", "id", "relation"),
				Assign{Name: reachType, Value: Raw(reachType + " || " + reachNextType)},
				Assign{Name: reachID, Value: Raw(reachID + " || " + reachNextID)},
				Assign{Name: reachRelation, Value: Raw(reachRelation + " || " + reachNextRelation)},
				Assign{Name: walkLevel, Value: Raw(walkLevel + " + 1")},
			},
		},
	}
	return []Stmt{
		Comment{Text: "States whose tuples can grant the relation, for the checked candidates"},
		If{Cond: usersetFilterReachable(plan), Then: walk},
	}
}

// usersetFilterReachable holds when the userset filter (type and relation) is a
// state resolution reaches (relationGraph.grantEdgesFrom) or a relation that
// grants one on the same object (editor for viewer: editor): any other filter
// has no candidates and no path for resolution to exceed the depth limit on.
func usersetFilterReachable(plan ListPlan) Expr {
	var filters []string
	add := func(objectType, relation string) {
		filter := "(" + Lit(objectType).SQL() + ", " + Lit(relation).SQL() + ")"
		if !slices.Contains(filters, filter) {
			filters = append(filters, filter)
		}
	}
	for _, e := range planGrantEdges(plan) {
		add(e.Type, e.Relation)
		if e.TargetType == "" {
			add(e.Type, e.TupleRelation)
		}
	}
	if len(filters) == 0 {
		return Bool(false)
	}
	return Raw("(v_filter_type, v_filter_relation) IN (" + strings.Join(filters, ", ") + ")")
}

// renderUsersetFilterSources sets v_uf_sources to the listed object and its
// ancestors of the same type along the self-recursive linking relations, read
// from the parent_closure walk.
func renderUsersetFilterSources(plan ListPlan, selfWalks []string) Stmt {
	ancestors := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "p", Column: "id"}},
		FromExpr:    TableAs("", "unnest("+closureArrays+")", "p(type, id, via)"),
		Where: And(
			Eq{Left: Col{Table: "p", Column: "type"}, Right: Lit(plan.ObjectType)},
			In{Expr: Col{Table: "p", Column: "via"}, Values: selfWalks},
			Ne{Left: Col{Table: "p", Column: "id"}, Right: ObjectID},
		),
	}
	return Assign{Name: usersetFilterSources, Value: Raw("ARRAY[p_object_id] || ARRAY(" + ancestors.SQL() + ")")}
}

// renderParentClosureWalk collects the ancestors of the listed object
// reachable via TTU linking relations into the parent_closure arrays, so the
// caller can find subjects that hold the checked relation on any ancestor
// without re-traversing the parent graph for each candidate subject.
//
// The walk is breadth first and expands each (ancestor, linking relation)
// once, so an ancestor's depth is its shortest distance. Each chain stays on
// the linking relation of its first hop (via): "v from parent" recurses
// through v's own "v from parent", never through another linking relation such
// as owner, so a parent -> owner chain grants nothing. A chain continues past
// the direct parent only along the recursing linking relations (see
// SubjectsRecursiveBlockSet.ParentClosureWalks), and only out of nodes of the
// object's own type: cross-type linking relations (folder.org -> org) are
// one-hop terminals whose targets feed the grant lookup but must not recurse
// (org:o1 -> parent -> org:o0 would pull in grants from unrelated ancestors).
//
// Ancestors still to expand walkDepthLimit hops away raise M2002, as OpenFGA's
// ListUsers fails on a parent chain of 25 hops.
func renderParentClosureWalk(plan ListPlan, recursing []string, crossLinks bool) []Stmt {
	linkingRelationSet := make(map[string]bool)
	allowedTypeSet := make(map[string]bool)
	for _, parent := range buildListParentRelations(plan.Analysis) {
		linkingRelationSet[parent.LinkingRelation] = true
		for _, t := range parent.AllowedLinkingTypesSlice {
			allowedTypeSet[t] = true
		}
	}
	linkingRelations := slices.Sorted(maps.Keys(linkingRelationSet))
	allowedTypes := slices.Sorted(maps.Keys(allowedTypeSet))

	// Seed: the direct parents (the link's subject becomes the parent object).
	seedWhere := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "link", Column: "relation"}, Values: linkingRelations},
	}
	if len(allowedTypes) > 0 {
		seedWhere = append(seedWhere, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: allowedTypes})
	}
	seed := SelectStmt{
		Distinct: true,
		ColumnExprs: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Col{Table: "link", Column: "relation"},
		},
		FromExpr: TableAs("", "melange_tuples", "link"),
		Where:    And(seedWhere...),
	}

	stmts := []Stmt{
		Comment{Text: "Collect the object's ancestors breadth first into parent_closure"},
		aggregateRowsInto(closureArrays, "seed", IndentLines(seed.SQL(), "    "), closureColumns...),
		Assign{Name: closureDepth, Value: Raw(fillDepth("0", closureType))},
	}
	if len(recursing) == 0 {
		return stmts
	}

	// A chain stays on its first linking relation, unless crossLinks: a
	// relation that recurses on itself through several linking relations
	// ("v from parent or v from owner") passes on through any of them.
	nextVia := Expr(Col{Table: "f", Column: "via"})
	followVia := Expr(Eq{Left: Col{Table: "link", Column: "relation"}, Right: Col{Table: "f", Column: "via"}})
	if crossLinks {
		nextVia = Col{Table: "link", Column: "relation"}
		followVia = In{Expr: Col{Table: "link", Column: "relation"}, Values: recursing}
	}
	step := SelectStmt{
		ColumnExprs: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			nextVia,
		},
		FromExpr: TableAs("", "unnest("+frontierArrays+")", "f(subject_type, subject_id, via)"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "link",
			On: And(
				Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Col{Table: "f", Column: "subject_type"}},
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: Col{Table: "f", Column: "subject_id"}},
				followVia,
			),
		}},
		Where: And(
			Eq{Left: Col{Table: "f", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
			In{Expr: Col{Table: "f", Column: "via"}, Values: recursing},
		),
	}
	fresh := IndentLines(step.SQL(), "    ") + "\n    EXCEPT\n    SELECT * FROM unnest(" + closureArrays + ")"
	stmts = append(stmts,
		Assign{Name: frontierType, Value: Raw(closureType)},
		Assign{Name: frontierID, Value: Raw(closureID)},
		Assign{Name: frontierVia, Value: Raw(closureVia)},
		Assign{Name: walkLevel, Value: Int(1)},
		While{
			Cond: Gt{Left: Raw("cardinality(" + frontierType + ")"), Right: Int(0)},
			Body: []Stmt{
				If{
					Cond: Gte{Left: Raw(walkLevel), Right: Int(walkDepthLimit)},
					Then: raiseTooDeep(),
				},
				aggregateRowsInto(frontierArrays, "fresh", fresh, closureColumns...),
				Assign{Name: closureType, Value: Raw(closureType + " || " + frontierType)},
				Assign{Name: closureID, Value: Raw(closureID + " || " + frontierID)},
				Assign{Name: closureVia, Value: Raw(closureVia + " || " + frontierVia)},
				Assign{Name: closureDepth, Value: Raw(closureDepth + " || " + fillDepth(walkLevel, frontierType))},
				Assign{Name: walkLevel, Value: Raw(walkLevel + " + 1")},
			},
		},
	)
	return append(stmts, renderLastHopExpansionCheck(plan, recursing, crossLinks)...)
}

// renderLastHopExpansionCheck raises M2002 when an ancestor walkDepthLimit-1
// hops away holds a tuple resolution would still follow there (a userset to
// expand, or a link to a parent of another type): OpenFGA's ListUsers counts
// that step and fails. Links the walk itself follows are covered by its own
// limit. Each ancestor holds the relation its chain looks up: the listed
// relation when crossLinks, else the relation looked up through its via.
func renderLastHopExpansionCheck(plan ListPlan, recursing []string, crossLinks bool) []Stmt {
	lookups := map[string][]string{}
	for _, parent := range buildListParentRelations(plan.Analysis) {
		via := parent.LinkingRelation
		relation := parent.Relation
		if crossLinks {
			relation = plan.Relation
		}
		if slices.Contains(recursing, via) && !slices.Contains(lookups[via], relation) {
			lookups[via] = append(lookups[via], relation)
		}
	}
	var rows []ValuesRow
	for _, via := range slices.Sorted(maps.Keys(lookups)) {
		for _, relation := range lookups[via] {
			for _, e := range plan.graph().grantEdgesFrom(plan.ObjectType, relation) {
				walked := e.TargetType == plan.ObjectType && slices.Contains(recursing, e.TupleRelation)
				if e.Type != plan.ObjectType || e.Relation != relation || walked {
					continue
				}
				target := Expr(Raw("NULL"))
				if e.TargetType != "" {
					target = Lit(e.TargetType)
				}
				row := ValuesRow{Lit(via), Lit(e.TupleRelation), target}
				if !slices.ContainsFunc(rows, func(r ValuesRow) bool { return r.SQL() == row.SQL() }) {
					rows = append(rows, row)
				}
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	tuple := func(column string) Expr { return Col{Table: "t", Column: column} }
	edge := func(column string) Expr { return Col{Table: "e", Column: column} }
	ancestor := func(column string) Expr { return Col{Table: "p", Column: column} }
	expandsAtLastHop := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    TableAs("", "unnest("+closureArrays+", "+closureDepth+")", "p(type, id, via, depth)"),
		Joins: []JoinClause{
			{
				Type: "INNER", TableExpr: TypedValuesTable{Rows: rows, Alias: "e", Columns: []string{"via", "tuple_relation", "target_type"}},
				On: Eq{Left: edge("via"), Right: ancestor("via")},
			},
			{Type: "INNER", Table: "melange_tuples", Alias: "t", On: And(
				Eq{Left: tuple("object_type"), Right: ancestor("type")},
				Eq{Left: tuple("object_id"), Right: ancestor("id")},
				Eq{Left: tuple("relation"), Right: edge("tuple_relation")},
			)},
		},
		Where: And(
			// closureDepth counts hops minus one.
			Eq{Left: ancestor("depth"), Right: Int(walkDepthLimit - 2)},
			Eq{Left: ancestor("type"), Right: Lit(plan.ObjectType)},
			Ne{Left: tuple("subject_id"), Right: Lit("*")},
			grantEdgeFollows(),
		),
	}
	return []Stmt{If{Cond: Exists{Query: expandsAtLastHop}, Then: raiseTooDeep()}}
}

// aggregateRowsInto selects the rows of query, named alias(columns), into the
// parallel arrays named by into, one per column. The arrays are aggregated in
// one pass over the same rows, so they stay aligned.
func aggregateRowsInto(into, alias, query string, columns ...string) Stmt {
	aggregates := make([]string, len(columns))
	for i, column := range columns {
		aggregates[i] = "array_agg(" + alias + "." + column + ")"
	}
	return RawStmt{SQLText: fmt.Sprintf("SELECT %s INTO %s\nFROM (\n%s\n) AS %s(%s);",
		strings.Join(aggregates, ", "), into, query, alias, strings.Join(columns, ", "))}
}

// fillDepth is an array holding depth once per element of typeArray.
func fillDepth(depth, typeArray string) string {
	return fmt.Sprintf("array_fill(%s, ARRAY[COALESCE(cardinality(%s), 0)])", depth, typeArray)
}

// buildSubjectPoolCTESQL builds the subject_pool CTE SQL for complex parent relations.
func buildSubjectPoolCTESQL(plan ListPlan) string {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples(plan.DatabaseSchema, "t").
		Select("t.subject_id").
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}).
		Distinct()

	if excludeWildcard {
		q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	return q.SQL()
}

// buildSubjectsWildcardTailQuery builds the final SELECT with wildcard expansion.
// The returned SQL has no trailing semicolon because it is composed into a larger
// pagination CTE by the caller.
func buildSubjectsWildcardTailQuery(plan ListPlan) SQLer {
	if plan.AllowWildcard {
		var from TableExpr = TableAs("", "base_results", "br")
		if plan.HasExclusion {
			from = wildcardExclusionCandidates(plan)
		}
		return SelectStmt{
			ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
			FromExpr:    from,
			Joins: []JoinClause{
				{Type: "CROSS", Table: "has_wildcard", Alias: "hw"},
			},
			Where: wildcardSubjectsTailWhere(plan.DatabaseSchema, plan.graph(), plan.Relation, plan.ObjectType, plan.HasExclusion),
		}
	}
	return SelectStmt{
		ColumnExprs: []Expr{Col{Table: "br", Column: "subject_id"}},
		FromExpr:    TableAs("", "base_results", "br"),
	}
}

// wildcardExclusionCandidates extends base_results for a relation whose
// wildcard grant an exclusion can narrow. Then the '*' row is dropped (not
// everyone has access), but every subject the exclusion leaves out still
// has access, and the base set names none of those reached only through the
// wildcard. Offer the concrete subjects named in any tuple on this object;
// the tail's checks keep exactly those with access (see
// wildcardSubjectsTailWhere).
func wildcardExclusionCandidates(plan ListPlan) TableExpr {
	return UnionSubquery{
		Alias: "br",
		Queries: []SelectStmt{
			{
				ColumnExprs: []Expr{Col{Table: "b", Column: "subject_id"}},
				FromExpr:    TableAs("", "base_results", "b"),
			},
			{
				ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
				FromExpr:    TableAs("", "melange_tuples", "t"),
				Where: And(
					Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
					Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
					Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
					NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				),
			},
		},
	}
}
