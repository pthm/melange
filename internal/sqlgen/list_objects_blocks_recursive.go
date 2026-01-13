package sqlgen

import (
	"fmt"
	"strings"
)

// RecursiveBlockSet contains blocks for a recursive list function.
type RecursiveBlockSet struct {
	BaseBlocks              []TypedQueryBlock
	RecursiveBlock          *TypedQueryBlock
	SelfCandidateBlock      *TypedQueryBlock
	SelfRefLinkingRelations []string
}

// HasRecursive returns true if there is a recursive block.
func (r RecursiveBlockSet) HasRecursive() bool {
	return r.RecursiveBlock != nil
}

// BuildListObjectsRecursiveBlocks builds blocks for a recursive list_objects function.
// This handles TTU patterns with depth tracking and recursive CTEs.
func BuildListObjectsRecursiveBlocks(plan ListPlan) (RecursiveBlockSet, error) {
	parentRelations := buildListParentRelations(plan.Analysis)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	selfRefLinkingRelations := dequoteLinkingRelations(selfRefSQL)

	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations)
	if err != nil {
		return RecursiveBlockSet{}, err
	}

	result := RecursiveBlockSet{
		BaseBlocks:              baseBlocks,
		SelfRefLinkingRelations: selfRefLinkingRelations,
		SelfCandidateBlock:      buildListObjectsSelfCandidateBlock(plan),
	}

	if len(selfRefLinkingRelations) > 0 {
		result.RecursiveBlock = buildRecursiveTTUBlock(plan, selfRefLinkingRelations)
	}

	return result, nil
}

// buildRecursiveBaseBlocks builds the base case blocks for the recursive CTE.
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)

	blocks = append(blocks, buildRecursiveDirectBlock(plan))
	blocks = append(blocks, buildRecursiveComplexClosureBlocks(plan)...)
	blocks = append(blocks, buildRecursiveIntersectionClosureBlocks(plan)...)
	blocks = append(blocks, buildRecursiveUsersetPatternBlocks(plan)...)
	blocks = append(blocks, buildCrossTypeTTUBlocks(plan, parentRelations)...)

	return blocks, nil
}

// buildRecursiveDirectBlock builds the direct tuple lookup block for recursive CTEs.
func buildRecursiveDirectBlock(plan ListPlan) TypedQueryBlock {
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

// buildRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildRecursiveComplexClosureBlocks(plan ListPlan) []TypedQueryBlock {
	if len(plan.ComplexClosure) == 0 {
		return nil
	}

	var blocks []TypedQueryBlock
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

// buildRecursiveIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildRecursiveIntersectionClosureBlocks(plan ListPlan) []TypedQueryBlock {
	if len(plan.Analysis.IntersectionClosureRelations) == 0 {
		return nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listObjectsFunctionName(plan.ObjectType, rel)
		stmt := SelectStmt{
			ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
			FromExpr: FunctionCallExpr{
				Name:  funcName,
				Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
				Alias: "icr",
			},
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection closure: %s", rel)},
			Query:    stmt,
		})
	}

	return blocks
}

// buildRecursiveUsersetPatternBlocks builds blocks for userset patterns.
func buildRecursiveUsersetPatternBlocks(plan ListPlan) []TypedQueryBlock {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsComplex {
			blocks = append(blocks, buildRecursiveComplexUsersetBlock(plan, pattern))
		} else {
			blocks = append(blocks, buildRecursiveSimpleUsersetBlock(plan, pattern))
		}
	}

	return blocks
}

// buildRecursiveComplexUsersetBlock builds a block for complex userset patterns.
func buildRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
				Right: Lit(pattern.SubjectRelation),
			},
			CheckPermission{
				Subject:  SubjectParams(),
				Relation: pattern.SubjectRelation,
				Object: ObjectRef{
					Type: Lit(pattern.SubjectType),
					ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				},
				ExpectAllow: true,
			},
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (complex)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    q.Build(),
	}
}

// buildRecursiveSimpleUsersetBlock builds a block for simple userset patterns.
func buildRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
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
			Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "m", Column: "subject_id"}, SubjectID, pattern.HasWildcard),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (simple)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    q.Build(),
	}
}

// buildCrossTypeTTUBlocks builds blocks for cross-type TTU patterns.
// These are non-recursive and use check_permission_internal.
func buildCrossTypeTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) []TypedQueryBlock {
	var blocks []TypedQueryBlock

	for _, parent := range parentRelations {
		if !parent.HasCrossTypeLinks {
			continue
		}

		crossTypes := dequoteLinkingRelations(parent.CrossTypeLinkingTypes)
		crossExclusions := buildExclusionInput(
			plan.Analysis,
			Col{Table: "child", Column: "object_id"},
			SubjectType,
			SubjectID,
		)

		q := Tuples("child").
			ObjectType(plan.ObjectType).
			Relations(parent.LinkingRelation).
			Where(
				In{Expr: Col{Table: "child", Column: "subject_type"}, Values: crossTypes},
				CheckPermission{
					Subject:  SubjectParams(),
					Relation: parent.Relation,
					Object: ObjectRef{
						Type: Col{Table: "child", Column: "subject_type"},
						ID:   Col{Table: "child", Column: "subject_id"},
					},
					ExpectAllow: true,
				},
			).
			SelectCol("object_id").
			Distinct()

		for _, pred := range crossExclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Cross-type TTU: %s -> %s", parent.LinkingRelation, parent.Relation)},
			Query:    q.Build(),
		})
	}

	return blocks
}

// buildRecursiveTTUBlock builds the recursive term block for self-referential TTU.
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string) *TypedQueryBlock {
	exclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "child", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth"},
		From:     "accessible",
		Alias:    "a",
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					In{Expr: Col{Table: "child", Column: "relation"}, Values: linkingRelations},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
	}

	if predicates := exclusions.BuildPredicates(); len(predicates) > 0 {
		stmt.Where = And(append([]Expr{stmt.Where}, predicates...)...)
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-referential TTU: follow linking relations to accessible parents"},
		Query:    stmt,
	}
}

// dequoteLinkingRelations extracts relation names from a SQL-formatted list.
// e.g., "'parent', 'container'" -> ["parent", "container"]
func dequoteLinkingRelations(sqlList string) []string {
	if sqlList == "" {
		return nil
	}
	parts := strings.Split(sqlList, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(strings.Trim(part, "'"))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
