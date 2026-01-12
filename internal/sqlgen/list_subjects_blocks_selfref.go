package sqlgen

import "fmt"

// =============================================================================
// Self-Referential Userset Block Builders (List Subjects)
// =============================================================================
// =============================================================================
// Self-Referential Userset List Subjects Block Builders
// =============================================================================

// SelfRefUsersetSubjectsBlockSet contains blocks for a self-referential userset list_subjects function.
// This includes separate blocks for userset filter path and regular path.
type SelfRefUsersetSubjectsBlockSet struct {
	// UsersetFilterBlocks are blocks for the userset filter path (when p_subject_type contains '#')
	UsersetFilterBlocks []TypedQueryBlock

	// UsersetFilterSelfBlock is the self-candidate block for userset filter path
	UsersetFilterSelfBlock *TypedQueryBlock

	// UsersetFilterRecursiveBlock is the recursive block for userset filter expansion
	UsersetFilterRecursiveBlock *TypedQueryBlock

	// RegularBlocks are blocks for the regular path (individual subjects)
	RegularBlocks []TypedQueryBlock

	// UsersetObjectsBaseBlock is the base block for userset objects CTE
	UsersetObjectsBaseBlock *TypedQueryBlock

	// UsersetObjectsRecursiveBlock is the recursive block for userset objects CTE
	UsersetObjectsRecursiveBlock *TypedQueryBlock
}

// BuildListSubjectsSelfRefUsersetBlocks builds blocks for a self-referential userset list_subjects function.
func BuildListSubjectsSelfRefUsersetBlocks(plan ListPlan) (SelfRefUsersetSubjectsBlockSet, error) {
	var result SelfRefUsersetSubjectsBlockSet

	// Build userset filter path blocks
	filterBlocks, filterSelf, filterRecursive, err := buildSelfRefUsersetFilterBlocks(plan)
	if err != nil {
		return SelfRefUsersetSubjectsBlockSet{}, err
	}
	result.UsersetFilterBlocks = filterBlocks
	result.UsersetFilterSelfBlock = filterSelf
	result.UsersetFilterRecursiveBlock = filterRecursive

	// Build regular path blocks
	regularBlocks, usersetBase, usersetRecursive, err := buildSelfRefUsersetRegularBlocks(plan)
	if err != nil {
		return SelfRefUsersetSubjectsBlockSet{}, err
	}
	result.RegularBlocks = regularBlocks
	result.UsersetObjectsBaseBlock = usersetBase
	result.UsersetObjectsRecursiveBlock = usersetRecursive

	return result, nil
}

// buildSelfRefUsersetFilterBlocks builds blocks for the userset filter path.
func buildSelfRefUsersetFilterBlocks(plan ListPlan) (blocks []TypedQueryBlock, selfBlock, computedBlock *TypedQueryBlock, err error) {
	// Build the base block for userset filter - finds userset tuples that match
	var baseBlock TypedQueryBlock
	baseBlock, err = buildSelfRefUsersetFilterBaseBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, baseBlock)

	// Build intersection closure blocks for userset filter path
	var intersectionBlocks []TypedQueryBlock
	intersectionBlocks, err = buildSelfRefUsersetFilterIntersectionBlocks(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build self-candidate block
	selfBlock, err = buildSelfRefUsersetFilterSelfBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build recursive block for userset expansion
	computedBlock, err = buildSelfRefUsersetFilterRecursiveBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	return blocks, selfBlock, computedBlock, nil
}

// buildSelfRefUsersetFilterBaseBlock builds the base block for userset filter path.
func buildSelfRefUsersetFilterBaseBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Build closure EXISTS subquery to check if userset relation satisfies filter relation
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Validate with check_permission
	checkExpr := CheckPermissionExpr(
		"check_permission",
		SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
		plan.Relation,
		ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
		true,
	)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"), Raw("0 AS depth")},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.AllSatisfyingRelations},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("v_filter_type")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Or(
				Eq{Left: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Param("v_filter_relation")},
				ExistsExpr(closureExistsStmt),
			),
			checkExpr,
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Userset filter: find userset tuples that match filter type/relation",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetFilterIntersectionBlocks builds intersection closure blocks for userset filter path.
func buildSelfRefUsersetFilterIntersectionBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range intersectionRels {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		// Call list_subjects for the intersection closure relation with userset filter
		fromClause := fmt.Sprintf("%s(p_object_id, v_filter_type || '#' || v_filter_relation)", funcName)

		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Raw("split_part(icr.subject_id, '#', 1) AS userset_object_id"), Raw("0 AS depth")},
			From:        fromClause,
			Alias:       "icr",
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

