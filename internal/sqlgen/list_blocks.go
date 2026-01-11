package sqlgen

import (
	"fmt"
	"strings"
)

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
	var partQueries []SelectStmt
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
func buildListSubjectsUsersetFilterBlocks(plan ListPlan) ([]TypedQueryBlock, *TypedQueryBlock, error) {
	var blocks []TypedQueryBlock
	var selfBlock *TypedQueryBlock

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

// =============================================================================
// Recursive List Objects Block Builders (TTU patterns)
// =============================================================================

// RecursiveBlockSet contains blocks for a recursive list function.
// These blocks are wrapped in a CTE with depth tracking.
type RecursiveBlockSet struct {
	// BaseBlocks are the base case blocks (depth=0) in the CTE
	BaseBlocks []TypedQueryBlock

	// RecursiveBlock is the recursive term block (references the CTE)
	RecursiveBlock *TypedQueryBlock

	// SelfCandidateBlock is added outside the CTE
	SelfCandidateBlock *TypedQueryBlock

	// SelfRefLinkingRelations are the linking relations for self-referential TTU
	// Used for depth check before query execution
	SelfRefLinkingRelations []string
}

// HasRecursive returns true if there is a recursive block.
func (r RecursiveBlockSet) HasRecursive() bool {
	return r.RecursiveBlock != nil
}

// BuildListObjectsRecursiveBlocks builds blocks for a recursive list_objects function.
// This handles TTU patterns with depth tracking and recursive CTEs.
func BuildListObjectsRecursiveBlocks(plan ListPlan) (RecursiveBlockSet, error) {
	var result RecursiveBlockSet

	// Compute parent relations from analysis
	parentRelations := buildListParentRelations(plan.Analysis)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	result.SelfRefLinkingRelations = dequoteLinkingRelations(selfRefSQL)

	// Build base blocks
	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations)
	if err != nil {
		return RecursiveBlockSet{}, err
	}
	result.BaseBlocks = baseBlocks

	// Build recursive block if there are self-referential TTU patterns
	if len(result.SelfRefLinkingRelations) > 0 {
		recursiveBlock, err := buildRecursiveTTUBlock(plan, result.SelfRefLinkingRelations)
		if err != nil {
			return RecursiveBlockSet{}, err
		}
		result.RecursiveBlock = recursiveBlock
	}

	// Build self-candidate block
	selfBlock, err := buildListObjectsSelfCandidateBlock(plan)
	if err != nil {
		return RecursiveBlockSet{}, err
	}
	result.SelfCandidateBlock = selfBlock

	return result, nil
}

// buildRecursiveBaseBlocks builds the base case blocks for the recursive CTE.
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Base exclusions for tuple lookup blocks
	baseExclusions := plan.Exclusions

	// Direct tuple lookup block
	directBlock, err := buildRecursiveDirectBlock(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Complex closure blocks
	complexBlocks, err := buildRecursiveComplexClosureBlocks(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildRecursiveIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Userset pattern blocks
	usersetBlocks, err := buildRecursiveUsersetPatternBlocks(plan, baseExclusions)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	// Cross-type TTU blocks (non-recursive, uses check_permission_internal)
	crossTTUBlocks, err := buildCrossTypeTTUBlocks(plan, parentRelations)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, crossTTUBlocks...)

	return blocks, nil
}

// buildRecursiveDirectBlock builds the direct tuple lookup block for recursive CTEs.
func buildRecursiveDirectBlock(plan ListPlan, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		Query: q.Build(),
	}, nil
}

// buildRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildRecursiveComplexClosureBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
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

// buildRecursiveIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildRecursiveIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
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
				fmt.Sprintf("-- Compose with intersection closure relation: %s", rel),
			},
			Query: stmt,
		})
	}

	return blocks, nil
}

// buildRecursiveUsersetPatternBlocks builds blocks for userset patterns.
func buildRecursiveUsersetPatternBlocks(plan ListPlan, exclusions ExclusionConfig) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		if pattern.IsComplex {
			block, err := buildRecursiveComplexUsersetBlock(plan, pattern, exclusions)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildRecursiveSimpleUsersetBlock(plan, pattern, exclusions)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildRecursiveComplexUsersetBlock builds a block for complex userset patterns.
func buildRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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

	// Add exclusion predicates
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Complex userset: use check_permission_internal for membership",
		},
		Query: q.Build(),
	}, nil
}

// buildRecursiveSimpleUsersetBlock builds a block for simple userset patterns.
func buildRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, exclusions ExclusionConfig) (TypedQueryBlock, error) {
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

	// Add exclusion predicates
	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: q.Build(),
	}, nil
}

// buildCrossTypeTTUBlocks builds blocks for cross-type TTU patterns.
// These are non-recursive and use check_permission_internal.
func buildCrossTypeTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	for _, parent := range parentRelations {
		if !parent.HasCrossTypeLinks {
			continue
		}

		crossTypes := dequoteLinkingRelations(parent.CrossTypeLinkingTypes)
		crossExclusions := buildExclusionInput(
			plan.Analysis,
			Col{Table: "child", Column: "object_id"},
			SubjectType,
			SubjectID,
		)

		q := Tuples("child").
			ObjectType(plan.ObjectType).
			Relations(parent.LinkingRelation).
			Where(
				In{Expr: Col{Table: "child", Column: "subject_type"}, Values: crossTypes},
				CheckPermission{
					Subject:  SubjectParams(),
					Relation: parent.Relation,
					Object: ObjectRef{
						Type: Col{Table: "child", Column: "subject_type"},
						ID:   Col{Table: "child", Column: "subject_id"},
					},
					ExpectAllow: true,
				},
			).
			SelectCol("object_id").
			Distinct()

		// Add exclusion predicates
		for _, pred := range crossExclusions.BuildPredicates() {
			q.Where(pred)
		}

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{
				fmt.Sprintf("-- Cross-type TTU: %s -> %s on non-self types", parent.LinkingRelation, parent.Relation),
				"-- Find objects whose linking relation points to a parent where subject has relation",
				"-- This is non-recursive (uses check_permission_internal, not CTE reference)",
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildRecursiveTTUBlock builds the recursive term block for self-referential TTU.
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string) (*TypedQueryBlock, error) {
	recursiveExclusions := buildExclusionInput(
		plan.Analysis,
		Col{Table: "child", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	// Build the recursive query that joins with the CTE
	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth"},
		From:     "accessible",
		Alias:    "a",
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					In{Expr: Col{Table: "child", Column: "relation"}, Values: linkingRelations},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
	}

	// Add exclusion predicates
	predicates := recursiveExclusions.BuildPredicates()
	if len(predicates) > 0 {
		allPredicates := append([]Expr{stmt.Where}, predicates...)
		stmt.Where = And(allPredicates...)
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-referential TTU: follow linking relations to accessible parents",
			"-- Combined all self-referential TTU patterns into single recursive term",
		},
		Query: stmt,
	}, nil
}

// dequoteLinkingRelations extracts relation names from a SQL-formatted list.
// e.g., "'parent', 'container'" -> ["parent", "container"]
func dequoteLinkingRelations(sqlList string) []string {
	if sqlList == "" {
		return nil
	}
	parts := strings.Split(sqlList, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(strings.Trim(part, "'"))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
