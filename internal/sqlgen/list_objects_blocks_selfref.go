package sqlgen

import "fmt"

// =============================================================================
// Self-Referential Userset Block Builders (List Objects)
// =============================================================================
// =============================================================================
// Self-Referential Userset Block Builders
// =============================================================================
//
// These builders handle patterns like [group#member] on group.member,
// which require recursive CTE expansion to find all objects reachable
// through self-referential userset chains.

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
	var result SelfRefUsersetBlockSet

	// Build base blocks
	baseBlocks, err := buildSelfRefUsersetBaseBlocks(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.BaseBlocks = baseBlocks

	// Build recursive block for self-referential userset expansion
	recursiveBlock, err := buildSelfRefUsersetRecursiveBlock(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.RecursiveBlock = recursiveBlock

	// Build self-candidate block
	selfBlock, err := buildListObjectsSelfCandidateBlock(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.SelfCandidateBlock = selfBlock

	return result, nil
}

// buildSelfRefUsersetBaseBlocks builds the base case blocks for self-referential userset CTE.
func buildSelfRefUsersetBaseBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Direct tuple lookup block
	directBlock, err := buildSelfRefUsersetDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Complex closure blocks
	complexBlocks, err := buildSelfRefUsersetComplexClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildSelfRefUsersetIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Userset pattern blocks (excluding self-referential ones)
	usersetBlocks, err := buildSelfRefUsersetPatternBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	return blocks, nil
}

// buildSelfRefUsersetDirectBlock builds the direct tuple lookup block for self-ref userset CTE.
func buildSelfRefUsersetDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
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

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		Query: q.Build(),
	}, nil
}

// buildSelfRefUsersetComplexClosureBlocks builds blocks for complex closure relations.
func buildSelfRefUsersetComplexClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	if len(plan.ComplexClosure) == 0 {
		return nil, nil
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

		// Add exclusion predicates
		for _, pred := range plan.Exclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Complex closure relation: %s", rel),
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildSelfRefUsersetIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildSelfRefUsersetIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range intersectionRels {
		funcName := listObjectsFunctionName(plan.ObjectType, rel)

		// Build the subquery that calls the intersection function
		stmt := SelectStmt{
			ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
			FromExpr: FunctionCallExpr{
				Name:  funcName,
				Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
				Alias: "icr",
			},
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			Query: stmt,
		})
	}

	return blocks, nil
}

// buildSelfRefUsersetPatternBlocks builds blocks for userset patterns (excluding self-referential).
func buildSelfRefUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		// Skip self-referential patterns - they're handled by the recursive block
		if pattern.IsSelfReferential {
			continue
		}

		if pattern.IsComplex {
			block, err := buildSelfRefUsersetComplexPatternBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildSelfRefUsersetSimplePatternBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildSelfRefUsersetComplexPatternBlock builds a block for a complex userset pattern.
func buildSelfRefUsersetComplexPatternBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	// For complex patterns, use check_permission_internal to verify membership
	checkExpr := CheckPermissionInternalExpr(
		SubjectParams(),
		pattern.SubjectRelation,
		ObjectRef{
			Type: Lit(pattern.SubjectType),
			ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
		},
		true,
	)

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
			checkExpr,
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Complex userset: use check_permission_internal for membership",
		},
		Query: q.Build(),
	}, nil
}

// buildSelfRefUsersetSimplePatternBlock builds a block for a simple userset pattern.
func buildSelfRefUsersetSimplePatternBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	// For simple patterns, use a JOIN with membership tuples
	membershipConditions := []Expr{
		Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: "m", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		In{Expr: Col{Table: "m", Column: "relation"}, Values: pattern.SatisfyingRelations},
		Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	// Subject ID matching for membership
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

	// Add exclusion predicates
	grantConditions = append(grantConditions, plan.Exclusions.BuildPredicates()...)

	// For closure patterns, add check_permission validation
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

	stmt := SelectStmt{
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
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetRecursiveBlock builds the recursive block for self-referential userset expansion.
func buildSelfRefUsersetRecursiveBlock(plan ListPlan) (*TypedQueryBlock, error) {
	// The recursive block expands userset chains like:
	// group:A#member -> group:B (where B has tuple group:B#member -> group:C#member)
	// This finds new objects reachable through self-referential userset references.

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

	stmt := SelectStmt{
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
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-referential userset expansion",
			fmt.Sprintf("-- For patterns like [%s#%s] on %s.%s", plan.ObjectType, plan.Relation, plan.ObjectType, plan.Relation),
		},
		Query: stmt,
	}, nil
}
