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
	if parent.AllowedLinkingTypes != "" {
		whereConditions = append(whereConditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
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
			From:        fmt.Sprintf("%s(p_object_id, %s)", funcName, filterUsersetExpr.SQL()),
			Alias:       "ics",
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
	if parent.AllowedLinkingTypes != "" {
		whereConditions = append(whereConditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
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
	if parent.AllowedLinkingTypes != "" {
		whereConditions = append(whereConditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
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
	lateralCall := fmt.Sprintf("LATERAL list_accessible_subjects(link.subject_type, link.subject_id, '%s', p_subject_type)", parent.Relation)

	// Build WHERE conditions
	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}

	// Add allowed linking types filter if present
	if parent.AllowedLinkingTypes != "" {
		whereConditions = append(whereConditions, Raw(fmt.Sprintf("link.subject_type IN (%s)", parent.AllowedLinkingTypes)))
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "nested", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: lateralCall,
			Alias: "nested",
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

// =============================================================================
// Self-Referential Userset Block Builders
// =============================================================================
//
// These builders handle patterns like [group#member] on group.member,
// which require recursive CTE expansion to find all objects reachable
// through self-referential userset chains.

// SelfRefUsersetBlockSet contains blocks for a self-referential userset list function.
// These blocks are wrapped in a recursive CTE for member expansion.
type SelfRefUsersetBlockSet struct {
	// BaseBlocks are the base case blocks (depth=0) in the CTE
	BaseBlocks []TypedQueryBlock

	// RecursiveBlock is the recursive term block that expands self-referential usersets
	RecursiveBlock *TypedQueryBlock

	// SelfCandidateBlock is added outside the CTE (UNION with CTE result)
	SelfCandidateBlock *TypedQueryBlock
}

// HasRecursive returns true if there is a recursive block.
func (s SelfRefUsersetBlockSet) HasRecursive() bool {
	return s.RecursiveBlock != nil
}

// BuildListObjectsSelfRefUsersetBlocks builds blocks for a self-referential userset list_objects function.
func BuildListObjectsSelfRefUsersetBlocks(plan ListPlan) (SelfRefUsersetBlockSet, error) {
	var result SelfRefUsersetBlockSet

	// Build base blocks
	baseBlocks, err := buildSelfRefUsersetBaseBlocks(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.BaseBlocks = baseBlocks

	// Build recursive block for self-referential userset expansion
	recursiveBlock, err := buildSelfRefUsersetRecursiveBlock(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.RecursiveBlock = recursiveBlock

	// Build self-candidate block
	selfBlock, err := buildListObjectsSelfCandidateBlock(plan)
	if err != nil {
		return SelfRefUsersetBlockSet{}, err
	}
	result.SelfCandidateBlock = selfBlock

	return result, nil
}

// buildSelfRefUsersetBaseBlocks builds the base case blocks for self-referential userset CTE.
func buildSelfRefUsersetBaseBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Direct tuple lookup block
	directBlock, err := buildSelfRefUsersetDirectBlock(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, directBlock)

	// Complex closure blocks
	complexBlocks, err := buildSelfRefUsersetComplexClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, complexBlocks...)

	// Intersection closure blocks
	intersectionBlocks, err := buildSelfRefUsersetIntersectionClosureBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Userset pattern blocks (excluding self-referential ones)
	usersetBlocks, err := buildSelfRefUsersetPatternBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	return blocks, nil
}

// buildSelfRefUsersetDirectBlock builds the direct tuple lookup block for self-ref userset CTE.
func buildSelfRefUsersetDirectBlock(plan ListPlan) (TypedQueryBlock, error) {
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
			"-- Path 1: Direct tuple lookup with simple closure relations",
		},
		Query: q.Build(),
	}, nil
}

// buildSelfRefUsersetComplexClosureBlocks builds blocks for complex closure relations.
func buildSelfRefUsersetComplexClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
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
				fmt.Sprintf("-- Complex closure relation: %s", rel),
			},
			Query: q.Build(),
		})
	}

	return blocks, nil
}

