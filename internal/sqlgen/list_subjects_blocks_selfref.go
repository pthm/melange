package sqlgen

import "fmt"

// Self-Referential Userset List Subjects Block Builders

// SelfRefUsersetSubjectsBlockSet contains blocks for a self-referential userset list_subjects function.
// This includes separate blocks for userset filter path and regular path.
type SelfRefUsersetSubjectsBlockSet struct {
	// UsersetFilterBlocks are blocks for the userset filter path (when p_subject_type contains '#')
	UsersetFilterBlocks []TypedQueryBlock

	// UsersetFilterSelfBlock is the self-candidate block for userset filter path
	UsersetFilterSelfBlock *TypedQueryBlock

	// UsersetFilterRecursiveBlock is the recursive block for userset filter expansion
	UsersetFilterRecursiveBlock *TypedQueryBlock

	// RegularBlocks are blocks for the regular path (individual subjects)
	RegularBlocks []TypedQueryBlock

	// UsersetObjectsBaseBlock is the base block for userset objects CTE
	UsersetObjectsBaseBlock *TypedQueryBlock

	// UsersetObjectsRecursiveBlock is the recursive block for userset objects CTE
	UsersetObjectsRecursiveBlock *TypedQueryBlock
}

// BuildListSubjectsSelfRefUsersetBlocks builds blocks for a self-referential userset list_subjects function.
func BuildListSubjectsSelfRefUsersetBlocks(plan ListPlan) (SelfRefUsersetSubjectsBlockSet, error) {
	filterBlocks, filterSelf, filterRecursive := buildSelfRefUsersetFilterBlocks(plan)
	regularBlocks, usersetBase, usersetRecursive := buildSelfRefUsersetRegularBlocks(plan)

	return SelfRefUsersetSubjectsBlockSet{
		UsersetFilterBlocks:          filterBlocks,
		UsersetFilterSelfBlock:       filterSelf,
		UsersetFilterRecursiveBlock:  filterRecursive,
		RegularBlocks:                regularBlocks,
		UsersetObjectsBaseBlock:      usersetBase,
		UsersetObjectsRecursiveBlock: usersetRecursive,
	}, nil
}

func buildSelfRefUsersetFilterBlocks(plan ListPlan) (blocks []TypedQueryBlock, selfBlock, recursiveBlock *TypedQueryBlock) {
	blocks = make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildSelfRefUsersetFilterBaseBlock(plan))
	blocks = append(blocks, buildSelfRefUsersetFilterIntersectionBlocks(plan)...)

	return blocks, buildSelfRefUsersetFilterSelfBlock(plan), buildSelfRefUsersetFilterRecursiveBlock()
}

func buildSelfRefUsersetFilterBaseBlock(plan ListPlan) TypedQueryBlock {
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	return TypedQueryBlock{
		Comments: []string{"-- Userset filter: find userset tuples that match filter type/relation"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
				Raw("0 AS depth"),
			},
			FromExpr: TableAs("melange_tuples", "t"),
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
				CheckPermissionExpr(
					"check_permission",
					SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
					plan.Relation,
					ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
					true,
				),
			),
		},
	}
}

func buildSelfRefUsersetFilterIntersectionBlocks(plan ListPlan) []TypedQueryBlock {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil
	}

	filterUsersetExpr := Concat{Parts: []Expr{Param("v_filter_type"), Lit("#"), Param("v_filter_relation")}}
	blocks := make([]TypedQueryBlock, 0, len(intersectionRels))

	for _, rel := range intersectionRels {
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Compose with intersection closure relation: %s", rel)},
			Query: SelectStmt{
				Distinct: true,
				ColumnExprs: []Expr{
					Raw("split_part(icr.subject_id, '#', 1) AS userset_object_id"),
					Raw("0 AS depth"),
				},
				FromExpr: FunctionCallExpr{
					Name:  listSubjectsFunctionName(plan.ObjectType, rel),
					Args:  []Expr{ObjectID, filterUsersetExpr},
					Alias: "icr",
				},
			},
		})
	}

	return blocks
}

