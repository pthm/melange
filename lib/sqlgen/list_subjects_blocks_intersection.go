package sqlgen

import "fmt"

// SubjectsIntersectionBlockSet contains blocks for an intersection list_subjects function.
// Unlike recursive which uses check_permission_internal within queries, intersection
// gathers candidates then filters with check_permission at the end.
type SubjectsIntersectionBlockSet struct {
	RegularCandidateBlocks       []TypedQueryBlock
	UsersetFilterCandidateBlocks []TypedQueryBlock
	UsersetFilterSelfBlock       *TypedQueryBlock
}

// buildDirectSubjectSelectStmt creates a SELECT DISTINCT subject_id FROM melange_tuples t.
func buildDirectSubjectSelectStmt(conditions []Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}
}

// buildTTUSubjectSelectStmt creates a TTU join query selecting subject_id from pt.
func buildTTUSubjectSelectStmt(conditions []Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "pt", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins:       []JoinClause{ttuJoin()},
		Where:       And(conditions...),
	}
}

// ttuJoin returns the standard TTU join clause.
func ttuJoin() JoinClause {
	return JoinClause{
		Type:  "INNER",
		Table: "melange_tuples",
		Alias: "pt",
		On: And(
			Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
		),
	}
}

// buildUsersetFilterTTUSelectStmt creates a userset filter TTU query.
func buildUsersetFilterTTUSelectStmt(objectType, linkingRelation string, subjectExpr, relationMatch Expr) SelectStmt {
	return SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins:       []JoinClause{ttuJoin()},
		Where: And(
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(objectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(linkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
			Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
			relationMatch,
		),
	}
}

func BuildListSubjectsIntersectionBlocks(plan ListPlan) SubjectsIntersectionBlockSet {
	return SubjectsIntersectionBlockSet{
		RegularCandidateBlocks:       buildListSubjectsIntersectionRegularCandidates(plan),
		UsersetFilterCandidateBlocks: buildListSubjectsIntersectionUsersetCandidates(plan),
		UsersetFilterSelfBlock:       buildListSubjectsUsersetFilterSelfBlock(plan),
	}
}

func buildListSubjectsIntersectionRegularCandidates(plan ListPlan) []TypedQueryBlock {
	excludeWildcard := plan.ExcludeWildcard()

	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsIntersectionBaseBlock(plan, excludeWildcard))
	blocks = append(blocks, buildListSubjectsIntersectionPartBlocks(plan, excludeWildcard)...)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetPatternBlocks(plan, excludeWildcard)...)
	blocks = append(blocks, buildListSubjectsIntersectionTTUBlocks(plan, excludeWildcard)...)
	blocks = append(blocks, buildListSubjectsIntersectionPoolBlock(plan, excludeWildcard))
	return blocks
}

// buildListSubjectsIntersectionBaseBlock builds the base tuple lookup block.
func buildListSubjectsIntersectionBaseBlock(plan ListPlan, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- Base: direct tuple lookup"},
		Query:    stmt,
	}
}

func buildListSubjectsIntersectionPartBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	var blocks []TypedQueryBlock
	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			blocks = append(blocks, buildListSubjectsIntersectionPartBlock(plan, part, excludeWildcard))
		}
	}
	return blocks
}

func buildListSubjectsIntersectionPartBlock(plan ListPlan, part IntersectionPart, excludeWildcard bool) TypedQueryBlock {
	if part.ParentRelation != nil {
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
		}
		if excludeWildcard {
			conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
		}

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    buildTTUSubjectSelectStmt(conditions),
		}
	}

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Intersection part: %s", part.Relation)},
		Query:    buildDirectSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionUsersetPatternBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		blocks = append(blocks, buildListSubjectsIntersectionUsersetPatternBlock(plan, pattern, excludeWildcard))
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetPatternBlock(plan ListPlan, pattern listUsersetPatternInput, excludeWildcard bool) TypedQueryBlock {
	const grantAlias = "g"
	const memberAlias = "s"

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
	if excludeWildcard {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset pattern: %s#%s", pattern.SubjectType, pattern.SubjectRelation)},
		Query: SelectStmt{
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
		},
	}
}

func buildListSubjectsIntersectionTTUBlocks(plan ListPlan, excludeWildcard bool) []TypedQueryBlock {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsIntersectionTTUBlock(plan, parent, excludeWildcard))
	}
	return blocks
}

func buildListSubjectsIntersectionTTUBlock(plan ListPlan, parent ListParentRelationData, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
	}
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		conditions = append(conditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU: subjects via %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    buildTTUSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionPoolBlock(plan ListPlan, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	return TypedQueryBlock{
		Comments: []string{"-- Subject pool: all subjects of requested type"},
		Query:    buildDirectSubjectSelectStmt(conditions),
	}
}

func buildListSubjectsIntersectionUsersetCandidates(plan ListPlan) []TypedQueryBlock {
	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterBaseBlock(plan))
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterPartBlocks(plan)...)
	blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan)...)
	return blocks
}

func buildListSubjectsIntersectionUsersetFilterBaseBlock(plan ListPlan) TypedQueryBlock {
	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "t.subject_id")
	subjectExpr := Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	return TypedQueryBlock{
		Comments: []string{"-- Userset filter: direct userset tuples"},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{subjectExpr},
			FromExpr:    TableAs("melange_tuples", "t"),
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
				Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
				relationMatch,
			),
		},
	}
}

func buildUsersetFilterRelationMatchExpr(closureRows []ValuesRow, subjectIDExpr string) Expr {
	relationExtract := "substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)"
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(closureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw(relationExtract)},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}
	return Or(
		Eq{Left: Raw(relationExtract), Right: Param("v_filter_relation")},
		Exists{Query: closureExistsStmt},
	)
}

func buildListSubjectsIntersectionUsersetFilterPartBlocks(plan ListPlan) []TypedQueryBlock {
	var blocks []TypedQueryBlock
	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterPartBlock(plan, part))
		}
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetFilterPartBlock(plan ListPlan, part IntersectionPart) TypedQueryBlock {
	if part.ParentRelation != nil {
		relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "pt.subject_id")
		subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Userset filter intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    buildUsersetFilterTTUSelectStmt(plan.ObjectType, part.ParentRelation.LinkingRelation, subjectExpr, relationMatch),
		}
	}

	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "t.subject_id")
	subjectExpr := Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset filter intersection part: %s", part.Relation)},
		Query: SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{subjectExpr},
			FromExpr:    TableAs("melange_tuples", "t"),
			Where: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
				Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
				relationMatch,
			),
		},
	}
}

func buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan ListPlan) []TypedQueryBlock {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsIntersectionUsersetFilterTTUBlock(plan, parent))
	}
	return blocks
}

func buildListSubjectsIntersectionUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "pt.subject_id")
	subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := buildUsersetFilterTTUSelectStmt(plan.ObjectType, parent.LinkingRelation, subjectExpr, relationMatch)
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		// Add type restriction to existing WHERE clause
		stmt.Where = And(stmt.Where, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset filter TTU: via %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}