// buildSelfRefUsersetIntersectionClosureBlocks builds blocks for intersection closure relations.
func buildSelfRefUsersetIntersectionClosureBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	intersectionRels := plan.Analysis.IntersectionClosureRelations
	if len(intersectionRels) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, rel := range intersectionRels {
		funcName := listObjectsFunctionName(plan.ObjectType, rel)

		// Build the subquery that calls the intersection function
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

// buildSelfRefUsersetPatternBlocks builds blocks for userset patterns (excluding self-referential).
func buildSelfRefUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	var blocks []TypedQueryBlock
	for _, pattern := range patterns {
		// Skip self-referential patterns - they're handled by the recursive block
		if pattern.IsSelfReferential {
			continue
		}

		if pattern.IsComplex {
			block, err := buildSelfRefUsersetComplexPatternBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		} else {
			block, err := buildSelfRefUsersetSimplePatternBlock(plan, pattern)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		}
	}

	return blocks, nil
}

// buildSelfRefUsersetComplexPatternBlock builds a block for a complex userset pattern.
func buildSelfRefUsersetComplexPatternBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	// For complex patterns, use check_permission_internal to verify membership
	checkExpr := CheckPermissionInternalExpr(
		SubjectParams(),
		pattern.SubjectRelation,
		ObjectRef{
			Type: Lit(pattern.SubjectType),
			ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
		},
		true,
	)

	q := Tuples("t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
			checkExpr,
		).
		SelectCol("object_id").
		Distinct()

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
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

// buildSelfRefUsersetSimplePatternBlock builds a block for a simple userset pattern.
func buildSelfRefUsersetSimplePatternBlock(plan ListPlan, pattern listUsersetPatternInput) (TypedQueryBlock, error) {
	// For simple patterns, use a JOIN with membership tuples
	membershipConditions := []Expr{
		Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: "m", Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}},
		In{Expr: Col{Table: "m", Column: "relation"}, Values: pattern.SatisfyingRelations},
		Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
	}

	// Subject ID matching for membership
	if plan.AllowWildcard {
		membershipConditions = append(membershipConditions,
			Or(
				Eq{Left: Col{Table: "m", Column: "subject_id"}, Right: SubjectID},
				IsWildcard{Col{Table: "m", Column: "subject_id"}},
			),
		)
	} else {
		membershipConditions = append(membershipConditions,
			Eq{Left: Col{Table: "m", Column: "subject_id"}, Right: SubjectID},
			NotExpr{Expr: IsWildcard{Col{Table: "m", Column: "subject_id"}}},
		)
	}

	grantConditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}

	// Add exclusion predicates
	for _, pred := range plan.Exclusions.BuildPredicates() {
		grantConditions = append(grantConditions, pred)
	}

	// For closure patterns, add check_permission validation
	if pattern.IsClosurePattern {
		grantConditions = append(grantConditions,
			CheckPermission{
				Subject:     SubjectParams(),
				Relation:    pattern.SourceRelation,
				Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
				ExpectAllow: true,
			},
		)
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "m",
			On:    And(membershipConditions...),
		}},
		Where: And(grantConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset path: Via %s#%s membership", pattern.SubjectType, pattern.SubjectRelation),
			"-- Simple userset: JOIN with membership tuples",
		},
		Query: stmt,
	}, nil
}

