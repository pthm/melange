package sqlgen

import "fmt"

// applyWildcardExclusion adds a wildcard exclusion predicate if plan excludes wildcards.
func applyWildcardExclusion(q *TupleQuery, plan ListPlan, tableAlias string) {
	if plan.ExcludeWildcard() {
		q.Where(Ne{Left: Col{Table: tableAlias, Column: "subject_id"}, Right: Lit("*")})
	}
}

// applyExclusionPredicates adds exclusion predicates to the query.
// When useCTE is true, skips predicates (applied via CTE anti-join instead).
func applyExclusionPredicates(q *TupleQuery, config ExclusionConfig, useCTE bool) {
	if useCTE {
		return
	}
	for _, pred := range config.BuildPredicates() {
		q.Where(pred)
	}
}

// BuildListSubjectsBlocks builds all query blocks for a list_subjects function.
// Returns a BlockSet with Primary and optionally Secondary blocks.
func BuildListSubjectsBlocks(plan ListPlan) (BlockSet, error) {
	usersetFilterBlocks, usersetFilterSelf, err := buildListSubjectsUsersetFilterBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}

	regularBlocks, err := buildListSubjectsRegularBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}

	return BlockSet{
		Primary:       regularBlocks,
		Secondary:     usersetFilterBlocks,
		SecondarySelf: usersetFilterSelf,
	}, nil
}

// buildListSubjectsUsersetFilterBlocks builds the userset filter path blocks.
// The userset filter path is ALWAYS built because any subject can be a userset
// reference (e.g., document:1#writer), even when there are no [group#member]
// patterns in the model.
func buildListSubjectsUsersetFilterBlocks(plan ListPlan) (blocks []TypedQueryBlock, selfBlock *TypedQueryBlock, err error) {
	directBlock := buildListSubjectsUsersetFilterDirectBlock(plan)

	var intersectionBlocks []TypedQueryBlock
	intersectionBlocks, err = buildListSubjectsIntersectionClosureBlocks(
		plan,
		"v_filter_type || '#' || v_filter_relation",
		"v_filter_type",
		plan.HasExclusion,
	)
	if err != nil {
		return nil, nil, err
	}

	selfBlock = buildListSubjectsUsersetFilterSelfBlock(plan)

	blocks = make([]TypedQueryBlock, 0, 1+len(intersectionBlocks))
	blocks = append(blocks, directBlock)
	blocks = append(blocks, intersectionBlocks...)

	return blocks, selfBlock, nil
}

// buildListSubjectsUsersetFilterDirectBlock builds the userset filter block for list_subjects.
// Handles queries like "list_subjects for folder:1.viewer with filter group#member".
// Finds tuples where the subject is a userset (e.g., group:fga#member_c4) and checks
// if the userset relation satisfies the filter relation via closure.
func buildListSubjectsUsersetFilterDirectBlock(plan ListPlan) TypedQueryBlock {
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, Param("v_filter_relation")),
		Name: "subject_id",
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Or(
				Eq{Left: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Param("v_filter_relation")},
				ExistsExpr(closureExistsStmt),
			),
			CheckPermissionCall{
				FunctionName: "check_permission",
				Subject:      SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
				Relation:     plan.Relation,
				Object:       LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow:  true,
			},
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Userset filter: find userset tuples that match and return normalized references",
		},
		Query: stmt,
	}
}

// buildListSubjectsUsersetFilterSelfBlock builds the self-referential userset filter block.
// Returns the object itself as a userset reference when filter type matches object type.
func buildListSubjectsUsersetFilterSelfBlock(plan ListPlan) *TypedQueryBlock {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{ObjectID, Lit("#"), Param("v_filter_relation")}},
		Name: "subject_id",
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectExpr},
		Where: And(
			Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: when filter type matches object type",
			"-- e.g., querying document:1.viewer with filter document#writer",
			"-- should return document:1#writer if writer satisfies the relation",
		},
		Query: stmt,
	}
}

// buildListSubjectsRegularBlocks builds the regular (non-userset-filter) path blocks.
func buildListSubjectsRegularBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	blocks := []TypedQueryBlock{buildListSubjectsDirectBlock(plan)}

	blocks = append(blocks, buildTypedListSubjectsComplexClosureBlocks(plan)...)

	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(
		plan, "p_subject_type", "p_subject_type", plan.HasExclusion,
	)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	if plan.HasUsersetPatterns {
		usersetBlocks, err := buildListSubjectsUsersetPatternBlocks(plan)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, usersetBlocks...)
	}

	return blocks, nil
}

// buildListSubjectsDirectBlock builds the direct tuple lookup block for list_subjects.
func buildListSubjectsDirectBlock(plan ListPlan) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("p_subject_type")},
			NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
		).
		SelectCol("subject_id").
		Distinct()

	applyWildcardExclusion(q, plan, "t")
	applyExclusionPredicates(q, plan.Exclusions, plan.UseCTEExclusion)

	return TypedQueryBlock{
		Comments: []string{"-- Path 1: Direct tuple lookup with simple closure relations"},
		Query:    q.Build(),
	}
}

