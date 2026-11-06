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
		decls = append(decls, parentClosureWalkDecls()...)
		regular = append(regular, renderParentClosureWalk(plan, blocks.ParentClosureWalks)...)
	}
	regular = append(regular, ReturnQuery{Query: regularPaginatedQuery})

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
				Then: renderUsersetFilterThenBranch(usersetFilterPaginatedQuery),
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

	return MultiCTE(false, ctes, wildcardTailQuery).SQL(), needsParentClosure
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
func renderParentClosureWalk(plan ListPlan, recursing []string) []Stmt {
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
		aggregateRowsInto(closureArrays, "seed", IndentLines(seed.SQL(), "    ")),
		Assign{Name: closureDepth, Value: Raw(fillDepth("0", closureType))},
	}
	if len(recursing) == 0 {
		return stmts
	}

	step := SelectStmt{
		ColumnExprs: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Col{Table: "f", Column: "via"},
		},
		FromExpr: TableAs("", "unnest("+frontierArrays+")", "f(subject_type, subject_id, via)"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "link",
			On: And(
				Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Col{Table: "f", Column: "subject_type"}},
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: Col{Table: "f", Column: "subject_id"}},
				Eq{Left: Col{Table: "link", Column: "relation"}, Right: Col{Table: "f", Column: "via"}},
			),
		}},
		Where: And(
			Eq{Left: Col{Table: "f", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
			In{Expr: Col{Table: "f", Column: "via"}, Values: recursing},
		),
	}
	fresh := IndentLines(step.SQL(), "    ") + "\n    EXCEPT\n    SELECT * FROM unnest(" + closureArrays + ")"
	return append(stmts,
		Assign{Name: frontierType, Value: Raw(closureType)},
		Assign{Name: frontierID, Value: Raw(closureID)},
		Assign{Name: frontierVia, Value: Raw(closureVia)},
		Assign{Name: walkLevel, Value: Int(1)},
		While{
			Cond: Gt{Left: Raw("cardinality(" + frontierType + ")"), Right: Int(0)},
			Body: []Stmt{
				If{
					Cond: Gte{Left: Raw(walkLevel), Right: Int(walkDepthLimit)},
					Then: []Stmt{Raise{Message: "resolution too complex", ErrCode: "M2002"}},
				},
				aggregateRowsInto(frontierArrays, "fresh", fresh),
				Assign{Name: closureType, Value: Raw(closureType + " || " + frontierType)},
				Assign{Name: closureID, Value: Raw(closureID + " || " + frontierID)},
				Assign{Name: closureVia, Value: Raw(closureVia + " || " + frontierVia)},
				Assign{Name: closureDepth, Value: Raw(closureDepth + " || " + fillDepth(walkLevel, frontierType))},
				Assign{Name: walkLevel, Value: Raw(walkLevel + " + 1")},
			},
		},
	)
}

// aggregateRowsInto selects the (subject_type, subject_id, via) rows of query
// into the parallel arrays named by into. The arrays are aggregated in one
// pass over the same rows, so they stay aligned.
func aggregateRowsInto(into, alias, query string) Stmt {
	return RawStmt{SQLText: fmt.Sprintf(`SELECT array_agg(%[1]s.subject_type), array_agg(%[1]s.subject_id), array_agg(%[1]s.via) INTO %[2]s
FROM (
%[3]s
) AS %[1]s(subject_type, subject_id, via);`, alias, into, query)}
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
			Where: wildcardSubjectsTailWhere(plan.DatabaseSchema, plan.Relation, plan.ObjectType, plan.HasExclusion),
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