// buildSelfRefUsersetRecursiveBlock builds the recursive block for self-referential userset expansion.
func buildSelfRefUsersetRecursiveBlock(plan ListPlan) (*TypedQueryBlock, error) {
	// The recursive block expands userset chains like:
	// group:A#member -> group:B (where B has tuple group:B#member -> group:C#member)
	// This finds new objects reachable through self-referential userset references.

	exclusionPreds := plan.Exclusions.BuildPredicates()

	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(plan.Relation)},
		Raw("me.depth < 25"),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}, Raw("me.depth + 1 AS depth")},
		FromExpr:    TableAs("member_expansion", "me"),
		Joins: []JoinClause{{
			Type:  "INNER",
			Table: "melange_tuples",
			Alias: "t",
			On:    Eq{Left: UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, Right: Col{Table: "me", Column: "object_id"}},
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-referential userset expansion",
			fmt.Sprintf("-- For patterns like [%s#%s] on %s.%s", plan.ObjectType, plan.Relation, plan.ObjectType, plan.Relation),
		},
		Query: stmt,
	}, nil
}

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
func buildSelfRefUsersetFilterBlocks(plan ListPlan) ([]TypedQueryBlock, *TypedQueryBlock, *TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

	// Build the base block for userset filter - finds userset tuples that match
	baseBlock, err := buildSelfRefUsersetFilterBaseBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, baseBlock)

	// Build intersection closure blocks for userset filter path
	intersectionBlocks, err := buildSelfRefUsersetFilterIntersectionBlocks(plan)
	if err != nil {
		return nil, nil, nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	// Build self-candidate block
	selfBlock, err := buildSelfRefUsersetFilterSelfBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	// Build recursive block for userset expansion
	recursiveBlock, err := buildSelfRefUsersetFilterRecursiveBlock(plan)
	if err != nil {
		return nil, nil, nil, err
	}

	return blocks, selfBlock, recursiveBlock, nil
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
func buildSelfRefUsersetRegularBlocks(plan ListPlan) ([]TypedQueryBlock, *TypedQueryBlock, *TypedQueryBlock, error) {
	var blocks []TypedQueryBlock

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

// =============================================================================
// Composed Strategy Blocks
// =============================================================================

// ComposedObjectsBlockSet contains blocks for a composed list_objects function.
// Composed functions handle indirect anchor patterns (TTU and userset composition).
type ComposedObjectsBlockSet struct {
	// SelfBlock is the self-candidate check block
	SelfBlock *TypedQueryBlock

	// MainBlocks are the composed query blocks (TTU and/or userset paths)
	MainBlocks []TypedQueryBlock

	// AllowedSubjectTypes for the type guard
	AllowedSubjectTypes []string

	// Anchor metadata for comments
	AnchorType     string
	AnchorRelation string
	FirstStepType  string
}

// ComposedSubjectsBlockSet contains blocks for a composed list_subjects function.
type ComposedSubjectsBlockSet struct {
	// SelfBlock is the self-candidate check block (for userset filter)
	SelfBlock *TypedQueryBlock

	// UsersetFilterBlocks are candidate blocks for userset filter path
	UsersetFilterBlocks []TypedQueryBlock

	// RegularBlocks are candidate blocks for regular path
	RegularBlocks []TypedQueryBlock

	// AllowedSubjectTypes for the type guard
	AllowedSubjectTypes []string

	// HasExclusions indicates if exclusion predicates are needed
	HasExclusions bool

	// Anchor metadata for comments
	AnchorType     string
	AnchorRelation string
	FirstStepType  string
}

// BuildListObjectsComposedBlocks builds block set for composed list_objects function.
func BuildListObjectsComposedBlocks(plan ListPlan) (ComposedObjectsBlockSet, error) {
	anchor := plan.Analysis.IndirectAnchor
	if anchor == nil || len(anchor.Path) == 0 {
		return ComposedObjectsBlockSet{}, fmt.Errorf("missing indirect anchor data for %s.%s", plan.ObjectType, plan.Relation)
	}

	var result ComposedObjectsBlockSet
	result.AllowedSubjectTypes = plan.AllowedSubjectTypes
	result.AnchorType = anchor.AnchorType
	result.AnchorRelation = anchor.AnchorRelation
	result.FirstStepType = anchor.Path[0].Type

	// Build self-candidate block
	selfBlock, err := buildComposedObjectsSelfBlock(plan)
	if err != nil {
		return ComposedObjectsBlockSet{}, err
	}
	result.SelfBlock = selfBlock

	// Build main composed query blocks
	firstStep := anchor.Path[0]
	exclusions := buildSimpleComplexExclusionInput(plan.Analysis, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID)

	switch firstStep.Type {
	case "ttu":
		// Build TTU blocks for each target type
		for _, targetType := range firstStep.AllTargetTypes {
			block, err := buildComposedTTUObjectsBlock(plan, anchor, targetType, exclusions)
			if err != nil {
				return ComposedObjectsBlockSet{}, err
			}
			result.MainBlocks = append(result.MainBlocks, *block)
		}

		// Build recursive TTU blocks
		for _, recursiveType := range firstStep.RecursiveTypes {
			block, err := buildComposedRecursiveTTUObjectsBlock(plan, anchor, recursiveType, exclusions)
			if err != nil {
				return ComposedObjectsBlockSet{}, err
			}
			result.MainBlocks = append(result.MainBlocks, *block)
		}

	case "userset":
		block, err := buildComposedUsersetObjectsBlock(plan, anchor, firstStep, exclusions)
		if err != nil {
			return ComposedObjectsBlockSet{}, err
		}
		result.MainBlocks = append(result.MainBlocks, *block)
	}

	return result, nil
}

// buildComposedObjectsSelfBlock builds the self-candidate check block.
func buildComposedObjectsSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
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
		ColumnExprs: []Expr{SelectAs(UsersetObjectID{Source: SubjectID}, "object_id")},
		Where: And(
			Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
			HasUserset{Source: SubjectID},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: when subject is a userset on the same object type",
		},
		Query: stmt,
	}, nil
}

