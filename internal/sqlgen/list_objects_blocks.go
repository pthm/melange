package sqlgen

import "fmt"

// BuildListObjectsBlocks builds all query blocks for a list_objects function.
// Returns a BlockSet with Primary blocks for the main query path.
func BuildListObjectsBlocks(plan ListPlan) (BlockSet, error) {
	var blocks BlockSet

	if plan.HasStandaloneAccess {
		standaloneBlocks, err := buildStandaloneAccessBlocks(plan)
		if err != nil {
			return BlockSet{}, err
		}
		blocks.Primary = append(blocks.Primary, standaloneBlocks...)
	}

	intersectionGroupBlocks, err := buildListObjectsIntersectionGroupBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = append(blocks.Primary, intersectionGroupBlocks...)

	if selfBlock := buildListObjectsSelfCandidateBlock(plan); selfBlock != nil {
		blocks.Primary = append(blocks.Primary, *selfBlock)
	}

	return blocks, nil
}

// buildStandaloneAccessBlocks builds blocks for access paths not constrained by intersection.
// For patterns like `viewer: [user] and writer`, the direct access ([user]) is inside
// the intersection, so standalone blocks are skipped.
func buildStandaloneAccessBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	directBlock, err := buildListObjectsDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	if plan.HasUsersetSubject {
		usersetBlock, err := buildListObjectsUsersetSubjectBlock(plan)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, usersetBlock)
	}

	complexBlocks, err := buildTypedListObjectsComplexClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	intersectionClosureBlocks, err := buildListObjectsIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionClosureBlocks...)

	if plan.HasUsersetPatterns {
		usersetPatternBlocks, err := buildListObjectsUsersetPatternBlocks(plan)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, usersetPatternBlocks...)
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
			"-- Userset subject matching: subject IS a userset (e.g., group:fga#member)",
			"-- matches tuples where subject_id has equivalent or satisfying relation via closure",
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
			ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
			FromExpr: FunctionCallExpr{
				Name:  funcName,
				Args:  []Expr{SubjectType, SubjectID, Null{}, Null{}},
				Alias: "icr",
			},
		}

		// Apply exclusion predicates to the composed results
		// The composed relation returns candidates, but they must also satisfy
		// the current relation's exclusions (e.g., can_read: reader but not nblocked)
		if plan.HasExclusion {
			exclusionConfig := buildExclusionInput(
				plan.Analysis,
				Col{Table: "icr", Column: "object_id"},
				SubjectType,
				SubjectID,
			)
			predicates := exclusionConfig.BuildPredicates()
			if len(predicates) > 0 {
				stmt.Where = And(predicates...)
			}
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

// buildListObjectsIntersectionGroupBlocks builds blocks for intersection groups.
// Each intersection group represents an AND of parts that must all be satisfied.
// Multiple groups are OR'd together (UNION).
// Within each group, parts are AND'd together (INTERSECT).
func buildListObjectsIntersectionGroupBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	if len(plan.Analysis.IntersectionGroups) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for idx, group := range plan.Analysis.IntersectionGroups {
		block, err := buildIntersectionGroupBlock(plan, idx, group)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}

	return blocks, nil
}

// buildIntersectionGroupBlock builds a single intersection group block.
// The group is an INTERSECT of all parts, wrapped in a subquery.
func buildIntersectionGroupBlock(plan ListPlan, idx int, group IntersectionGroupInfo) (TypedQueryBlock, error) {
	// Build query for each part
	partQueries := make([]SelectStmt, 0, len(group.Parts))
	for _, part := range group.Parts {
		partQuery := buildIntersectionPartQuery(plan, part)
		partQueries = append(partQueries, partQuery)
	}

	// Build INTERSECT of all parts as a subquery
	// Result: SELECT ig.object_id FROM (part1 INTERSECT part2 INTERSECT ...) AS ig
	intersectQuery := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "ig", Column: "object_id"}},
		FromExpr: IntersectSubquery{
			Alias:   "ig",
			Queries: partQueries,
		},
	}

	// Apply exclusion predicates to the intersection result
	// Exclusions are configured at the relation level and applied after the INTERSECT
	// We need to rebuild the exclusion config with the correct object_id reference (ig.object_id)
	if plan.HasExclusion {
		exclusionConfig := buildExclusionInput(
			plan.Analysis,
			Col{Table: "ig", Column: "object_id"}, // Use ig.object_id for intersection result
			SubjectType,
			SubjectID,
		)
		predicates := exclusionConfig.BuildPredicates()
		if len(predicates) > 0 {
			intersectQuery.Where = And(predicates...)
		}
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Intersection group %d: all parts must be satisfied", idx),
		},
		Query: intersectQuery,
	}, nil
}

// buildIntersectionPartQuery builds a query for a single intersection part.
func buildIntersectionPartQuery(plan ListPlan, part IntersectionPart) SelectStmt {
	var q *TupleQuery
	alias := "t"

	switch {
	case part.IsThis:
		q = Tuples(alias).
			ObjectType(plan.ObjectType).
			Relations(plan.Relation).
			SelectCol("object_id").
			Where(
				Eq{Left: Col{Table: alias, Column: "subject_type"}, Right: SubjectType},
				In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
				SubjectIDMatch(Col{Table: alias, Column: "subject_id"}, SubjectID, part.HasWildcard),
			).
			Distinct()

	case part.ParentRelation != nil:
		alias = "child"
		q = Tuples(alias).
			ObjectType(plan.ObjectType).
			Relations(part.ParentRelation.LinkingRelation).
			SelectCol("object_id").
			Where(CheckPermission{
				Subject:  SubjectParams(),
				Relation: part.ParentRelation.Relation,
				Object: ObjectRef{
					Type: Col{Table: alias, Column: "subject_type"},
					ID:   Col{Table: alias, Column: "subject_id"},
				},
				ExpectAllow: true,
			}).
			Distinct()

	default:
		q = Tuples(alias).
			ObjectType(plan.ObjectType).
			SelectCol("object_id").
			Where(CheckPermission{
				Subject:     SubjectParams(),
				Relation:    part.Relation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: alias, Column: "object_id"}),
				ExpectAllow: true,
			}).
			Distinct()
	}

	if part.ExcludedRelation != "" {
		q.Where(CheckPermission{
			Subject:     SubjectParams(),
			Relation:    part.ExcludedRelation,
			Object:      LiteralObject(plan.ObjectType, Col{Table: alias, Column: "object_id"}),
			ExpectAllow: false,
		})
	}

	return q.Build()
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
			"-- Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " (complex userset, validated by check_permission)",
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
			"-- Via " + pattern.SubjectType + "#" + pattern.SubjectRelation + " (simple userset, JOIN with membership tuples)",
		},
		Query: q.Build(),
	}, nil
}

// buildListObjectsSelfCandidateBlock builds the self-candidate block for when
// the subject is a userset on the same object type (e.g., document:1#writer).
func buildListObjectsSelfCandidateBlock(plan ListPlan) *TypedQueryBlock {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: SubstringUsersetRelation{Source: SubjectID}},
		),
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{UsersetObjectID{Source: SubjectID}},
		Alias:       "object_id",
		Where: And(
			HasUserset{Source: SubjectID},
			Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-candidate: subject is userset on same object type"},
		Query:    stmt,
	}
}
