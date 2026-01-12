package sqlgen

import "fmt"

// =============================================================================
// Intersection List Subjects Block Builders
// =============================================================================

// SubjectsIntersectionBlockSet contains blocks for an intersection list_subjects function.
// Unlike recursive which uses check_permission_internal within queries, intersection
// gathers candidates then filters with check_permission at the end.
type SubjectsIntersectionBlockSet struct {
	// RegularCandidateBlocks are candidate blocks for regular (non-userset-filter) path
	RegularCandidateBlocks []TypedQueryBlock

	// UsersetFilterCandidateBlocks are candidate blocks for userset filter path
	UsersetFilterCandidateBlocks []TypedQueryBlock

	// UsersetFilterSelfBlock is the self-referential userset block
	UsersetFilterSelfBlock *TypedQueryBlock
}

// BuildListSubjectsIntersectionBlocks builds blocks for an intersection list_subjects function.
func BuildListSubjectsIntersectionBlocks(plan ListPlan) (SubjectsIntersectionBlockSet, error) {
	var result SubjectsIntersectionBlockSet

	// Build regular candidate blocks
	regularBlocks, err := buildListSubjectsIntersectionRegularCandidates(plan)
	if err != nil {
		return SubjectsIntersectionBlockSet{}, err
	}
	result.RegularCandidateBlocks = regularBlocks

	// Build userset filter candidate blocks
	usersetBlocks, err := buildListSubjectsIntersectionUsersetCandidates(plan)
	if err != nil {
		return SubjectsIntersectionBlockSet{}, err
	}
	result.UsersetFilterCandidateBlocks = usersetBlocks

	// Build userset filter self block
	selfBlock, err := buildListSubjectsUsersetFilterSelfBlock(plan)
	if err != nil {
		return SubjectsIntersectionBlockSet{}, err
	}
	result.UsersetFilterSelfBlock = selfBlock

	return result, nil
}

// buildListSubjectsIntersectionRegularCandidates builds candidate blocks for the regular path.
func buildListSubjectsIntersectionRegularCandidates(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock
	excludeWildcard := plan.ExcludeWildcard()

	// Base query - direct tuple lookup
	baseBlock := buildListSubjectsIntersectionBaseBlock(plan, excludeWildcard)
	blocks = append(blocks, baseBlock)

	// Intersection part candidates
	partBlocks, err := buildListSubjectsIntersectionPartBlocks(plan, excludeWildcard)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, partBlocks...)

	// Userset pattern candidates
	patternBlocks, err := buildListSubjectsIntersectionUsersetPatternBlocks(plan, excludeWildcard)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, patternBlocks...)

	// TTU candidates
	ttuBlocks, err := buildListSubjectsIntersectionTTUBlocks(plan, excludeWildcard)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, ttuBlocks...)

	// Subject pool block - all subjects of the requested type
	poolBlock := buildListSubjectsIntersectionPoolBlock(plan, excludeWildcard)
	blocks = append(blocks, poolBlock)

	return blocks, nil
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

// buildListSubjectsIntersectionPartBlocks builds blocks for intersection parts.
func buildListSubjectsIntersectionPartBlocks(plan ListPlan, excludeWildcard bool) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			block, err := buildListSubjectsIntersectionPartBlock(plan, part, excludeWildcard)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildListSubjectsIntersectionPartBlock builds a block for a single intersection part.
func buildListSubjectsIntersectionPartBlock(plan ListPlan, part IntersectionPart, excludeWildcard bool) (TypedQueryBlock, error) {
	if part.ParentRelation != nil {
		// TTU part - join through parent
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
		}
		if excludeWildcard {
			conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
		}

		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "pt", Column: "subject_id"}},
			FromExpr:    TableAs("melange_tuples", "link"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: And(
					Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
					Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
				),
			}},
			Where: And(conditions...),
		}

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    stmt,
		}, nil
	}

	// Direct part
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
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
		Comments: []string{fmt.Sprintf("-- Intersection part: %s", part.Relation)},
		Query:    stmt,
	}, nil
}

