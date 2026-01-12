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
	listFunction := fmt.Sprintf("list_%s_%s_subjects(link.subject_id, p_subject_type)", targetType, anchor.Path[0].TargetRelation)

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
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil, // CROSS JOIN has no ON clause
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- From %s parents", targetType),
		},
		Query: stmt,
	}, nil
}

// buildComposedUsersetSubjectsBlock builds a userset subjects candidate block.
func buildComposedUsersetSubjectsBlock(plan ListPlan, firstStep AnchorPathStep) (*TypedQueryBlock, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), p_subject_type)", firstStep.SubjectType, firstStep.SubjectRelation)

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
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil,
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset: %s#%s grants", firstStep.SubjectType, firstStep.SubjectRelation),
		},
		Query: stmt,
	}, nil
}