func buildSelfRefUsersetFilterSelfBlock(plan ListPlan) *TypedQueryBlock {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{
			ObjectID,
			Lit("#"),
			Param("v_filter_relation"),
		}},
		Name: "subject_id",
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-candidate: when filter type matches object type"},
		Query: SelectStmt{
			ColumnExprs: []Expr{subjectExpr},
			Where: And(
				Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
				Raw(closureStmt.Exists()),
			),
		},
	}
}

func buildSelfRefUsersetFilterRecursiveBlock() *TypedQueryBlock {
	return &TypedQueryBlock{
		Comments: []string{"-- Recursive userset expansion for filter path"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
				Raw("ue.depth + 1 AS depth"),
			},
			FromExpr: TableAs("userset_expansion", "ue"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
			}},
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Raw("v_filter_type")},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
				Eq{Left: Col{Table: "t", Column: "relation"}, Right: Raw("v_filter_relation")},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Raw("v_filter_type")},
				HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
				Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Raw("v_filter_relation")},
				Raw("ue.depth < 25"),
			),
		},
	}
}

// buildSelfRefUsersetRegularBlocks builds blocks for the regular path (individual subjects).
func buildSelfRefUsersetRegularBlocks(plan ListPlan) (blocks []TypedQueryBlock, baseBlock, recursiveBlock *TypedQueryBlock) {
	exclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "t", Column: "subject_id"},
	)

	blocks = make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildSelfRefUsersetRegularDirectBlock(plan, exclusions))
	blocks = append(blocks, buildSelfRefUsersetRegularComplexClosureBlocks(plan, exclusions)...)
	blocks = append(blocks, buildSelfRefUsersetRegularIntersectionClosureBlocks(plan)...)
	blocks = append(blocks, buildSelfRefUsersetRegularExpansionBlock(plan, exclusions))
	blocks = append(blocks, buildSelfRefUsersetRegularPatternBlocks(plan)...)

	return blocks, buildSelfRefUsersetObjectsBaseBlock(plan), buildSelfRefUsersetObjectsRecursiveBlock(plan)
}

func buildSelfRefUsersetRegularDirectBlock(plan ListPlan, exclusions ExclusionConfig) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		WhereObjectID(ObjectID).
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}).
		SelectCol("subject_id").
		Distinct()

	applyWildcardExclusion(q, plan, "t")
	applyExclusionPredicates(q, exclusions)

	return TypedQueryBlock{
		Comments: []string{"-- Path 1: Direct tuple lookup on the object itself"},
		Query:    q.Build(),
	}
}

func buildSelfRefUsersetRegularComplexClosureBlocks(plan ListPlan, exclusions ExclusionConfig) []TypedQueryBlock {
	if len(plan.ComplexClosure) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(plan.ComplexClosure))
	for _, rel := range plan.ComplexClosure {
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			WhereObjectID(ObjectID).
			WhereSubjectType(SubjectType).
			Where(
				In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
				CheckPermissionInternalExpr(
					SubjectRef{Type: SubjectType, ID: Col{Table: "t", Column: "subject_id"}},
					rel,
					ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
					true,
				),
			).
			SelectCol("subject_id").
			Distinct()

		applyWildcardExclusion(q, plan, "t")
		applyExclusionPredicates(q, exclusions)

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
			Query:    q.Build(),
		})
	}

	return blocks
}

func buildSelfRefUsersetRegularIntersectionClosureBlocks(plan ListPlan) []TypedQueryBlock {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(intersectionRels))
	for _, rel := range intersectionRels {
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Compose with intersection closure relation: %s", rel)},
			Query: SelectStmt{
				ColumnExprs: []Expr{Col{Table: "icr", Column: "subject_id"}},
				FromExpr: FunctionCallExpr{
					Name:  listSubjectsFunctionName(plan.ObjectType, rel),
					Args:  []Expr{ObjectID, SubjectType},
					Alias: "icr",
				},
			},
		})
	}

	return blocks
}

