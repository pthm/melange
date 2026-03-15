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

	// Compute which relations should propagate through the recursive step.
	// Only relations that satisfy a self-referential TTU target should propagate.
	propagatableRelations := computePropagatableRelations(plan, parentRelations)

	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations, propagatableRelations)
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

// computePropagatableRelations returns the set of relations whose results should
// seed the recursive step. A relation is propagatable if it satisfies any relation
// that has a self-referential TTU pattern. For example, with "can_view: viewer or
// folder_viewer" where viewer has "viewer from parent", the propagatable set is
// {viewer, editor, manager} — relations that satisfy "viewer". folder_viewer is
// excluded because it does not satisfy any TTU-bearing relation.
func computePropagatableRelations(plan ListPlan, parentRelations []ListParentRelationData) map[string]bool {
	result := make(map[string]bool)

	for _, p := range parentRelations {
		if !p.IsSelfReferential {
			continue
		}
		ttuRelation := p.Relation // e.g., "viewer"

		if plan.AnalysisLookup != nil {
			key := plan.ObjectType + "." + ttuRelation
			if relAnalysis, ok := plan.AnalysisLookup[key]; ok {
				for _, sat := range relAnalysis.SatisfyingRelations {
					result[sat] = true
				}
			}
		}

		result[ttuRelation] = true
	}

	return result
}

// anyRelationPropagatable checks if any relation in the list is in the propagatable set.
func anyRelationPropagatable(relations []string, propagatable map[string]bool) bool {
	for _, r := range relations {
		if propagatable[r] {
			return true
		}
	}
	return false
}

// buildRecursiveBaseBlocks builds the base case blocks for the recursive CTE.
// Each block is tagged with Propagatable based on whether its source relation
// satisfies a self-referential TTU target.
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData, propagatable map[string]bool) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)

	directBlock := buildRecursiveDirectBlock(plan)
	directBlock.Propagatable = anyRelationPropagatable(plan.RelationList, propagatable)
	blocks = append(blocks, directBlock)

	for _, rel := range plan.ComplexClosure {
		block := buildRecursiveComplexClosureBlock(plan, rel)
		block.Propagatable = propagatable[rel]
		blocks = append(blocks, block)
	}

	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		block := buildRecursiveIntersectionClosureBlock(plan, rel)
		block.Propagatable = propagatable[rel]
		blocks = append(blocks, block)
	}

	patterns := buildListUsersetPatternInputs(plan.Analysis)
	for _, pattern := range patterns {
		var block TypedQueryBlock
		if pattern.IsComplex {
			block = buildRecursiveComplexUsersetBlock(plan, pattern)
		} else {
			block = buildRecursiveSimpleUsersetBlock(plan, pattern)
		}
		block.Propagatable = anyRelationPropagatable(pattern.SourceRelations, propagatable)
		blocks = append(blocks, block)
	}

	// Cross-type TTU blocks are non-propagatable (one-hop, not self-referential).
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

// buildRecursiveComplexClosureBlock builds a block for a single complex closure relation.
func buildRecursiveComplexClosureBlock(plan ListPlan, rel string) TypedQueryBlock {
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

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
		Query:    q.Build(),
	}
}

// buildRecursiveIntersectionClosureBlock builds a block for a single intersection closure relation.
func buildRecursiveIntersectionClosureBlock(plan ListPlan, rel string) TypedQueryBlock {
	funcName := listObjectsFunctionName(plan.ObjectType, rel)
	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
		FromExpr: FunctionCallExpr{
			Name:  funcName,
			Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias: "icr",
		},
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Intersection closure: %s", rel)},
		Query:    stmt,
	}
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
// Filters on a.propagatable to ensure only results from TTU-bearing relations
// seed the recursive step (e.g., folder_viewer results do NOT propagate).
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string) *TypedQueryBlock {
	exclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "child", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth", "TRUE AS propagatable"},
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
		Where: And(
			Col{Table: "a", Column: "propagatable"},
			Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
		),
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
