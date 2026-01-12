package sqlgen

import "fmt"

// =============================================================================
// List Subjects Block Builders
// =============================================================================

// BuildListSubjectsBlocks builds all query blocks for a list_subjects function.
// Returns a BlockSet with Primary and optionally Secondary blocks.
func BuildListSubjectsBlocks(plan ListPlan) (BlockSet, error) {
	var blocks BlockSet

	// Build userset filter blocks (Secondary path)
	usersetFilterBlocks, usersetFilterSelf, err := buildListSubjectsUsersetFilterBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Secondary = usersetFilterBlocks
	blocks.SecondarySelf = usersetFilterSelf

	// Build regular blocks (Primary path)
	regularBlocks, err := buildListSubjectsRegularBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = regularBlocks

	return blocks, nil
}

// buildListSubjectsUsersetFilterBlocks builds the userset filter path blocks.
// The userset filter path is ALWAYS built because any subject can be a userset
// reference (e.g., document:1#writer), even when there are no [group#member]
// patterns in the model.
func buildListSubjectsUsersetFilterBlocks(plan ListPlan) (blocks []TypedQueryBlock, selfBlock *TypedQueryBlock, err error) {
	// Build direct userset filter block
	directBlock, err := buildListSubjectsUsersetFilterDirectBlock(plan)
	if err != nil {
		return nil, nil, err
	}
	blocks = append(blocks, directBlock)

	// Build intersection closure blocks for userset filter path
	// The subject type for userset filter is: v_filter_type || '#' || v_filter_relation
	// Validate with check_permission when exclusion is present to apply exclusion rules
	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(plan, "v_filter_type || '#' || v_filter_relation", "v_filter_type", plan.HasExclusion)
	if err != nil {
		return nil, nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build self-referential userset filter block
	selfBlock, err = buildListSubjectsUsersetFilterSelfBlock(plan)
	if err != nil {
		return nil, nil, err
	}

	return blocks, selfBlock, nil
}

// buildListSubjectsUsersetFilterDirectBlock builds the userset filter block for list_subjects.
// This handles queries like "list_subjects for folder:1.viewer with filter group#member".
// It finds tuples where the subject is a userset (e.g., group:fga#member_c4) and checks
// if the userset relation satisfies the filter relation via closure.
func buildListSubjectsUsersetFilterDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Build closure EXISTS subquery to check if userset relation satisfies filter relation
	// e.g., check if member_c4 satisfies member via closure (group, member_c4, member)
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: SubstringUsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Normalized subject expression: split_part(subject_id, '#', 1) || '#' || filter_relation
	// e.g., 'fga#member_c4' becomes 'fga#member'
	subjectExpr := Alias{
		Expr: NormalizedUsersetSubject(Col{Table: "t", Column: "subject_id"}, Param("v_filter_relation")),
		Name: "subject_id",
	}

	// Build the query:
	// - Find tuples on the object where subject_type matches filter_type
	// - Subject must have userset marker (position('#') > 0)
	// - Either userset relation matches filter relation exactly, or closure says it satisfies
	// - Validate with check_permission call
	//
	// Use AllSatisfyingRelations (not RelationList) because:
	// 1. We're validating with check_permission anyway, which handles complex relations
	// 2. We need to find tuples for all satisfying relations, not just simple ones
	// 3. Example: for can_view: viewer, we need to find tuples with relation='viewer'
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
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
			// Validate that the userset subject actually has permission
			CheckPermissionCall{
				FunctionName: "check_permission",
				Subject:      SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
				Relation:     plan.Relation,
				Object:       LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow:  true,
			},
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Userset filter: find userset tuples that match and return normalized references",
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsUsersetFilterSelfBlock builds the self-referential userset filter block.
func buildListSubjectsUsersetFilterSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
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

	// Column must be aliased as subject_id for pagination wrapper
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
			"-- e.g., querying document:1.viewer with filter document#writer",
			"-- should return document:1#writer if writer satisfies the relation",
			"-- No type guard here - validity comes from the closure check below",
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsRegularBlocks builds the regular (non-userset-filter) path blocks.
func buildListSubjectsRegularBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Build direct tuple lookup block
	directBlock, err := buildListSubjectsDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Build complex closure blocks
	complexBlocks, err := buildTypedListSubjectsComplexClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Build intersection closure blocks
	// These compose with relations that have intersection patterns (e.g., can_read implied from reader)
	// Validate with check_permission when exclusion is present to apply exclusion rules
	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(plan, "p_subject_type", "p_subject_type", plan.HasExclusion)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build userset pattern blocks if needed
	if plan.HasUsersetPatterns {
		usersetBlocks, err := buildListSubjectsUsersetPatternBlocks(plan)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, usersetBlocks...)
	}

	return blocks, nil
}

