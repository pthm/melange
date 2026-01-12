package sqlgen

import "fmt"

// =============================================================================
// Recursive List Subjects Block Builders (TTU patterns)
// =============================================================================

// SubjectsRecursiveBlockSet contains blocks for a recursive list_subjects function.
// Unlike list_objects recursive which uses WITH RECURSIVE depth tracking, list_subjects
// recursive uses subject_pool CTE + base_results CTE + check_permission_internal calls.
type SubjectsRecursiveBlockSet struct {
	// RegularBlocks are the base result blocks for regular (non-userset-filter) path
	RegularBlocks []TypedQueryBlock

	// RegularTTUBlocks are the TTU path blocks for regular path (cross join with check_permission_internal)
	RegularTTUBlocks []TypedQueryBlock

	// UsersetFilterBlocks are the blocks for userset filter path (when p_subject_type contains '#')
	UsersetFilterBlocks []TypedQueryBlock

	// UsersetFilterSelfBlock is the self-referential userset block for userset filter path
	UsersetFilterSelfBlock *TypedQueryBlock

	// ParentRelations contains the TTU parent relations for rendering
	ParentRelations []ListParentRelationData
}

// BuildListSubjectsRecursiveBlocks builds blocks for a recursive list_subjects function.
// This handles TTU patterns with subject_pool CTE and check_permission_internal calls.
func BuildListSubjectsRecursiveBlocks(plan ListPlan) (SubjectsRecursiveBlockSet, error) {
	var result SubjectsRecursiveBlockSet

	// Compute parent relations from analysis
	result.ParentRelations = buildListParentRelations(plan.Analysis)

	// Build regular path blocks
	regularBlocks, err := buildListSubjectsRecursiveRegularBlocks(plan)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}
	result.RegularBlocks = regularBlocks

	// Build regular TTU path blocks
	ttuBlocks, err := buildListSubjectsRecursiveTTUBlocks(plan, result.ParentRelations)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}
	result.RegularTTUBlocks = ttuBlocks

	// Build userset filter path blocks
	usersetBlocks, err := buildSubjectsRecursiveUsersetFilterTypedBlocks(plan, result.ParentRelations)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}
	result.UsersetFilterBlocks = usersetBlocks

	// Build userset filter self block
	selfBlock, err := buildListSubjectsUsersetFilterSelfBlock(plan)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}
	result.UsersetFilterSelfBlock = selfBlock

	return result, nil
}

// buildListSubjectsRecursiveRegularBlocks builds the regular path blocks for recursive list_subjects.
// These are UNION'd together and wrapped in base_results CTE.
func buildListSubjectsRecursiveRegularBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Build direct tuple lookup block
	directBlock, err := buildListSubjectsRecursiveDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Build complex closure blocks
	complexBlocks, err := buildListSubjectsRecursiveComplexClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Build intersection closure blocks
	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(plan, "p_subject_type", "p_subject_type", plan.HasExclusion)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build userset pattern blocks
	usersetBlocks, err := buildListSubjectsRecursiveUsersetPatternBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	return blocks, nil
}

// buildListSubjectsRecursiveDirectBlock builds the direct tuple lookup block for recursive list_subjects.
func buildListSubjectsRecursiveDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
		).
		SelectCol("subject_id").
		Distinct()

	// Exclude wildcards if needed
	if excludeWildcard {
		q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

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

// buildListSubjectsRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildListSubjectsRecursiveComplexClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	if len(plan.ComplexClosure) == 0 {
		return nil, nil
	}

	excludeWildcard := plan.ExcludeWildcard()
	var blocks []TypedQueryBlock

	for _, rel := range plan.ComplexClosure {
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
				NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				CheckPermission{
					Subject: SubjectRef{
						Type: SubjectType,
						ID:   Col{Table: "t", Column: "subject_id"},
					},
					Relation:    rel,
					Object:      LiteralObject(plan.ObjectType, ObjectID),
					ExpectAllow: true,
				},
			).
			SelectCol("subject_id").
			Distinct()

		// Exclude wildcards if needed
		if excludeWildcard {
			q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
		}

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

