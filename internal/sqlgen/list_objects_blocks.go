package sqlgen

import "fmt"

// =============================================================================
// List Objects Block Builders
// =============================================================================

// BuildListObjectsBlocks builds all query blocks for a list_objects function.
// Returns a BlockSet with Primary blocks for the main query path.
func BuildListObjectsBlocks(plan ListPlan) (BlockSet, error) {
	var blocks BlockSet

	// Standalone access blocks are only added when there are access paths
	// not constrained by intersection. For patterns like `viewer: [user] and writer`,
	// the direct access ([user]) is inside the intersection, so we skip standalone blocks.
	if plan.HasStandaloneAccess {
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

		// Build intersection closure blocks (for composing with other relations)
		intersectionClosureBlocks, err := buildListObjectsIntersectionClosureBlocks(plan)
		if err != nil {
			return BlockSet{}, err
		}
		blocks.Primary = append(blocks.Primary, intersectionClosureBlocks...)

		// Build userset pattern blocks if needed
		if plan.HasUsersetPatterns {
			usersetPatternBlocks, err := buildListObjectsUsersetPatternBlocks(plan)
			if err != nil {
				return BlockSet{}, err
			}
			blocks.Primary = append(blocks.Primary, usersetPatternBlocks...)
		}
	}

	// Intersection group blocks are always added when present
	// These represent AND groups that must all be satisfied
	intersectionGroupBlocks, err := buildListObjectsIntersectionGroupBlocks(plan)
	if err != nil {
		return BlockSet{}, err
	}
	blocks.Primary = append(blocks.Primary, intersectionGroupBlocks...)

	// Self-candidate block is always added
	// This handles the case where the subject is a userset on the same object type
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
	switch {
	case part.IsThis:
		// Direct tuple lookup on the same relation
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			Relations(plan.Relation).
			SelectCol("object_id").
			Where(
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
				In{Expr: SubjectType, Values: plan.AllowedSubjectTypes}, // Type guard for allowed subject types
				SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, part.HasWildcard),
			).
			Distinct()

		// Add exclusion check if part has nested exclusion
		if part.ExcludedRelation != "" {
			q.Where(CheckPermission{
				Subject:     SubjectParams(),
				Relation:    part.ExcludedRelation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: false,
			})
		}

		return q.Build()

	case part.ParentRelation != nil:
		// TTU pattern: lookup via parent with check_permission validation
		q := Tuples("child").
			ObjectType(plan.ObjectType).
			Relations(part.ParentRelation.LinkingRelation).
			SelectCol("object_id").
			Where(CheckPermission{
				Subject:  SubjectParams(),
				Relation: part.ParentRelation.Relation,
				Object: ObjectRef{
					Type: Col{Table: "child", Column: "subject_type"},
					ID:   Col{Table: "child", Column: "subject_id"},
				},
				ExpectAllow: true,
			}).
			Distinct()

		// Add exclusion check if part has nested exclusion
		if part.ExcludedRelation != "" {
			q.Where(CheckPermission{
				Subject:     SubjectParams(),
				Relation:    part.ExcludedRelation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "child", Column: "object_id"}),
				ExpectAllow: false,
			})
		}

		return q.Build()

	default:
		// Computed userset: use check_permission to validate
		q := Tuples("t").
			ObjectType(plan.ObjectType).
			SelectCol("object_id").
			Where(CheckPermission{
				Subject:     SubjectParams(),
				Relation:    part.Relation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: true,
			}).
			Distinct()

		// Add exclusion check if part has nested exclusion
		if part.ExcludedRelation != "" {
			q.Where(CheckPermission{
				Subject:     SubjectParams(),
				Relation:    part.ExcludedRelation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: false,
			})
		}

		return q.Build()
	}
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
// This block handles the case where the subject is a userset on the same object type.
// For example: querying list_objects(document, viewer, document:1#writer) should return
// document:1 if writer satisfies viewer.
func buildListObjectsSelfCandidateBlock(plan ListPlan) (*TypedQueryBlock, error) {
	// Build closure check: does the userset relation in the subject satisfy the queried relation?
	// Check: c.object_type = plan.ObjectType AND c.relation = plan.Relation AND
	//        c.satisfying_relation = substring(p_subject_id from position('#') + 1)
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
		Comments: []string{
			"-- Self-candidate: when subject is a userset on the same object type",
			"-- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'",
			"-- The object 'document:1' should be considered as a candidate",
			"-- No type guard here - validity comes from the closure check below",
			"-- No exclusion checks for self-candidate - this is a structural validity check",
		},
		Query: stmt,
	}, nil
}
