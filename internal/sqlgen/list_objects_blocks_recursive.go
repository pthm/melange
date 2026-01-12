package sqlgen

import (
	"fmt"
	"strings"
)

// =============================================================================
// Recursive List Objects Block Builders (TTU patterns)
// =============================================================================

// RecursiveBlockSet contains blocks for a recursive list function.
// These blocks are wrapped in a CTE with depth tracking.
type RecursiveBlockSet struct {
	// BaseBlocks are the base case blocks (depth=0) in the CTE
	BaseBlocks []TypedQueryBlock

	// RecursiveBlock is the recursive term block (references the CTE)
	RecursiveBlock *TypedQueryBlock

	// SelfCandidateBlock is added outside the CTE
	SelfCandidateBlock *TypedQueryBlock

	// SelfRefLinkingRelations are the linking relations for self-referential TTU
	// Used for depth check before query execution
	SelfRefLinkingRelations []string
}

// HasRecursive returns true if there is a recursive block.
func (r RecursiveBlockSet) HasRecursive() bool {
	return r.RecursiveBlock != nil
}

// BuildListObjectsRecursiveBlocks builds blocks for a recursive list_objects function.
// This handles TTU patterns with depth tracking and recursive CTEs.
func BuildListObjectsRecursiveBlocks(plan ListPlan) (RecursiveBlockSet, error) {
	var result RecursiveBlockSet

	// Compute parent relations from analysis
	parentRelations := buildListParentRelations(plan.Analysis)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	result.SelfRefLinkingRelations = dequoteLinkingRelations(selfRefSQL)

	// Build base blocks
	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations)
	if err != nil {
		return RecursiveBlockSet{}, err
	}
	result.BaseBlocks = baseBlocks

	// Build recursive block if there are self-referential TTU patterns
	if len(result.SelfRefLinkingRelations) > 0 {
		recursiveBlock, err := buildRecursiveTTUBlock(plan, result.SelfRefLinkingRelations)
		if err != nil {
			return RecursiveBlockSet{}, err
		}
		result.RecursiveBlock = recursiveBlock
	}

	// Build self-candidate block
	selfBlock, err := buildListObjectsSelfCandidateBlock(plan)
	if err != nil {
		return RecursiveBlockSet{}, err
	}
	result.SelfCandidateBlock = selfBlock

	return result, nil
}

// buildRecursiveBaseBlocks builds the base case blocks for the recursive CTE.
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Base exclusions for tuple lookup blocks
	baseExclusions := plan.Exclusions

	// Direct tuple lookup block
	directBlock, err := buildRecursiveDirectBlock(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Complex closure blocks
	complexBlocks, err := buildRecursiveComplexClosureBlocks(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildRecursiveIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Userset pattern blocks
	usersetBlocks, err := buildRecursiveUsersetPatternBlocks(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	// Cross-type TTU blocks (non-recursive, uses check_permission_internal)
	crossTTUBlocks, err := buildCrossTypeTTUBlocks(plan, parentRelations)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, crossTTUBlocks...)

	return blocks, nil
}

// buildRecursiveDirectBlock builds the direct tuple lookup block for recursive CTEs.
func buildRecursiveDirectBlock(plan ListPlan, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		Query: q.Build(),
	}, nil
}

// buildRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildRecursiveComplexClosureBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
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
		for _, pred := range exclusions.BuildPredicates() {
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

// buildRecursiveIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildRecursiveIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	if len(plan.Analysis.IntersectionClosureRelations) == 0 {
		return nil, nil
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
			Comments: []string{
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			Query: stmt,
		})
	}

	return blocks, nil
}

// buildRecursiveUsersetPatternBlocks builds blocks for userset patterns.
func buildRecursiveUsersetPatternBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsComplex {
			block, err := buildRecursiveComplexUsersetBlock(plan, pattern, exclusions)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildRecursiveSimpleUsersetBlock(plan, pattern, exclusions)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildRecursiveComplexUsersetBlock builds a block for complex userset patterns.
func buildRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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

	// Add exclusion predicates
	for _, pred := range exclusions.BuildPredicates() {
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

// buildRecursiveSimpleUsersetBlock builds a block for simple userset patterns.
func buildRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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

	// Add exclusion predicates
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: q.Build(),
	}, nil
}

// buildCrossTypeTTUBlocks builds blocks for cross-type TTU patterns.
// These are non-recursive and use check_permission_internal.
func buildCrossTypeTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
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

		// Add exclusion predicates
		for _, pred := range crossExclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Cross-type TTU: %s -> %s on non-self types", parent.LinkingRelation, parent.Relation),
				"-- Find objects whose linking relation points to a parent where subject has relation",
				"-- This is non-recursive (uses check_permission_internal, not CTE reference)",
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildRecursiveTTUBlock builds the recursive term block for self-referential TTU.
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string) (*TypedQueryBlock, error) {
	recursiveExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "child", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	// Build the recursive query that joins with the CTE
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

	// Add exclusion predicates
	predicates := recursiveExclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]Expr{stmt.Where}, predicates...)
		stmt.Where = And(allPredicates...)
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-referential TTU: follow linking relations to accessible parents",
			"-- Combined all self-referential TTU patterns into single recursive term",
		},
		Query: stmt,
	}, nil
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