// buildTypedListSubjectsComplexClosureBlocks builds blocks for complex closure relations.
func buildTypedListSubjectsComplexClosureBlocks(plan ListPlan) []TypedQueryBlock {
	if len(plan.ComplexClosure) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(plan.ComplexClosure))
	for _, rel := range plan.ComplexClosure {
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("p_subject_type")},
				NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				CheckPermission{
					Subject: SubjectRef{
						Type: Param("p_subject_type"),
						ID:   Col{Table: "t", Column: "subject_id"},
					},
					Relation:    rel,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					ExpectAllow: true,
				},
			).
			SelectCol("subject_id").
			Distinct()

		applyWildcardExclusion(q, plan, "t")
		applyExclusionPredicates(q, plan.Exclusions, plan.UseCTEExclusion)

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{"-- Complex closure: validate via check_permission_internal"},
			Query:    q.Build(),
		})
	}

	return blocks
}

// buildListSubjectsIntersectionClosureBlocks builds blocks for intersection closure relations.
// Calls list_subjects for each intersection relation to get candidates.
// When validate is true and checkSubjectTypeExpr is non-empty, wraps with check_permission.
func buildListSubjectsIntersectionClosureBlocks(plan ListPlan, subjectTypeExpr, checkSubjectTypeExpr string, validate bool) ([]TypedQueryBlock, error) {
	if len(plan.Analysis.IntersectionClosureRelations) == 0 {
		return nil, nil
	}

	blocks := make([]TypedQueryBlock, 0, len(plan.Analysis.IntersectionClosureRelations))
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcCall := FunctionCallExpr{
			Name:  listSubjectsFunctionName(plan.ObjectType, rel),
			Args:  []Expr{ObjectID, Raw(subjectTypeExpr)},
			Alias: "ics",
		}

		stmt := SelectStmt{
			ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
			FromExpr:    funcCall,
		}

		if validate && checkSubjectTypeExpr != "" {
			stmt.Distinct = true
			stmt.Where = CheckPermissionCall{
				FunctionName: "check_permission",
				Subject:      SubjectRef{Type: Raw(checkSubjectTypeExpr), ID: Col{Table: "ics", Column: "subject_id"}},
				Relation:     plan.Relation,
				Object:       LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow:  true,
			}
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Compose with intersection closure: %s", rel)},
			Query:    stmt,
		})
	}

	return blocks, nil
}

// buildListSubjectsUsersetPatternBlocks builds blocks for userset patterns in list_subjects.
func buildListSubjectsUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		var block TypedQueryBlock
		if pattern.IsComplex {
			block = buildListSubjectsComplexUsersetBlock(plan, pattern)
		} else {
			block = buildListSubjectsSimpleUsersetBlock(plan, pattern)
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildListSubjectsComplexUsersetBlock builds a block for complex userset patterns.
// Uses LATERAL join with userset's list_subjects function for userset-to-userset chains.
func buildListSubjectsComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	funcName := listSubjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "ls", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{{
			Type: "CROSS JOIN LATERAL",
			TableExpr: FunctionCallExpr{
				Name: funcName,
				Args: []Expr{
					UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
					Param("p_subject_type"),
					Null{},
					Null{},
				},
				Alias: "ls",
			},
		}},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: pattern.SourceRelations},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
				Right: Lit(pattern.SubjectRelation),
			},
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Via %s#%s (complex userset, LATERAL join)", pattern.SubjectType, pattern.SubjectRelation),
		},
		Query: stmt,
	}
}

// buildListSubjectsSimpleUsersetBlock builds a block for simple userset patterns.
// Uses JOIN with membership tuples to expand group membership.
func buildListSubjectsSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
				Right: Lit(pattern.SubjectRelation),
			},
		).
		JoinTuples("m",
			Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
			Eq{
				Left:  Col{Table: "m", Column: "object_id"},
				Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			},
			In{Expr: Col{Table: "m", Column: "relation"}, Values: pattern.SatisfyingRelations},
			Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Param("p_subject_type")},
			NoUserset{Source: Col{Table: "m", Column: "subject_id"}},
		).
		Select("m.subject_id").
		Distinct()

	applyWildcardExclusion(q, plan, "m")

	if pattern.IsClosurePattern && pattern.SourceRelation != "" {
		q.Where(CheckPermission{
			Subject: SubjectRef{
				Type: Param("p_subject_type"),
				ID:   Col{Table: "m", Column: "subject_id"},
			},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	applyExclusionPredicates(q, plan.Exclusions, plan.UseCTEExclusion)

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Via %s#%s (simple userset, JOIN)", pattern.SubjectType, pattern.SubjectRelation),
		},
		Query: q.Build(),
	}
}