// buildListSubjectsRecursiveUsersetPatternBlocks builds blocks for userset patterns in recursive list_subjects.
func buildListSubjectsRecursiveUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	excludeWildcard := plan.ExcludeWildcard()
	var blocks []TypedQueryBlock

	for _, pattern := range patterns {
		if pattern.IsComplex {
			block, err := buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern, excludeWildcard)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildListSubjectsRecursiveSimpleUsersetBlock(plan, pattern, excludeWildcard)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildListSubjectsRecursiveComplexUsersetBlock builds a complex userset block for recursive list_subjects.
func buildListSubjectsRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, excludeWildcard bool) (TypedQueryBlock, error) {
	// For complex usersets, we need to validate membership via check_permission_internal
	grantAlias := "g"
	memberAlias := "m"

	// Exclusions for the member subject
	memberExclusions := buildExclusionInput(plan.Analysis, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

	// Build check_permission_internal call for membership validation
	checkExpr := CheckPermissionInternalExpr(
		SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
		pattern.SubjectRelation,
		ObjectRef{Type: Lit(pattern.SubjectType), ID: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		true,
	)

	// Build the JOIN condition (note: check_permission_internal is in WHERE, not JOIN)
	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
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
		checkExpr,
	}

	// Add wildcard exclusion if needed
	if excludeWildcard {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}

	// Add exclusion predicates
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	// Add closure pattern validation if needed
	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
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
			fmt.Sprintf("-- Userset path: Via %s#%s (complex - uses check_permission_internal)", pattern.SubjectType, pattern.SubjectRelation),
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRecursiveSimpleUsersetBlock builds a simple userset block for recursive list_subjects.
func buildListSubjectsRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, excludeWildcard bool) (TypedQueryBlock, error) {
	grantAlias := "g"
	memberAlias := "s"

	// Exclusions for the member subject
	memberExclusions := buildExclusionInput(plan.Analysis, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

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

	// Add wildcard exclusion if needed
	if excludeWildcard {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}

	// Add exclusion predicates
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	// Add closure pattern validation if needed
	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
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
			fmt.Sprintf("-- Userset path: Via %s#%s (simple - direct join)", pattern.SubjectType, pattern.SubjectRelation),
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRecursiveTTUBlocks builds the TTU path blocks for recursive list_subjects.
// These use CROSS JOIN with subject_pool and check_permission_internal calls.
func buildListSubjectsRecursiveTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	if len(parentRelations) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, parent := range parentRelations {
		block, err := buildListSubjectsRecursiveTTUBlock(plan, parent)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildListSubjectsRecursiveTTUBlock builds a single TTU path block for recursive list_subjects.
func buildListSubjectsRecursiveTTUBlock(plan ListPlan, parent ListParentRelationData) (TypedQueryBlock, error) {
	// Build exclusions for the subject
	exclusions := buildExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sp", Column: "subject_id"})

	// Build check_permission_internal call
	checkExpr := CheckPermissionInternalExpr(
		SubjectRef{Type: SubjectType, ID: Col{Table: "sp", Column: "subject_id"}},
		parent.Relation,
		ObjectRef{Type: Col{Table: "link", Column: "subject_type"}, ID: Col{Table: "link", Column: "subject_id"}},
		true,
	)

	// Build WHERE conditions
	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		checkExpr,
	}

	// Add allowed linking types filter if present
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	// Add exclusion predicates
	whereConditions = append(whereConditions, exclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "sp", Column: "subject_id"}},
		FromExpr:    TableAs("subject_pool", "sp"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "melange_tuples",
			Alias: "link",
			On:    Bool(true), // CROSS JOIN
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- TTU path: subjects via %s -> %s", parent.LinkingRelation, parent.Relation),
		},
		Query: stmt,
	}, nil
}

// buildSubjectsRecursiveUsersetFilterTypedBlocks builds the userset filter path blocks.
func buildSubjectsRecursiveUsersetFilterTypedBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Build direct userset tuples block with check_permission validation
	directBlock, err := buildListSubjectsRecursiveUsersetFilterDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Build TTU path blocks for userset filter
	for _, parent := range parentRelations {
		// Main TTU query - find userset subjects via parent relation
		ttuBlock, err := buildListSubjectsRecursiveUsersetFilterTTUBlock(plan, parent)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, ttuBlock)

		// Intermediate TTU query - return parent object itself as userset reference
		intermediateBlock, err := buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan, parent)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, intermediateBlock)

		// Nested TTU query - recursively resolve multi-hop chains
		nestedBlock, err := buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan, parent)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, nestedBlock)
	}

	// Build intersection closure blocks for userset filter
	filterUsersetExpr := Concat{Parts: []Expr{Param("v_filter_type"), Lit("#"), Param("v_filter_relation")}}
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
			FromExpr: FunctionCallExpr{
				Name:  funcName,
				Args:  []Expr{ObjectID, filterUsersetExpr},
				Alias: "ics",
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

// buildListSubjectsRecursiveUsersetFilterDirectBlock builds the direct userset tuples block.
func buildListSubjectsRecursiveUsersetFilterDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Build check_permission call for validation
	checkExpr := CheckPermission{
		Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
		Relation:    plan.Relation,
		Object:      LiteralObject(plan.ObjectType, Param("p_object_id")),
		ExpectAllow: true,
	}

	// Build closure EXISTS for relation normalization
	// This checks if the userset relation in subject_id satisfies v_filter_relation on v_filter_type
	// e.g., for subject_id='fga#member' and filter 'group#member':
	// - userset_relation = 'member' (extracted from subject_id)
	// - v_filter_type = 'group'
	// - v_filter_relation = 'member'
	// Check: does 'member' satisfy 'member' on 'group'? (yes via closure)
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)")},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Build subject_id expression with normalization
	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{
			UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			Lit("#"),
			Param("v_filter_relation"),
		}},
		Name: "subject_id",
	}

	// Build final select statement
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
			Raw(closureStmt.Exists()),
			checkExpr,
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Direct userset tuples on this object",
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRecursiveUsersetFilterTTUBlock builds the TTU path block for userset filter.
func buildListSubjectsRecursiveUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) (TypedQueryBlock, error) {
	// Build closure subquery for relation resolution
	closureRelStmt := SelectStmt{
		Columns:  []string{"c.satisfying_relation"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
		),
	}

	// Build closure EXISTS for subject relation verification
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Build WHERE conditions
	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "pt", Column: "subject_type"}, Right: Param("v_filter_type")},
		Gt{Left: Raw("position('#' in pt.subject_id)"), Right: Int(0)},
		Or(
			Eq{Left: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)"), Right: Param("v_filter_relation")},
			Exists{Query: closureExistsStmt},
		),
	}

	// Add allowed linking types filter if present
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	// Build subject_id expression with normalization
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
				Raw("pt.relation IN ("+closureRelStmt.SQL()+")"),
			),
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- TTU path: userset subjects via %s -> %s", parent.LinkingRelation, parent.Relation),
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock builds the intermediate TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan ListPlan, parent ListParentRelationData) (TypedQueryBlock, error) {
	// Build closure EXISTS for relation verification
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Build WHERE conditions
	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Param("v_filter_type")},
		Exists{Query: closureExistsStmt},
	}

	// Add allowed linking types filter if present
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	// Build subject_id expression
	subjectExpr := Raw("link.subject_id || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("melange_tuples", "link"),
		Where:       And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- TTU intermediate object: return the parent object itself as a userset reference",
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRecursiveUsersetFilterTTUNestedBlock builds the nested TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan ListPlan, parent ListParentRelationData) (TypedQueryBlock, error) {
	// Build LATERAL call to list_accessible_subjects
	lateralCall := LateralFunction{
		Name: "list_accessible_subjects",
		Args: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Lit(parent.Relation),
			SubjectType,
		},
		Alias: "nested",
	}

	// Build WHERE conditions
	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}

	// Add allowed linking types filter if present
	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "nested", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:      "CROSS",
			TableExpr: lateralCall,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- TTU nested intermediate objects: recursively resolve multi-hop TTU chains",
		},
		Query: stmt,
	}, nil
}