// buildComposedTTUObjectsBlock builds a TTU composition block.
func buildComposedTTUObjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, targetType string, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Build subquery for list function call
	targetFunction := fmt.Sprintf("list_%s_%s_objects", targetType, anchor.Path[0].TargetRelation)
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) obj", targetFunction)

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(targetType)},
		Raw("t.subject_id IN ("+subquery+")"),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- TTU composition: %s -> %s", anchor.Path[0].LinkingRelation, targetType),
		},
		Query: stmt,
	}, nil
}

// buildComposedRecursiveTTUObjectsBlock builds a recursive TTU composition block.
func buildComposedRecursiveTTUObjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, recursiveType string, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	conditions := make([]Expr, 0, 4+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(recursiveType)},
		CheckPermissionInternalExpr(SubjectParams(), anchor.Path[0].TargetRelation, ObjectRef{Type: Lit(recursiveType), ID: Col{Table: "t", Column: "subject_id"}}, true),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Recursive TTU: %s -> %s", anchor.Path[0].LinkingRelation, recursiveType),
		},
		Query: stmt,
	}, nil
}

// buildComposedUsersetObjectsBlock builds a userset composition block.
func buildComposedUsersetObjectsBlock(plan ListPlan, _ *IndirectAnchorInfo, firstStep AnchorPathStep, exclusions ExclusionConfig) (*TypedQueryBlock, error) {
	exclusionPreds := exclusions.BuildPredicates()

	// Build subquery for list function call
	targetFunction := fmt.Sprintf("list_%s_%s_objects", firstStep.SubjectType, firstStep.SubjectRelation)
	subquery := fmt.Sprintf("SELECT obj.object_id FROM %s(p_subject_type, p_subject_id, NULL, NULL) obj", targetFunction)

	conditions := make([]Expr, 0, 6+len(exclusionPreds))
	conditions = append(conditions,
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
		Or(
			Raw("split_part(t.subject_id, '#', 1) IN ("+subquery+")"),
			CheckPermissionInternalExpr(SubjectParams(), firstStep.SubjectRelation, ObjectRef{Type: Lit(firstStep.SubjectType), ID: Raw("split_part(t.subject_id, '#', 1)")}, true),
		),
	)
	conditions = append(conditions, exclusionPreds...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "object_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Where:       And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset composition: %s#%s", firstStep.SubjectType, firstStep.SubjectRelation),
		},
		Query: stmt,
	}, nil
}

// BuildListSubjectsComposedBlocks builds block set for composed list_subjects function.
func BuildListSubjectsComposedBlocks(plan ListPlan) (ComposedSubjectsBlockSet, error) {
	anchor := plan.Analysis.IndirectAnchor
	if anchor == nil || len(anchor.Path) == 0 {
		return ComposedSubjectsBlockSet{}, fmt.Errorf("missing indirect anchor data for %s.%s", plan.ObjectType, plan.Relation)
	}

	var result ComposedSubjectsBlockSet
	result.AllowedSubjectTypes = plan.AllowedSubjectTypes
	result.AnchorType = anchor.AnchorType
	result.AnchorRelation = anchor.AnchorRelation
	result.FirstStepType = anchor.Path[0].Type

	// Build self-candidate block
	selfBlock, err := buildComposedSubjectsSelfBlock(plan)
	if err != nil {
		return ComposedSubjectsBlockSet{}, err
	}
	result.SelfBlock = selfBlock

	// Build candidate blocks for both userset filter and regular paths
	candidateBlocks, err := buildTypedComposedSubjectsCandidateBlocks(plan, anchor)
	if err != nil {
		return ComposedSubjectsBlockSet{}, err
	}
	result.UsersetFilterBlocks = candidateBlocks
	result.RegularBlocks = candidateBlocks

	// Check for exclusions
	exclusions := buildSimpleComplexExclusionInput(plan.Analysis, ObjectID, SubjectType, Col{Table: "sc", Column: "subject_id"})
	result.HasExclusions = len(exclusions.BuildPredicates()) > 0

	return result, nil
}

