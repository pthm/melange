package sqlgen

import "fmt"

// SubjectsRecursiveBlockSet contains blocks for a recursive list_subjects function.
type SubjectsRecursiveBlockSet struct {
	RegularBlocks          []TypedQueryBlock
	RegularTTUBlocks       []TypedQueryBlock
	UsersetFilterBlocks    []TypedQueryBlock
	UsersetFilterSelfBlock *TypedQueryBlock
	ParentRelations        []ListParentRelationData
}

// BuildListSubjectsRecursiveBlocks builds blocks for a recursive list_subjects function.
func BuildListSubjectsRecursiveBlocks(plan ListPlan) (SubjectsRecursiveBlockSet, error) {
	parentRelations := buildListParentRelations(plan.Analysis)

	regularBlocks, err := buildListSubjectsRecursiveRegularBlocks(plan)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}

	usersetBlocks, err := buildSubjectsRecursiveUsersetFilterTypedBlocks(plan, parentRelations)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}

	return SubjectsRecursiveBlockSet{
		RegularBlocks:          regularBlocks,
		RegularTTUBlocks:       buildListSubjectsRecursiveTTUBlocks(plan, parentRelations),
		UsersetFilterBlocks:    usersetBlocks,
		UsersetFilterSelfBlock: buildListSubjectsUsersetFilterSelfBlock(plan),
		ParentRelations:        parentRelations,
	}, nil
}

// buildListSubjectsRecursiveRegularBlocks builds the regular path blocks for recursive list_subjects.
func buildListSubjectsRecursiveRegularBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	blocks := []TypedQueryBlock{buildListSubjectsRecursiveDirectBlock(plan)}

	blocks = append(blocks, buildListSubjectsRecursiveComplexClosureBlocks(plan)...)

	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(plan, "p_subject_type", "p_subject_type", plan.HasExclusion)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	usersetBlocks, err := buildListSubjectsRecursiveUsersetPatternBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	return blocks, nil
}

// buildListSubjectsRecursiveDirectBlock builds the direct tuple lookup block for recursive list_subjects.
func buildListSubjectsRecursiveDirectBlock(plan ListPlan) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
		).
		SelectCol("subject_id").
		Distinct()

	applyWildcardExclusion(q, plan, "t")
	applyExclusionPredicates(q, plan.Exclusions)

	return TypedQueryBlock{
		Comments: []string{"-- Direct tuple lookup with simple closure relations"},
		Query:    q.Build(),
	}
}

// buildListSubjectsRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildListSubjectsRecursiveComplexClosureBlocks(plan ListPlan) []TypedQueryBlock {
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
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
				NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				CheckPermission{
					Subject: SubjectRef{
						Type: SubjectType,
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
		applyExclusionPredicates(q, plan.Exclusions)

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
			Query:    q.Build(),
		})
	}

	return blocks
}

// buildListSubjectsRecursiveUsersetPatternBlocks builds blocks for userset patterns in recursive list_subjects.
func buildListSubjectsRecursiveUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern.IsComplex {
			blocks = append(blocks, buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern))
		} else {
			blocks = append(blocks, buildListSubjectsRecursiveSimpleUsersetBlock(plan, pattern))
		}
	}

	return blocks, nil
}