// buildListSubjectsDirectBlock builds the direct tuple lookup block for list_subjects.
func buildListSubjectsDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("p_subject_type")},
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

// buildTypedListSubjectsComplexClosureBlocks builds blocks for complex closure relations.
func buildTypedListSubjectsComplexClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
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
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("p_subject_type")},
				NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				CheckPermission{
					Subject: SubjectRef{
						Type: Param("p_subject_type"),
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
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildListSubjectsIntersectionClosureBlocks builds blocks for intersection closure relations.
// For implied relations like can_read (implied from reader which has intersection), this
// calls the list_subjects function for the intersection relation to get candidates.
//
// Parameters:
// - subjectTypeExpr: expression for function call (e.g., "p_subject_type" or "v_filter_type || '#' || v_filter_relation")
// - checkSubjectTypeExpr: expression for validation (e.g., "p_subject_type" or "v_filter_type"), empty to skip validation
// - validate: if true, wrap results with check_permission validation for exclusion
func buildListSubjectsIntersectionClosureBlocks(plan ListPlan, subjectTypeExpr, checkSubjectTypeExpr string, validate bool) ([]TypedQueryBlock, error) {
	if len(plan.Analysis.IntersectionClosureRelations) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		// Call list_subjects for the intersection closure relation
		// e.g., list_folder_reader_subjects(p_object_id, p_subject_type)
		fromClause := fmt.Sprintf("%s(p_object_id, %s)", funcName, subjectTypeExpr)

		var stmt SelectStmt
		if validate && checkSubjectTypeExpr != "" {
			// Validated path: wrap with check_permission to apply exclusion
			stmt = SelectStmt{
				Distinct: true,
				Columns:  []string{"ics.subject_id"},
				From:     fromClause,
				Alias:    "ics",
				Where: CheckPermissionCall{
					FunctionName: "check_permission",
					Subject:      SubjectRef{Type: Raw(checkSubjectTypeExpr), ID: Col{Table: "ics", Column: "subject_id"}},
					Relation:     plan.Relation,
					Object:       LiteralObject(plan.ObjectType, ObjectID),
					ExpectAllow:  true,
				},
			}
		} else {
			// Non-validated path: just return the results
			stmt = SelectStmt{
				Columns: []string{"ics.subject_id"},
				From:    fromClause,
				Alias:   "ics",
			}
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

// buildListSubjectsUsersetPatternBlocks builds blocks for userset patterns in list_subjects.
func buildListSubjectsUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsComplex {
			// Complex userset: use LATERAL join with userset's list_subjects function
			block, err := buildListSubjectsComplexUsersetBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			// Simple userset: JOIN with membership tuples
			block, err := buildListSubjectsSimpleUsersetBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildListSubjectsComplexUsersetBlock builds a block for complex userset patterns in list_subjects.
func buildListSubjectsComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	// Get the function name for the userset's list_subjects
	funcName := listSubjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation)

	// Build LATERAL join query
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "ls", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type: "CROSS JOIN LATERAL",
				// Call the userset's list_subjects function
				TableExpr: FunctionCallExpr{
					Name: funcName,
					Args: []Expr{
						UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
						Param("p_subject_type"),
						Null{},
						Null{},
					},
					Alias: "ls",
				},
			},
		},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Table: "t", Column: "relation"}, Values: pattern.SourceRelations},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
				Right: Lit(pattern.SubjectRelation),
			},
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path: Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " - expand group membership to return individual subjects",
			"-- Complex userset: use LATERAL join with userset's list_subjects function",
			"-- This handles userset-to-userset chains where there are no direct subject tuples",
		},
		Query: stmt,
	}, nil
}

// buildListSubjectsSimpleUsersetBlock builds a block for simple userset patterns in list_subjects.
func buildListSubjectsSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	excludeWildcard := plan.ExcludeWildcard()

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
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
			Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Param("p_subject_type")},
			NoUserset{Source: Col{Table: "m", Column: "subject_id"}},
		).
		Select("m.subject_id").
		Distinct()

	// Exclude wildcards if needed
	if excludeWildcard {
		q.Where(Ne{Left: Col{Table: "m", Column: "subject_id"}, Right: Lit("*")})
	}

	// Add check_permission validation for closure patterns
	// This is needed for exclusion to work correctly - validates that the subject
	// actually has permission via the closure relation
	if pattern.IsClosurePattern && pattern.SourceRelation != "" {
		q.Where(CheckPermission{
			Subject: SubjectRef{
				Type: Param("p_subject_type"),
				ID:   Col{Table: "m", Column: "subject_id"},
			},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path: Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " - expand group membership to return individual subjects",
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: q.Build(),
	}, nil
}
