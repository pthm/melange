package sqlgen

import (
	"fmt"
	"slices"
)

// BuildListSubjectsComposedBlocks builds block set for composed list_subjects function.
func BuildListSubjectsComposedBlocks(plan ListPlan) (ComposedSubjectsBlockSet, error) {
	anchor := plan.Analysis.IndirectAnchor
	if anchor == nil || len(anchor.Path) == 0 {
		return ComposedSubjectsBlockSet{}, fmt.Errorf("missing indirect anchor data for %s.%s", plan.ObjectType, plan.Relation)
	}

	candidateBlocks := buildComposedSubjectsCandidateBlocks(plan, anchor)
	exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})

	return ComposedSubjectsBlockSet{
		SelfBlock:           buildComposedSubjectsSelfBlock(plan),
		UsersetFilterBlocks: candidateBlocks,
		RegularBlocks:       candidateBlocks,
		AllowedSubjectTypes: plan.AllowedSubjectTypes,
		HasExclusions:       len(exclusions.BuildPredicates()) > 0,
		AnchorType:          anchor.AnchorType,
		AnchorRelation:      anchor.AnchorRelation,
		FirstStepType:       anchor.Path[0].Type,
	}, nil
}

func buildComposedSubjectsSelfBlock(plan ListPlan) *TypedQueryBlock {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-candidate: object_id#filter_relation when filter type matches object type"},
		Query: SelectStmt{
			ColumnExprs: []Expr{SelectAs(Concat{Parts: []Expr{ObjectID, Lit("#"), Param("v_filter_relation")}}, "subject_id")},
			Where: And(
				Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
				Raw(closureStmt.Exists()),
			),
		},
	}
}

func buildComposedSubjectsCandidateBlocks(plan ListPlan, anchor *IndirectAnchorInfo) []TypedQueryBlock {
	firstStep := anchor.Path[0]

	switch firstStep.Type {
	case "ttu":
		allTypes := slices.Concat(firstStep.AllTargetTypes, firstStep.RecursiveTypes)
		blocks := make([]TypedQueryBlock, 0, len(allTypes))
		for _, targetType := range allTypes {
			blocks = append(blocks, buildComposedTTUSubjectsBlock(plan, anchor, targetType))
		}
		return blocks

	case "userset":
		return []TypedQueryBlock{buildComposedUsersetSubjectsBlock(plan, firstStep)}

	default:
		return nil
	}
}

func buildComposedTTUSubjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, targetType string) TypedQueryBlock {
	firstStep := anchor.Path[0]

	return TypedQueryBlock{
		Comments: []string{"-- From " + targetType + " parents"},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
			FromExpr:    TableAs("melange_tuples", "link"),
			Joins: []JoinClause{{
				Type: "CROSS",
				TableExpr: LateralFunction{
					Name:  ListSubjectsFunctionName(targetType, firstStep.TargetRelation),
					Args:  []Expr{Col{Table: "link", Column: "subject_id"}, SubjectType},
					Alias: "s",
				},
			}},
			Where: And(
				Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(firstStep.LinkingRelation)},
				Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Lit(targetType)},
			),
		},
	}
}

func buildComposedUsersetSubjectsBlock(plan ListPlan, firstStep AnchorPathStep) TypedQueryBlock {
	subjectIDCol := Col{Table: "t", Column: "subject_id"}

	return TypedQueryBlock{
		Comments: []string{"-- Userset: " + firstStep.SubjectType + "#" + firstStep.SubjectRelation + " grants"},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
			FromExpr:    TableAs("melange_tuples", "t"),
			Joins: []JoinClause{{
				Type: "CROSS",
				TableExpr: LateralFunction{
					Name:  ListSubjectsFunctionName(firstStep.SubjectType, firstStep.SubjectRelation),
					Args:  []Expr{Raw("split_part(t.subject_id, '#', 1)"), SubjectType},
					Alias: "s",
				},
			}},
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
				HasUserset{Source: subjectIDCol},
				Eq{Left: UsersetRelation{Source: subjectIDCol}, Right: Lit(firstStep.SubjectRelation)},
			),
		},
	}
}