func buildSelfRefUsersetRegularExpansionBlock(plan ListPlan, exclusions ExclusionConfig) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	if plan.ExcludeWildcard() {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	conditions = append(conditions, exclusions.BuildPredicates()...)

	return TypedQueryBlock{
		Comments: []string{"-- Path 2: Expand userset subjects from all reachable userset objects"},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
			FromExpr:    TableAs("userset_objects", "uo"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
			}},
			Where: And(conditions...),
		},
	}
}

func buildSelfRefUsersetRegularPatternBlocks(plan ListPlan) []TypedQueryBlock {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsSelfReferential {
			continue
		}
		if pattern.IsComplex {
			blocks = append(blocks, buildSelfRefUsersetRegularComplexPatternBlock(plan, pattern))
		} else {
			blocks = append(blocks, buildSelfRefUsersetRegularSimplePatternBlock(plan, pattern))
		}
	}

	return blocks
}

func buildSelfRefUsersetRegularComplexPatternBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	grantConditions := []Expr{
		Eq{Left: Col{Table: "g", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "g", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "g", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "g", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "g", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "g", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}

	subjectExclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "s", Column: "subject_id"},
	)

	whereClause := And(grantConditions...)
	for _, pred := range subjectExclusions.BuildPredicates() {
		whereClause = And(whereClause, pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
			"-- Complex userset: use LATERAL list function",
		},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
			FromExpr:    TableAs("melange_tuples", "g"),
			Joins: []JoinClause{{
				Type: "CROSS",
				TableExpr: LateralFunction{
					Name: listSubjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation),
					Args: []Expr{
						Func{Name: "split_part", Args: []Expr{Col{Table: "g", Column: "subject_id"}, Lit("#"), Int(1)}},
						SubjectType,
						Null{},
						Null{},
					},
					Alias: "s",
				},
			}},
			Where: whereClause,
		},
	}
}

func buildSelfRefUsersetRegularSimplePatternBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	subjectExclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "s", Column: "subject_id"},
	)

	membershipConditions := []Expr{
		Eq{Left: Col{Table: "s", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: "s", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "g", Column: "subject_id"}}},
		In{Expr: Col{Table: "s", Column: "relation"}, Values: pattern.SatisfyingRelations},
		Eq{Left: Col{Table: "s", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	if plan.ExcludeWildcard() {
		membershipConditions = append(membershipConditions, Ne{Left: Col{Table: "s", Column: "subject_id"}, Right: Lit("*")})
	}

	grantConditions := []Expr{
		Eq{Left: Col{Table: "g", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "g", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "g", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "g", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "g", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "g", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}

	if pattern.IsClosurePattern {
		grantConditions = append(grantConditions, CheckPermissionExpr(
			"check_permission",
			SubjectRef{Type: SubjectType, ID: Col{Table: "s", Column: "subject_id"}},
			pattern.SourceRelation,
			ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
			true,
		))
	}

	grantConditions = append(grantConditions, subjectExclusions.BuildPredicates()...)

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
			FromExpr:    TableAs("melange_tuples", "g"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "s",
				On:    And(membershipConditions...),
			}},
			Where: And(grantConditions...),
		},
	}
}

func buildSelfRefUsersetObjectsBaseBlock(plan ListPlan) *TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Select("split_part(t.subject_id, '#', 1) AS userset_object_id", "0 AS depth").
		WhereObjectID(ObjectID).
		Where(Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)}).
		WhereHasUserset().
		WhereUsersetRelation(plan.Relation).
		Distinct()

	return &TypedQueryBlock{
		Comments: []string{"-- Base case: find initial userset references"},
		Query:    q.Build(),
	}
}

func buildSelfRefUsersetObjectsRecursiveBlock(plan ListPlan) *TypedQueryBlock {
	return &TypedQueryBlock{
		Comments: []string{"-- Recursive case: expand self-referential userset references"},
		Query: SelectStmt{
			Distinct: true,
			ColumnExprs: []Expr{
				Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"),
				Raw("uo.depth + 1 AS depth"),
			},
			FromExpr: TableAs("userset_objects", "uo"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
			}},
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
				Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(plan.Relation)},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
				HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
				Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(plan.Relation)},
				Raw("uo.depth < 25"),
			),
		},
	}
}
