package sqlgen

// =============================================================================
// List Blocks Layer
// =============================================================================
//
// This file implements the Blocks layer for list function generation.
// The Blocks layer builds QueryBlock/SelectStmt values using DSL only.
// No SQL strings are produced here - rendering is done in the Render layer.
//
// Architecture: Plan → Blocks → Render
// - Plan: compute flags and normalized inputs
// - Blocks: build typed query structures using DSL (this file)
// - Render: produce SQL/PLpgSQL strings

// TypedQueryBlock represents a query with optional comments using typed DSL.
// Unlike QueryBlock which uses SQL string, this uses SelectStmt for DSL-first building.
type TypedQueryBlock struct {
	Comments []string   // Comment lines (without -- prefix)
	Query    SelectStmt // The query as typed DSL
}

// BlockSet contains the query blocks for a list function.
// This separates primary and secondary query paths for rendering flexibility.
type BlockSet struct {
	// Primary contains the main query blocks (UNION'd together)
	Primary []TypedQueryBlock

	// Secondary contains optional secondary path blocks (e.g., userset filter path)
	Secondary []TypedQueryBlock

	// SecondarySelf is an optional self-candidate block for userset filter
	SecondarySelf *TypedQueryBlock
}

// HasSecondary returns true if there are secondary blocks.
func (b BlockSet) HasSecondary() bool {
	return len(b.Secondary) > 0 || b.SecondarySelf != nil
}

// AllSecondary returns all secondary blocks including the self block.
func (b BlockSet) AllSecondary() []TypedQueryBlock {
	if b.SecondarySelf == nil {
		return b.Secondary
	}
	return append(b.Secondary, *b.SecondarySelf)
}

// =============================================================================
// List Objects Block Builders
// =============================================================================

// BuildListObjectsBlocks builds all query blocks for a list_objects function.
// Returns a BlockSet with Primary blocks for the main query path.
func BuildListObjectsBlocks(plan ListPlan) (BlockSet, error) {
	var blocks BlockSet

	// Build direct tuple lookup block
	directBlock, err := buildListObjectsDirectBlock(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = append(blocks.Primary, directBlock)

	// Build userset subject block if needed
	if plan.HasUsersetSubject {
		usersetBlock, err := buildListObjectsUsersetSubjectBlock(plan)
		if err != nil {
			return BlockSet{}, err
		}
		blocks.Primary = append(blocks.Primary, usersetBlock)
	}

	// Build complex closure blocks
	complexBlocks, err := buildTypedListObjectsComplexClosureBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = append(blocks.Primary, complexBlocks...)

	// Build intersection closure blocks
	intersectionBlocks, err := buildListObjectsIntersectionClosureBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = append(blocks.Primary, intersectionBlocks...)

	// Build userset pattern blocks if needed
	if plan.HasUsersetPatterns {
		usersetPatternBlocks, err := buildListObjectsUsersetPatternBlocks(plan)
		if err != nil {
			return BlockSet{}, err
		}
		blocks.Primary = append(blocks.Primary, usersetPatternBlocks...)
	}

	// Build self-candidate block
	selfBlock, err := buildListObjectsSelfCandidateBlock(plan)
	if err != nil {
		return BlockSet{}, err
	}
	if selfBlock != nil {
		blocks.Primary = append(blocks.Primary, *selfBlock)
	}

	return blocks, nil
}

// buildListObjectsDirectBlock builds the direct tuple lookup query block.
func buildListObjectsDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
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
	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Direct tuple lookup with simple closure relations",
			"-- Type guard: only return results if subject type is in allowed subject types",
		},
		Query: q.Build(),
	}, nil
}

// buildListObjectsUsersetSubjectBlock builds the userset subject matching block.
func buildListObjectsUsersetSubjectBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Build the closure EXISTS subquery
	closureExistsStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: SubjectType},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	// Subject match: either exact match or userset object ID match with closure exists
	subjectMatch := Or(
		Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
		And(
			Eq{
				Left:  UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
				Right: UsersetObjectID{Source: SubjectID},
			},
			Raw(closureExistsStmt.Exists()),
		),
	)

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			HasUserset{Source: SubjectID},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			subjectMatch,
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Direct userset subject matching: when the subject IS a userset (e.g., group:fga#member)",
			"-- and there's a tuple with that userset (or a satisfying relation) as the subject",
			"-- This handles cases like: tuple(document:1, viewer, group:fga#member_c4) queried by group:fga#member",
			"-- where member satisfies member_c4 via the closure (member → member_c1 → ... → member_c4)",
		},
		Query: q.Build(),
	}, nil
}

