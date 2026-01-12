package sqlgen

import "fmt"

// =============================================================================
// Composed Strategy Blocks (List Subjects)
// =============================================================================
func BuildListSubjectsComposedBlocks(plan ListPlan) (ComposedSubjectsBlockSet, error) {
	anchor := plan.Analysis.IndirectAnchor
	if anchor == nil || len(anchor.Path) == 0 {
		return ComposedSubjectsBlockSet{}, fmt.Errorf("missing indirect anchor data for %s.%s", plan.ObjectType, plan.Relation)
	}

	var result ComposedSubjectsBlockSet
	result.AllowedSubjectTypes = plan.AllowedSubjectTypes
	result.AnchorType = anchor.AnchorType
	result.AnchorRelation = anchor.AnchorRelation
	result.FirstStepType = anchor.Path[0].Type

	// Build self-candidate block
	selfBlock, err := buildComposedSubjectsSelfBlock(plan)
	if err != nil {
		return ComposedSubjectsBlockSet{}, err
	}
	result.SelfBlock = selfBlock

	// Build candidate blocks for both userset filter and regular paths
	candidateBlocks, err := buildTypedComposedSubjectsCandidateBlocks(plan, anchor)
	if err != nil {
		return ComposedSubjectsBlockSet{}, err
	}
	result.UsersetFilterBlocks = candidateBlocks
	result.RegularBlocks = candidateBlocks

	// Check for exclusions
	exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
	result.HasExclusions = len(exclusions.BuildPredicates()) > 0

	return result, nil
}

// buildComposedSubjectsSelfBlock builds the self-candidate block for list_subjects.
// This returns a userset subject_id like "document:1#viewer" when the filter type matches
// the object type and the filter relation satisfies the target relation.
func buildComposedSubjectsSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Subject ID output: object_id || '#' || filter_relation
	subjectIDCol := SelectAs(Concat{Parts: []Expr{ObjectID, Lit("#"), Param("v_filter_relation")}}, "subject_id")

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectIDCol},
		Where: And(
			Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: object_id#filter_relation when filter type matches object type",
		},
		Query: stmt,
	}, nil
}

// buildTypedComposedSubjectsCandidateBlocks builds candidate blocks for composed subjects.
// This is the typed version using TypedQueryBlock instead of string.
func buildTypedComposedSubjectsCandidateBlocks(plan ListPlan, anchor *IndirectAnchorInfo) ([]TypedQueryBlock, error) {
	if anchor == nil || len(anchor.Path) == 0 {
		return nil, nil
	}

	firstStep := anchor.Path[0]
	var blocks []TypedQueryBlock

	switch firstStep.Type {
	case "ttu":
		for _, targetType := range firstStep.AllTargetTypes {
			block, err := buildComposedTTUSubjectsBlock(plan, anchor, targetType)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)
		}

		for _, recursiveType := range firstStep.RecursiveTypes {
			block, err := buildComposedTTUSubjectsBlock(plan, anchor, recursiveType)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)
		}

	case "userset":
		block, err := buildComposedUsersetSubjectsBlock(plan, firstStep)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, *block)
	}

	return blocks, nil
}

// buildComposedTTUSubjectsBlock builds a TTU subjects candidate block.
func buildComposedTTUSubjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, targetType string) (*TypedQueryBlock, error) {
	// Use LateralFunction DSL type instead of string concatenation
	lateralFunc := LateralFunction{
		Name:  ListSubjectsFunctionName(targetType, anchor.Path[0].TargetRelation),
		Args:  []Expr{Col{Table: "link", Column: "subject_id"}, SubjectType},
		Alias: "s",
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Lit(targetType)},
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:      "CROSS",
			TableExpr: lateralFunc,
			On:        nil, // CROSS JOIN has no ON clause
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- From " + targetType + " parents",
		},
		Query: stmt,
	}, nil
}

// buildComposedUsersetSubjectsBlock builds a userset subjects candidate block.
func buildComposedUsersetSubjectsBlock(plan ListPlan, firstStep AnchorPathStep) (*TypedQueryBlock, error) {
	// Use LateralFunction DSL type instead of string concatenation
	// split_part(t.subject_id, '#', 1) extracts the object_id from the userset
	lateralFunc := LateralFunction{
		Name:  ListSubjectsFunctionName(firstStep.SubjectType, firstStep.SubjectRelation),
		Args:  []Expr{Raw("split_part(t.subject_id, '#', 1)"), SubjectType},
		Alias: "s",
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{{
			Type:      "CROSS",
			TableExpr: lateralFunc,
			On:        nil,
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Userset: " + firstStep.SubjectType + "#" + firstStep.SubjectRelation + " grants",
		},
		Query: stmt,
	}, nil
}