// buildComposedSubjectsSelfBlock builds the self-candidate block for list_subjects.
// This returns a userset subject_id like "document:1#viewer" when the filter type matches
// the object type and the filter relation satisfies the target relation.
func buildComposedSubjectsSelfBlock(plan ListPlan) (*TypedQueryBlock, error) {
	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, plan.Inline.ClosureValues, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(plan.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	// Subject ID output: object_id || '#' || filter_relation
	subjectIDCol := SelectAs(Concat{Parts: []Expr{ObjectID, Lit("#"), Param("v_filter_relation")}}, "subject_id")

	stmt := SelectStmt{
		ColumnExprs: []Expr{subjectIDCol},
		Where: And(
			Eq{Left: Param("v_filter_type"), Right: Lit(plan.ObjectType)},
			Raw(closureStmt.Exists()),
		),
	}

	return &TypedQueryBlock{
		Comments: []string{
			"-- Self-candidate: object_id#filter_relation when filter type matches object type",
		},
		Query: stmt,
	}, nil
}

// buildTypedComposedSubjectsCandidateBlocks builds candidate blocks for composed subjects.
// This is the typed version using TypedQueryBlock instead of string.
func buildTypedComposedSubjectsCandidateBlocks(plan ListPlan, anchor *IndirectAnchorInfo) ([]TypedQueryBlock, error) {
	if anchor == nil || len(anchor.Path) == 0 {
		return nil, nil
	}

	firstStep := anchor.Path[0]
	var blocks []TypedQueryBlock

	switch firstStep.Type {
	case "ttu":
		for _, targetType := range firstStep.AllTargetTypes {
			block, err := buildComposedTTUSubjectsBlock(plan, anchor, targetType)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)
		}

		for _, recursiveType := range firstStep.RecursiveTypes {
			block, err := buildComposedTTUSubjectsBlock(plan, anchor, recursiveType)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, *block)
		}

	case "userset":
		block, err := buildComposedUsersetSubjectsBlock(plan, firstStep)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, *block)
	}

	return blocks, nil
}

// buildComposedTTUSubjectsBlock builds a TTU subjects candidate block.
func buildComposedTTUSubjectsBlock(plan ListPlan, anchor *IndirectAnchorInfo, targetType string) (*TypedQueryBlock, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(link.subject_id, p_subject_type)", targetType, anchor.Path[0].TargetRelation)

	conditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(anchor.Path[0].LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Lit(targetType)},
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil, // CROSS JOIN has no ON clause
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- From %s parents", targetType),
		},
		Query: stmt,
	}, nil
}

// buildComposedUsersetSubjectsBlock builds a userset subjects candidate block.
func buildComposedUsersetSubjectsBlock(plan ListPlan, firstStep AnchorPathStep) (*TypedQueryBlock, error) {
	listFunction := fmt.Sprintf("list_%s_%s_subjects(split_part(t.subject_id, '#', 1), p_subject_type)", firstStep.SubjectType, firstStep.SubjectRelation)

	conditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.RelationList},
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(firstStep.SubjectType)},
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(firstStep.SubjectRelation)},
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "s", Column: "subject_id"}},
		FromExpr:    TableAs("melange_tuples", "t"),
		Joins: []JoinClause{{
			Type:  "CROSS",
			Table: "LATERAL " + listFunction,
			Alias: "s",
			On:    nil,
		}},
		Where: And(conditions...),
	}

	return &TypedQueryBlock{
		Comments: []string{
			fmt.Sprintf("-- Userset: %s#%s grants", firstStep.SubjectType, firstStep.SubjectRelation),
		},
		Query: stmt,
	}, nil
}