// buildTypedListObjectsComplexClosureBlocks builds blocks for complex closure relations.
func buildTypedListObjectsComplexClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
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
		for _, pred := range plan.Exclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
				"-- These relations have exclusions or other complex features that require full permission check",
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildListObjectsIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildListObjectsIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	if len(plan.Analysis.IntersectionClosureRelations) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listObjectsFunctionName(plan.ObjectType, rel)
		stmt := SelectStmt{
			Columns: []string{"icr.object_id"},
			From:    funcName + "(p_subject_type, p_subject_id, NULL, NULL)",
			Alias:   "icr",
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				"-- Compose with intersection closure relation: " + rel,
			},
			Query: stmt,
		})
	}

	return blocks, nil
}

// buildListObjectsUsersetPatternBlocks builds blocks for userset patterns.
func buildListObjectsUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsComplex {
			// Complex userset: use check_permission_internal for membership verification
			block, err := buildListObjectsComplexUsersetBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			// Simple userset: JOIN with membership tuples
			block, err := buildListObjectsSimpleUsersetBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildListObjectsComplexUsersetBlock builds a block for complex userset patterns.
func buildListObjectsComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
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

	return TypedQueryBlock{
		Comments: []string{
			"-- Path: Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " membership",
			"-- Complex userset: use check_permission_internal for membership verification",
			"-- Note: No type guard needed here because check_permission_internal handles all validation",
			"-- including userset self-referential checks (e.g., group:1#member checking member on group:1)",
		},
		Query: q.Build(),
	}, nil
}

// buildListObjectsSimpleUsersetBlock builds a block for simple userset patterns.
func buildListObjectsSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
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

	return TypedQueryBlock{
		Comments: []string{
			"-- Path: Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " membership",
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: q.Build(),
	}, nil
}

// buildListObjectsSelfCandidateBlock builds the self-candidate block.
func buildListObjectsSelfCandidateBlock(plan ListPlan) (*TypedQueryBlock, error) {
	// Only build if the object type could appear in the result
	if !plan.HasUsersetSubject {
		return nil, nil
	}

	// Build closure check for self-referential userset
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: SubjectType},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
			In{Expr: Col{Table: "c", Column: "satisfying_relation"}, Values: plan.AllSatisfyingRelations},
		),
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{UsersetObjectID{Source: SubjectID}},
		Where: And(
			Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
			HasUserset{Source: SubjectID},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: when subject is a userset on the same object type",
			"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
			"-- The object 'document:1' should be considered as a candidate",
			"-- No type guard here - validity comes from the closure check below",
		},
		Query: stmt,
	}, nil
}

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
func buildListSubjectsUsersetFilterBlocks(plan ListPlan) ([]TypedQueryBlock, *TypedQueryBlock, error) {
	// Only build if the relation supports userset subjects
	if !plan.HasUsersetSubject {
		return nil, nil, nil
	}

	var blocks []TypedQueryBlock
	var selfBlock *TypedQueryBlock

	// Build direct userset filter block
	directBlock, err := buildListSubjectsUsersetFilterDirectBlock(plan)
	if err != nil {
		return nil, nil, err
	}
	blocks = append(blocks, directBlock)

	// Build self-referential userset filter block
	selfBlock, err = buildListSubjectsUsersetFilterSelfBlock(plan)
	if err != nil {
		return nil, nil, err
	}

	return blocks, selfBlock, nil
}

// buildListSubjectsUsersetFilterDirectBlock builds the direct userset filter block.
func buildListSubjectsUsersetFilterDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
	// Query with closure join to find satisfying relations
	closureJoin := JoinClause{
		Type:      "INNER",
		TableExpr: ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		On: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Col{Table: "t", Column: "relation"}},
		),
	}

	stmt := SelectStmt{
		Distinct: true,
		ColumnExprs: []Expr{
			// Return normalized userset reference: subject_type || ':' || subject_id || '#' || p_filter_relation
			Concat{Parts: []Expr{
				Col{Table: "t", Column: "subject_type"},
				Lit(":"),
				Col{Table: "t", Column: "subject_id"},
				Lit("#"),
				Param("p_filter_relation"),
			}},
		},
		FromExpr: TableAs("melange_tuples", "t"),
		Joins:    []JoinClause{closureJoin},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Param("p_filter_type")},
			In{Expr: Param("p_filter_type"), Values: plan.AllowedSubjectTypes},
			NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
		),
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Direct tuple lookup with simple closure relations",
			"-- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)",
			"-- Type guard: only return results if filter type is in allowed subject types",
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
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("p_filter_relation")},
		),
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{
			// Return object_id || '#' || p_filter_relation
			Concat{Parts: []Expr{
				ObjectID,
				Lit("#"),
				Param("p_filter_relation"),
			}},
		},
		Where: And(
			Eq{Left: Param("p_filter_type"), Right: Lit(plan.ObjectType)},
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

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				"-- Complex closure relations: find candidates via tuples, validate via check_permission_internal",
			},
			Query: q.Build(),
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

	return TypedQueryBlock{
		Comments: []string{
			"-- Path: Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " - expand group membership to return individual subjects",
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: q.Build(),
	}, nil
}