// buildSelfRefUsersetFilterSelfBlock builds the self-candidate block for userset filter path.
func buildSelfRefUsersetFilterSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
	// Check if filter type matches object type and relation satisfies
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{
			ObjectID,
			Lit("#"),
			Param("v_filter_relation"),
		}},
		Name: "subject_id",
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectExpr},
		Where: And(
			Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: when filter type matches object type",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetFilterRecursiveBlock builds the recursive block for userset filter expansion.
func buildSelfRefUsersetFilterRecursiveBlock(_ ListPlan) (*TypedQueryBlock, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Raw("v_filter_type")},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Raw("v_filter_relation")},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Raw("v_filter_type")},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Raw("v_filter_relation")},
		Raw("ue.depth < 25"),
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"), Raw("ue.depth + 1 AS depth")},
		FromExpr:    TableAs("userset_expansion", "ue"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "ue", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Recursive userset expansion for filter path",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetRegularBlocks builds blocks for the regular path (individual subjects).
func buildSelfRefUsersetRegularBlocks(plan ListPlan) (blocks []TypedQueryBlock, selfBlock, computedBlock *TypedQueryBlock, err error) {
	// Configure exclusions for regular path
	exclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "t", Column: "subject_id"},
	)

	// Build direct tuple lookup block
	directBlock, err := buildSelfRefUsersetRegularDirectBlock(plan, exclusions)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, directBlock)

	// Build complex closure blocks
	complexBlocks, err := buildSelfRefUsersetRegularComplexClosureBlocks(plan, exclusions)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Build intersection closure blocks
	intersectionBlocks, err := buildSelfRefUsersetRegularIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build userset expansion block - subjects from recursively expanded userset objects
	usersetExpansionBlock, err := buildSelfRefUsersetRegularExpansionBlock(plan, exclusions)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, usersetExpansionBlock)

	// Build userset pattern blocks (non-self-referential)
	usersetPatternBlocks, err := buildSelfRefUsersetRegularPatternBlocks(plan, exclusions)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, usersetPatternBlocks...)

	// Build userset objects base block
	usersetBase, err := buildSelfRefUsersetObjectsBaseBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build userset objects recursive block
	usersetRecursive, err := buildSelfRefUsersetObjectsRecursiveBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	return blocks, usersetBase, usersetRecursive, nil
}

// buildSelfRefUsersetRegularDirectBlock builds the direct tuple lookup block for regular path.
func buildSelfRefUsersetRegularDirectBlock(plan ListPlan, exclusions ExclusionConfig) (TypedQueryBlock, error) {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		WhereObjectID(ObjectID).
		WhereSubjectType(SubjectType).
		Where(In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}).
		SelectCol("subject_id").
		Distinct()

	if excludeWildcard {
		q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}

	// Add exclusion predicates
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path 1: Direct tuple lookup on the object itself",
		},
		Query: q.Build(),
	}, nil
}