// buildListSubjectsRecursiveComplexUsersetBlock builds a complex userset block for recursive list_subjects.
func buildListSubjectsRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	const grantAlias, memberAlias = "g", "m"

	memberExclusions := buildExclusionInput(plan.Analysis, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

	checkExpr := CheckPermissionInternalExpr(
		SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
		pattern.SubjectRelation,
		ObjectRef{Type: Lit(pattern.SubjectType), ID: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		true,
	)

	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
	)

	whereConditions := []Expr{
		Eq{Left: Col{Table: grantAlias, Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: grantAlias, Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: grantAlias, Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: grantAlias, Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: grantAlias, Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: grantAlias, Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
		Eq{Left: Col{Table: memberAlias, Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		NoUserset{Source: Col{Table: memberAlias, Column: "subject_id"}},
		checkExpr,
	}

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", grantAlias),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: memberAlias,
			On:    joinCond,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (complex)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveSimpleUsersetBlock builds a simple userset block for recursive list_subjects.
func buildListSubjectsRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	const grantAlias, memberAlias = "g", "s"

	memberExclusions := buildExclusionInput(plan.Analysis, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		In{Expr: Col{Table: memberAlias, Column: "relation"}, Values: pattern.SatisfyingRelations},
	)

	whereConditions := []Expr{
		Eq{Left: Col{Table: grantAlias, Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: grantAlias, Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: grantAlias, Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: grantAlias, Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: grantAlias, Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: grantAlias, Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
		Eq{Left: Col{Table: memberAlias, Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		NoUserset{Source: Col{Table: memberAlias, Column: "subject_id"}},
	}

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", grantAlias),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: memberAlias,
			On:    joinCond,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (simple)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveTTUBlocks builds the TTU path blocks for recursive list_subjects.
func buildListSubjectsRecursiveTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) []TypedQueryBlock {
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsRecursiveTTUBlock(plan, parent))
	}
	return blocks
}

// buildListSubjectsRecursiveTTUBlock builds a single TTU path block for recursive list_subjects.
func buildListSubjectsRecursiveTTUBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	exclusions := buildExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sp", Column: "subject_id"})

	checkExpr := CheckPermissionInternalExpr(
		SubjectRef{Type: SubjectType, ID: Col{Table: "sp", Column: "subject_id"}},
		parent.Relation,
		ObjectRef{Type: Col{Table: "link", Column: "subject_type"}, ID: Col{Table: "link", Column: "subject_id"}},
		true,
	)

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		checkExpr,
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}
	whereConditions = append(whereConditions, exclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "sp", Column: "subject_id"}},
		FromExpr:    TableAs("subject_pool", "sp"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "melange_tuples",
			Alias: "link",
			On:    Bool(true),
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU: subjects via %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}

// buildSubjectsRecursiveUsersetFilterTypedBlocks builds the userset filter path blocks.
func buildSubjectsRecursiveUsersetFilterTypedBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsRecursiveUsersetFilterDirectBlock(plan))

	for _, parent := range parentRelations {
		blocks = append(blocks,
			buildListSubjectsRecursiveUsersetFilterTTUBlock(plan, parent),
			buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan, parent),
			buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan, parent),
		)
	}

	filterUsersetExpr := Concat{Parts: []Expr{Param("v_filter_type"), Lit("#"), Param("v_filter_relation")}}
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
			FromExpr: FunctionCallExpr{
				Name:  funcName,
				Args:  []Expr{ObjectID, filterUsersetExpr},
				Alias: "ics",
			},
		}
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection closure: %s", rel)},
			Query:    stmt,
		})
	}

	return blocks, nil
}

// buildListSubjectsRecursiveUsersetFilterDirectBlock builds the direct userset tuples block.
func buildListSubjectsRecursiveUsersetFilterDirectBlock(plan ListPlan) TypedQueryBlock {
	checkExpr := CheckPermission{
		Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
		Relation:    plan.Relation,
		Object:      LiteralObject(plan.ObjectType, Param("p_object_id")),
		ExpectAllow: true,
	}

	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)")},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{
			UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			Lit("#"),
			Param("v_filter_relation"),
		}},
		Name: "subject_id",
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
			Raw(closureStmt.Exists()),
			checkExpr,
		),
	}

	return TypedQueryBlock{
		Comments: []string{"-- Direct userset tuples"},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUBlock builds the TTU path block for userset filter.
func buildListSubjectsRecursiveUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	closureRelStmt := SelectStmt{
		Columns:  []string{"c.satisfying_relation"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
		),
	}

	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
		Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
		Or(
			Eq{Left: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)"), Right: Param("v_filter_relation")},
			Exists{Query: closureExistsStmt},
		),
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
				Raw("pt.relation IN ("+closureRelStmt.SQL()+")"),
			),
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU userset: %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock builds the intermediate TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Param("v_filter_type")},
		Exists{Query: closureExistsStmt},
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	subjectExpr := Raw("link.subject_id || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "link"),
		Where:       And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- TTU intermediate: parent object as userset reference"},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUNestedBlock builds the nested TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	lateralCall := LateralFunction{
		Name: "list_accessible_subjects",
		Args: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Lit(parent.Relation),
			SubjectType,
		},
		Alias: "nested",
	}

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "nested", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:      "CROSS",
			TableExpr: lateralCall,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- TTU nested: multi-hop chain resolution"},
		Query:    stmt,
	}
}
