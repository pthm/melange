package sqlgen

import "fmt"

// SelfRefUsersetBlockSet contains blocks for a self-referential userset list function.
// These blocks are wrapped in a recursive CTE for member expansion.
type SelfRefUsersetBlockSet struct {
	// BaseBlocks are the base case blocks (depth=0) in the CTE
	BaseBlocks []TypedQueryBlock

	// RecursiveBlock is the recursive term block that expands self-referential usersets
	RecursiveBlock *TypedQueryBlock

	// SelfCandidateBlock is added outside the CTE (UNION with CTE result)
	SelfCandidateBlock *TypedQueryBlock
}

// HasRecursive returns true if there is a recursive block.
func (s SelfRefUsersetBlockSet) HasRecursive() bool {
	return s.RecursiveBlock != nil
}

// BuildListObjectsSelfRefUsersetBlocks builds blocks for a self-referential userset list_objects function.
func BuildListObjectsSelfRefUsersetBlocks(plan ListPlan) (SelfRefUsersetBlockSet, error) {
	baseBlocks, err := buildSelfRefUsersetBaseBlocks(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}

	return SelfRefUsersetBlockSet{
		BaseBlocks:         baseBlocks,
		RecursiveBlock:     buildSelfRefUsersetRecursiveBlock(plan),
		SelfCandidateBlock: buildListObjectsSelfCandidateBlock(plan),
	}, nil
}

// buildSelfRefUsersetBaseBlocks builds the base case blocks for self-referential userset CTE.
func buildSelfRefUsersetBaseBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildSelfRefUsersetDirectBlock(plan))
	blocks = append(blocks, buildSelfRefUsersetComplexClosureBlocks(plan)...)
	blocks = append(blocks, buildSelfRefUsersetIntersectionClosureBlocks(plan)...)
	blocks = append(blocks, buildSelfRefUsersetPatternBlocks(plan)...)
	return blocks, nil
}

func buildSelfRefUsersetDirectBlock(plan ListPlan) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{"-- Direct tuple lookup with simple closure relations"},
		Query:    q.Build(),
	}
}

func buildSelfRefUsersetComplexClosureBlocks(plan ListPlan) []TypedQueryBlock {
	if len(plan.ComplexClosure) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(plan.ComplexClosure))
	for _, rel := range plan.ComplexClosure {
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
				In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
				SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
				CheckPermission{
					Subject:     SubjectParams(),
					Relation:    rel,
					Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
					ExpectAllow: true,
				},
			).
			SelectCol("object_id").
			Distinct()

		for _, pred := range plan.Exclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
			Query:    q.Build(),
		})
	}

	return blocks
}

func buildSelfRefUsersetIntersectionClosureBlocks(plan ListPlan) []TypedQueryBlock {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(intersectionRels))
	for _, rel := range intersectionRels {
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Compose with intersection closure relation: %s", rel)},
			Query: SelectStmt{
				ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
				FromExpr: FunctionCallExpr{
					Name:  listObjectsFunctionName(plan.ObjectType, rel),
					Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
					Alias: "icr",
				},
			},
		})
	}

	return blocks
}

func buildSelfRefUsersetPatternBlocks(plan ListPlan) []TypedQueryBlock {
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
			blocks = append(blocks, buildSelfRefUsersetComplexPatternBlock(plan, pattern))
		} else {
			blocks = append(blocks, buildSelfRefUsersetSimplePatternBlock(plan, pattern))
		}
	}

	return blocks
}

func buildSelfRefUsersetComplexPatternBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
			CheckPermissionInternalExpr(
				SubjectParams(),
				pattern.SubjectRelation,
				ObjectRef{
					Type: Lit(pattern.SubjectType),
					ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				},
				true,
			),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Via %s#%s (complex userset)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    q.Build(),
	}
}

func buildSelfRefUsersetSimplePatternBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	membershipConditions := []Expr{
		Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: "m", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		In{Expr: Col{Table: "m", Column: "relation"}, Values: pattern.SatisfyingRelations},
		Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	if plan.AllowWildcard {
		membershipConditions = append(membershipConditions,
			Or(
				Eq{Left: Col{Table: "m", Column: "subject_id"}, Right: SubjectID},
				IsWildcard{Source: Col{Table: "m", Column: "subject_id"}},
			),
		)
	} else {
		membershipConditions = append(membershipConditions,
			Eq{Left: Col{Table: "m", Column: "subject_id"}, Right: SubjectID},
			NotExpr{Expr: IsWildcard{Source: Col{Table: "m", Column: "subject_id"}}},
		)
	}

	grantConditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}
	grantConditions = append(grantConditions, plan.Exclusions.BuildPredicates()...)

	if pattern.IsClosurePattern {
		grantConditions = append(grantConditions,
			CheckPermission{
				Subject:     SubjectParams(),
				Relation:    pattern.SourceRelation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: true,
			},
		)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Via %s#%s (simple userset)", pattern.SubjectType, pattern.SubjectRelation)},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
			FromExpr:    TableAs("melange_tuples", "t"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "m",
				On:    And(membershipConditions...),
			}},
			Where: And(grantConditions...),
		},
	}
}

func buildSelfRefUsersetRecursiveBlock(plan ListPlan) *TypedQueryBlock {
	exclusionPreds := plan.Exclusions.BuildPredicates()
	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(plan.Relation)},
		Raw("me.depth < 25"),
	)
	conditions = append(conditions, exclusionPreds...)

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-referential userset expansion",
			fmt.Sprintf("-- Patterns like [%s#%s] on %s.%s", plan.ObjectType, plan.Relation, plan.ObjectType, plan.Relation),
		},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}, Raw("me.depth + 1 AS depth")},
			FromExpr:    TableAs("member_expansion", "me"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "t",
				On:    Eq{Left: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, Right: Col{Table: "me", Column: "object_id"}},
			}},
			Where: And(conditions...),
		},
	}
}