// buildSelfRefUsersetRegularComplexClosureBlocks builds complex closure blocks for regular path.
func buildSelfRefUsersetRegularComplexClosureBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
	if len(plan.ComplexClosure) == 0 {
		return nil, nil
	}

	excludeWildcard := plan.ExcludeWildcard()
	var blocks []TypedQueryBlock

	for _, rel := range plan.ComplexClosure {
		checkExpr := CheckPermissionInternalExpr(
			SubjectRef{Type: SubjectType, ID: Col{Table: "t", Column: "subject_id"}},
			rel,
			ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
			true,
		)

		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			WhereObjectID(ObjectID).
			WhereSubjectType(SubjectType).
			Where(
				In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
				checkExpr,
			).
			SelectCol("subject_id").
			Distinct()

		if excludeWildcard {
			q = q.Where(Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
		}

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

// buildSelfRefUsersetRegularIntersectionClosureBlocks builds intersection closure blocks for regular path.
func buildSelfRefUsersetRegularIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range intersectionRels {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		fromClause := fmt.Sprintf("%s(p_object_id, p_subject_type)", funcName)

		stmt := SelectStmt{
			Columns: []string{"icr.subject_id"},
			From:    fromClause,
			Alias:   "icr",
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

// buildSelfRefUsersetRegularExpansionBlock builds the userset expansion block for regular path.
func buildSelfRefUsersetRegularExpansionBlock(plan ListPlan, exclusions ExclusionConfig) (TypedQueryBlock, error) {
	excludeWildcard := plan.ExcludeWildcard()
	exclusionPreds := exclusions.BuildPredicates()

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	if excludeWildcard {
		conditions = append(conditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("userset_objects", "uo"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path 2: Expand userset subjects from all reachable userset objects",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetRegularPatternBlocks builds userset pattern blocks for regular path.
func buildSelfRefUsersetRegularPatternBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	excludeWildcard := plan.ExcludeWildcard()
	var blocks []TypedQueryBlock

	for _, pattern := range patterns {
		// Skip self-referential patterns - handled by userset_objects CTE
		if pattern.IsSelfReferential {
			continue
		}

		if pattern.IsComplex {
			block, err := buildSelfRefUsersetRegularComplexPatternBlock(plan, pattern, exclusions, excludeWildcard)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildSelfRefUsersetRegularSimplePatternBlock(plan, pattern, exclusions, excludeWildcard)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildSelfRefUsersetRegularComplexPatternBlock builds a complex userset pattern block for regular path.
func buildSelfRefUsersetRegularComplexPatternBlock(plan ListPlan, pattern listUsersetPatternInput, _ ExclusionConfig, _ bool) (TypedQueryBlock, error) {
	// Use LATERAL list function for complex patterns
	funcName := listSubjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation)

	// Find grant tuples, then LATERAL join to list function
	grantConditions := []Expr{
		Eq{Left: Col{Table: "g", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "g", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "g", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "g", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "g", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "g", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}

	// Build LATERAL subquery for subjects from userset object
	lateralSubquery := fmt.Sprintf("LATERAL %s(split_part(g.subject_id, '#', 1), p_subject_type, NULL, NULL) AS s", funcName)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "g"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: lateralSubquery,
		}},
		Where: And(grantConditions...),
	}

	// Add exclusion predicates on subject
	subjectExclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "s", Column: "subject_id"},
	)
	for _, pred := range subjectExclusions.BuildPredicates() {
		stmt.Where = And(stmt.Where, pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
			"-- Complex userset: use LATERAL list function",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetRegularSimplePatternBlock builds a simple userset pattern block for regular path.
func buildSelfRefUsersetRegularSimplePatternBlock(plan ListPlan, pattern listUsersetPatternInput, _ ExclusionConfig, excludeWildcard bool) (TypedQueryBlock, error) {
	// For simple patterns, use a JOIN with membership tuples
	subjectExclusions := buildExclusionInput(
		plan.Analysis,
		ObjectID,
		SubjectType,
		Col{Table: "s", Column: "subject_id"},
	)

	membershipConditions := []Expr{
		Eq{Left: Col{Table: "s", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: "s", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "g", Column: "subject_id"}}},
		In{Expr: Col{Table: "s", Column: "relation"}, Values: pattern.SatisfyingRelations},
		Eq{Left: Col{Table: "s", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	if excludeWildcard {
		membershipConditions = append(membershipConditions, Ne{Left: Col{Table: "s", Column: "subject_id"}, Right: Lit("*")})
	}

	grantConditions := []Expr{
		Eq{Left: Col{Table: "g", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "g", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "g", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "g", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "g", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "g", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}

	// For closure patterns, add check_permission validation
	if pattern.IsClosurePattern {
		checkExpr := CheckPermissionExpr(
			"check_permission",
			SubjectRef{Type: SubjectType, ID: Col{Table: "s", Column: "subject_id"}},
			pattern.SourceRelation,
			ObjectRef{Type: Lit(plan.ObjectType), ID: ObjectID},
			true,
		)
		grantConditions = append(grantConditions, checkExpr)
	}

	// Add exclusion predicates
	grantConditions = append(grantConditions, subjectExclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "g"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "s",
			On:    And(membershipConditions...),
		}},
		Where: And(grantConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Non-self userset expansion: %s#%s", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetObjectsBaseBlock builds the base block for userset_objects CTE.
func buildSelfRefUsersetObjectsBaseBlock(plan ListPlan) (*TypedQueryBlock, error) {
	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Select("split_part(t.subject_id, '#', 1) AS userset_object_id", "0 AS depth").
		WhereObjectID(ObjectID).
		Where(Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)}).
		WhereHasUserset().
		WhereUsersetRelation(plan.Relation).
		Distinct()

	return &TypedQueryBlock{
		Comments: []string{
			"-- Base case: find initial userset references",
		},
		Query: q.Build(),
	}, nil
}

// buildSelfRefUsersetObjectsRecursiveBlock builds the recursive block for userset_objects CTE.
func buildSelfRefUsersetObjectsRecursiveBlock(plan ListPlan) (*TypedQueryBlock, error) {
	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(plan.Relation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(plan.Relation)},
		Raw("uo.depth < 25"),
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Raw("split_part(t.subject_id, '#', 1) AS userset_object_id"), Raw("uo.depth + 1 AS depth")},
		FromExpr:    TableAs("userset_objects", "uo"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "uo", Column: "userset_object_id"}},
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Recursive case: expand self-referential userset references",
		},
		Query: stmt,
	}, nil
}