// buildListSubjectsIntersectionUsersetPatternBlocks builds userset pattern blocks for intersection.
func buildListSubjectsIntersectionUsersetPatternBlocks(plan ListPlan, excludeWildcard bool) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		block, err := buildListSubjectsIntersectionUsersetPatternBlock(plan, pattern, excludeWildcard)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildListSubjectsIntersectionUsersetPatternBlock builds a single userset pattern block.
func buildListSubjectsIntersectionUsersetPatternBlock(plan ListPlan, pattern listUsersetPatternInput, excludeWildcard bool) (TypedQueryBlock, error) {
	grantAlias := "g"
	memberAlias := "s"

	// Build the JOIN condition
	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		In{Expr: Col{Table: memberAlias, Column: "relation"}, Values: pattern.SatisfyingRelations},
	)

	// Build the WHERE conditions
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

	stmt := SelectStmt{
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
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset pattern: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsIntersectionTTUBlocks builds TTU blocks for intersection.
func buildListSubjectsIntersectionTTUBlocks(plan ListPlan, excludeWildcard bool) ([]TypedQueryBlock, error) {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, parent := range parentRelations {
		block, err := buildListSubjectsIntersectionTTUBlock(plan, parent, excludeWildcard)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildListSubjectsIntersectionTTUBlock builds a single TTU block for intersection.
func buildListSubjectsIntersectionTTUBlock(plan ListPlan, parent ListParentRelationData, excludeWildcard bool) (TypedQueryBlock, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: SubjectType},
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}
	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "pt", Column: "subject_id"}, Right: Lit("*")})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "pt", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
			),
		}},
		Where: And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- TTU: subjects via %s -> %s", parent.LinkingRelation, parent.Relation),
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsIntersectionPoolBlock builds the subject pool block.
func buildListSubjectsIntersectionPoolBlock(plan ListPlan, excludeWildcard bool) TypedQueryBlock {
	conditions := []Expr{
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
		Comments: []string{"-- Subject pool: all subjects of requested type"},
		Query:    stmt,
	}
}

// buildListSubjectsIntersectionUsersetCandidates builds userset filter candidate blocks.
func buildListSubjectsIntersectionUsersetCandidates(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Base userset filter block
	baseBlock, err := buildListSubjectsIntersectionUsersetFilterBaseBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, baseBlock)

	// Intersection part blocks for userset filter
	partBlocks, err := buildListSubjectsIntersectionUsersetFilterPartBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, partBlocks...)

	// TTU blocks for userset filter
	ttuBlocks, err := buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, ttuBlocks...)

	return blocks, nil
}

// buildListSubjectsIntersectionUsersetFilterBaseBlock builds the base userset filter block.
func buildListSubjectsIntersectionUsersetFilterBaseBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Build relation match expression
	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "t.subject_id")

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
		Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
		relationMatch,
	}

	// Build subject_id expression with normalization
	subjectExpr := Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- Userset filter: direct userset tuples"},
		Query:    stmt,
	}, nil
}

// buildUsersetFilterRelationMatchExpr builds the relation match expression for userset filter.
func buildUsersetFilterRelationMatchExpr(closureRows []ValuesRow, subjectIDExpr string) Expr {
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(closureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}
	return Or(
		Eq{Left: Raw("substring(" + subjectIDExpr + " from position('#' in " + subjectIDExpr + ") + 1)"), Right: Param("v_filter_relation")},
		Exists{Query: closureExistsStmt},
	)
}

// buildListSubjectsIntersectionUsersetFilterPartBlocks builds intersection part blocks for userset filter.
func buildListSubjectsIntersectionUsersetFilterPartBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	for _, group := range plan.Analysis.IntersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				continue
			}
			block, err := buildListSubjectsIntersectionUsersetFilterPartBlock(plan, part)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildListSubjectsIntersectionUsersetFilterPartBlock builds a single intersection part block for userset filter.
func buildListSubjectsIntersectionUsersetFilterPartBlock(plan ListPlan, part IntersectionPart) (TypedQueryBlock, error) {
	if part.ParentRelation != nil {
		// TTU part
		relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "pt.subject_id")
		conditions := []Expr{
			Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(part.ParentRelation.LinkingRelation)},
			Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
			Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
			relationMatch,
		}

		subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{subjectExpr},
			FromExpr:    TableAs("melange_tuples", "link"),
			Joins: []JoinClause{{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "pt",
				On: And(
					Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
					Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
				),
			}},
			Where: And(conditions...),
		}

		return TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Userset filter intersection part: via %s", part.ParentRelation.LinkingRelation)},
			Query:    stmt,
		}, nil
	}

	// Direct part
	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "t.subject_id")
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(part.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
		Gt{Left: Raw("position('#' in t.subject_id)"), Right: Int(0)},
		relationMatch,
	}

	subjectExpr := Raw("substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset filter intersection part: %s", part.Relation)},
		Query:    stmt,
	}, nil
}

// buildListSubjectsIntersectionUsersetFilterTTUBlocks builds TTU blocks for userset filter path.
func buildListSubjectsIntersectionUsersetFilterTTUBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	parentRelations := buildListParentRelations(plan.Analysis)
	if len(parentRelations) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, parent := range parentRelations {
		block, err := buildListSubjectsIntersectionUsersetFilterTTUBlock(plan, parent)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildListSubjectsIntersectionUsersetFilterTTUBlock builds a single TTU block for userset filter.
func buildListSubjectsIntersectionUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) (TypedQueryBlock, error) {
	relationMatch := buildUsersetFilterRelationMatchExpr(plan.Inline.ClosureRows, "pt.subject_id")
	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
		Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
		relationMatch,
	}
	if parent.AllowedLinkingTypes != "" {
		conditions = append(conditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
			),
		}},
		Where: And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset filter TTU: via %s -> %s", parent.LinkingRelation, parent.Relation),
		},
		Query: stmt,
	}, nil
}
