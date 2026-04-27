package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// SubjectsRecursiveBlockSet contains blocks for a recursive list_subjects function.
type SubjectsRecursiveBlockSet struct {
	RegularBlocks          []TypedQueryBlock
	RegularTTUBlocks       []TypedQueryBlock
	UsersetFilterBlocks    []TypedQueryBlock
	UsersetFilterSelfBlock *TypedQueryBlock
	ParentRelations        []ListParentRelationData
}

// BuildListSubjectsRecursiveBlocks builds blocks for a recursive list_subjects function.
func BuildListSubjectsRecursiveBlocks(plan ListPlan) (SubjectsRecursiveBlockSet, error) {
	parentRelations := buildListParentRelations(plan.Analysis)

	regularBlocks, err := buildListSubjectsRecursiveRegularBlocks(plan)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}

	usersetBlocks, err := buildSubjectsRecursiveUsersetFilterTypedBlocks(plan, parentRelations)
	if err != nil {
		return SubjectsRecursiveBlockSet{}, err
	}

	return SubjectsRecursiveBlockSet{
		RegularBlocks:          regularBlocks,
		RegularTTUBlocks:       buildListSubjectsRecursiveTTUBlocks(plan, parentRelations),
		UsersetFilterBlocks:    usersetBlocks,
		UsersetFilterSelfBlock: buildListSubjectsUsersetFilterSelfBlock(plan),
		ParentRelations:        parentRelations,
	}, nil
}

// buildListSubjectsRecursiveRegularBlocks builds the regular path blocks for recursive list_subjects.
func buildListSubjectsRecursiveRegularBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	blocks := []TypedQueryBlock{buildListSubjectsRecursiveDirectBlock(plan)}

	blocks = append(blocks, buildListSubjectsRecursiveComplexClosureBlocks(plan)...)

	intersectionBlocks, err := buildListSubjectsIntersectionClosureBlocks(plan, "p_subject_type", "p_subject_type", plan.HasExclusion)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, intersectionBlocks...)

	usersetBlocks, err := buildListSubjectsRecursiveUsersetPatternBlocks(plan)
	if err != nil {
		return nil, err
	}
	blocks = append(blocks, usersetBlocks...)

	return blocks, nil
}

// buildListSubjectsRecursiveDirectBlock builds the direct tuple lookup block for recursive list_subjects.
func buildListSubjectsRecursiveDirectBlock(plan ListPlan) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
		).
		SelectCol("subject_id").
		Distinct()

	applyWildcardExclusion(q, plan, "t")
	applyExclusionPredicates(q, plan.Exclusions, plan.UseCTEExclusion)

	return TypedQueryBlock{
		Comments: []string{"-- Direct tuple lookup with simple closure relations"},
		Query:    q.Build(),
	}
}

// buildListSubjectsRecursiveComplexClosureBlocks builds blocks for complex closure relations.
func buildListSubjectsRecursiveComplexClosureBlocks(plan ListPlan) []TypedQueryBlock {
	if len(plan.ComplexClosure) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(plan.ComplexClosure))
	for _, rel := range plan.ComplexClosure {
		q := Tuples(plan.DatabaseSchema, "t").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
				NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
				CheckPermission{
					Schema: plan.DatabaseSchema,
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

		applyWildcardExclusion(q, plan, "t")
		applyExclusionPredicates(q, plan.Exclusions, plan.UseCTEExclusion)

		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
			Query:    q.Build(),
		})
	}

	return blocks
}

// buildListSubjectsRecursiveUsersetPatternBlocks builds blocks for userset patterns in recursive list_subjects.
func buildListSubjectsRecursiveUsersetPatternBlocks(plan ListPlan) ([]TypedQueryBlock, error) {
	patterns := buildListUsersetPatternInputs(plan.Analysis)
	if len(patterns) == 0 {
		return nil, nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern.IsComplex {
			blocks = append(blocks, buildListSubjectsRecursiveComplexUsersetBlock(plan, pattern))
		} else {
			blocks = append(blocks, buildListSubjectsRecursiveSimpleUsersetBlock(plan, pattern))
		}
	}

	return blocks, nil
}

// buildListSubjectsRecursiveComplexUsersetBlock builds a complex userset block for recursive list_subjects.
func buildListSubjectsRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	const grantAlias, memberAlias = "g", "m"

	memberExclusions := buildExclusionInput(plan.Analysis, plan.DatabaseSchema, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

	checkExpr := CheckPermissionInternalExpr(
		plan.DatabaseSchema,
		SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
		pattern.SubjectRelation,
		ObjectRef{Type: Lit(pattern.SubjectType), ID: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
		true,
	)

	joinCond := And(
		Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
		Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
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
		checkExpr,
	}

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Schema:      plan.DatabaseSchema,
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", grantAlias),
		Joins: []JoinClause{{
			Type:   "INNER",
			Schema: "",
			Table:  "melange_tuples",
			Alias:  memberAlias,
			On:     joinCond,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (complex)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveSimpleUsersetBlock builds a simple userset block for recursive list_subjects.
func buildListSubjectsRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	const grantAlias, memberAlias = "g", "s"

	memberExclusions := buildExclusionInput(plan.Analysis, plan.DatabaseSchema, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

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

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	if pattern.IsClosurePattern {
		whereConditions = append(whereConditions, CheckPermission{
			Schema:      plan.DatabaseSchema,
			Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: memberAlias, Column: "subject_id"}},
			Relation:    pattern.SourceRelation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			ExpectAllow: true,
		})
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", grantAlias),
		Joins: []JoinClause{{
			Type:   "INNER",
			Schema: "",
			Table:  "melange_tuples",
			Alias:  memberAlias,
			On:     joinCond,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (simple)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveTTUBlocks builds the TTU path blocks for recursive list_subjects.
func buildListSubjectsRecursiveTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData) []TypedQueryBlock {
	if len(parentRelations) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(parentRelations))
	for _, parent := range parentRelations {
		blocks = append(blocks, buildListSubjectsRecursiveTTUBlock(plan, parent)...)
	}
	return blocks
}

// parentRelationStrategy describes how a parent relation should be resolved
// during list_subjects TTU evaluation.
type parentRelationStrategy int

const (
	// parentStrategyClosure uses parent_closure CTE + JOINs over melange_tuples.
	// Sufficient for parent relations whose features are some combination of
	// Direct, Implied, Wildcard, Userset (simple member only), Recursive.
	parentStrategyClosure parentRelationStrategy = iota

	// parentStrategySubjectPool uses subject_pool + per-row check_permission_internal.
	// Required when the parent relation has intersection or exclusion at the
	// parent level, or userset patterns whose member relations are themselves
	// non-closure-compatible.
	parentStrategySubjectPool
)

// classifyParentRelation picks the dispatch strategy for `parent` by looking
// at every allowed parent type's target relation. If any parent type forces
// subject_pool, all paths must use subject_pool to keep the result set
// consistent.
func classifyParentRelation(plan ListPlan, parent ListParentRelationData) parentRelationStrategy {
	if plan.AnalysisLookup == nil {
		// Without analysis, mirror the historical default: closure path.
		return parentStrategyClosure
	}

	for _, parentType := range parent.AllowedLinkingTypesSlice {
		key := parentType + "." + parent.Relation
		if parentTargetNeedsSubjectPool(plan.AnalysisLookup[key], parent) {
			return parentStrategySubjectPool
		}
	}
	return parentStrategyClosure
}

// parentTargetNeedsSubjectPool returns true when the parent target relation
// cannot be safely evaluated by the closure path. The closure path materializes
// ancestor objects via one linking relation and looks up direct/userset grants
// on those ancestors; it cannot evaluate nested TTU semantics or
// intersection/exclusion at the parent level.
//
// Conditions that force subject_pool:
//   - Parent target relation has intersection or exclusion.
//   - Parent target relation has complex userset patterns.
//   - Parent target relation has nested TTUs that are not (a) self-referential
//     on the target's own object type AND (b) using the same linking relation
//     as the current parent_closure walk.
func parentTargetNeedsSubjectPool(target *RelationAnalysis, currentParent ListParentRelationData) bool {
	if target == nil {
		return true
	}
	if target.Features.HasIntersection || target.Features.HasExclusion {
		return true
	}
	if target.HasComplexUsersetPatterns {
		return true
	}

	// Nested TTUs: ParentRelations are direct, ClosureParentRelations are
	// inherited via implied relations. Both must satisfy the closure path's
	// preconditions.
	for _, pr := range target.ParentRelations {
		if nestedParentForcesSubjectPool(pr, target.ObjectType, currentParent.LinkingRelation) {
			return true
		}
	}
	for _, pr := range target.ClosureParentRelations {
		if nestedParentForcesSubjectPool(pr, target.ObjectType, currentParent.LinkingRelation) {
			return true
		}
	}
	return false
}

// nestedParentForcesSubjectPool returns true when a nested parent relation
// breaks one of the closure path's preconditions: every allowed linking type
// must equal the target object type, and the linking relation must match the
// current walk.
func nestedParentForcesSubjectPool(pr ParentRelationInfo, targetObjectType, currentLinkingRelation string) bool {
	if len(pr.AllowedLinkingTypes) == 0 {
		return true
	}
	for _, lt := range pr.AllowedLinkingTypes {
		// Cross-type link: parent_closure walks via the current linking
		// relation only; we can't evaluate the chain across a different type.
		if lt != targetObjectType {
			return true
		}
	}
	// Self-referential but on a different linking relation than the current
	// walk traverses; parent_closure can't reach those ancestors.
	return pr.LinkingRelation != currentLinkingRelation
}

// buildListSubjectsRecursiveTTUBlock builds the TTU path blocks for one
// parent relation. The closure path emits a Direct block plus zero or more
// userset blocks, depending on what features the parent relation declares.
//
// Wildcard rows ('*') are surfaced by the Direct block when ExcludeWildcard
// is false (its WHERE only filters '*' when ExcludeWildcard is true), so no
// dedicated wildcard block is needed today.
func buildListSubjectsRecursiveTTUBlock(plan ListPlan, parent ListParentRelationData) []TypedQueryBlock {
	if classifyParentRelation(plan, parent) == parentStrategySubjectPool {
		return []TypedQueryBlock{buildListSubjectsRecursiveTTUBlockSubjectPool(plan, parent)}
	}

	usersetBlocks := buildListSubjectsRecursiveTTUBlockParentClosureUserset(plan, parent)
	blocks := make([]TypedQueryBlock, 0, 1+len(usersetBlocks))
	blocks = append(blocks, buildListSubjectsRecursiveTTUBlockParentClosure(plan, parent))
	blocks = append(blocks, usersetBlocks...)
	return blocks
}

// parentRelationAnalysis returns the analysis for the parent target relation
// at any one allowed parent type. The strategy classifier has already
// verified that all parent types share the same general shape, so looking at
// the first one is sufficient. Returns nil if no analysis is available.
func parentRelationAnalysis(plan ListPlan, parent ListParentRelationData) *RelationAnalysis {
	if plan.AnalysisLookup == nil {
		return nil
	}
	for _, parentType := range parent.AllowedLinkingTypesSlice {
		if a := plan.AnalysisLookup[parentType+"."+parent.Relation]; a != nil {
			return a
		}
	}
	return nil
}

// collectParentSatisfyingRelations collects all satisfying relations for the parent relation
// across all parent types. For implied relations like "can_read: member" on organization,
// this returns ["can_read", "member"] so that tuple lookups match actual tuples.
func collectParentSatisfyingRelations(plan ListPlan, parent ListParentRelationData) []string {
	if plan.AnalysisLookup == nil || len(parent.AllowedLinkingTypesSlice) == 0 {
		return []string{parent.Relation}
	}

	seen := make(map[string]bool)
	var result []string

	for _, parentType := range parent.AllowedLinkingTypesSlice {
		key := parentType + "." + parent.Relation
		analysis := plan.AnalysisLookup[key]

		rels := []string{parent.Relation}
		if analysis != nil && len(analysis.SatisfyingRelations) > 0 {
			rels = analysis.SatisfyingRelations
		}

		for _, rel := range rels {
			if !seen[rel] {
				seen[rel] = true
				result = append(result, rel)
			}
		}
	}

	return result
}

// buildListSubjectsRecursiveTTUBlockParentClosure builds a TTU block using parent closure optimization.
// This scans for direct grants on parent ancestors - only correct for simple parent relations.
func buildListSubjectsRecursiveTTUBlockParentClosure(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	exclusions := buildExclusionInput(plan.Analysis, plan.DatabaseSchema, ObjectID, SubjectType, Col{Table: "t", Column: "subject_id"})

	// Collect satisfying relations for the parent relation across all parent types.
	// For implied relations like "can_read: member", we need to look up tuples with
	// any relation that satisfies "can_read" (e.g., "member"), not just "can_read" itself.
	satisfyingRelations := collectParentSatisfyingRelations(plan, parent)

	// Build query that scans for grants on parent ancestors
	// parent_closure CTE returns (subject_type, subject_id, depth) where subject is the parent object
	whereConditions := []Expr{
		Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
		In{Expr: Col{Table: "t", Column: "relation"}, Values: satisfyingRelations},
		NoUserset{Source: Col{Table: "t", Column: "subject_id"}},
	}

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, exclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "t", Column: "subject_id"}},
		FromExpr:    TableAs("", "parent_closure", "p"),
		Joins: []JoinClause{{
			Type:   "INNER",
			Schema: "",
			Table:  "melange_tuples",
			Alias:  "t",
			On: And(
				Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Col{Table: "p", Column: "subject_type"}},
				Eq{Left: Col{Table: "t", Column: "object_id"}, Right: Col{Table: "p", Column: "subject_id"}},
			),
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU: subjects via %s -> %s (parent closure optimization)", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveTTUBlockParentClosureUserset emits one block per
// userset pattern at the parent relation level. Each block enumerates members
// of any userset (e.g., group#member) granted on any ancestor in parent_closure.
//
// Member resolution is a direct tuple JOIN, which is correct only when the
// member relation is closure-compatible; classifyParentRelation routes parents
// with complex userset patterns to subject_pool instead.
//
// Userset patterns can arrive directly or via the closure (an implied
// relation like can_view = viewer can inherit viewer's [group#member]).
// buildListUsersetPatternInputs already merges both sources.
func buildListSubjectsRecursiveTTUBlockParentClosureUserset(plan ListPlan, parent ListParentRelationData) []TypedQueryBlock {
	parentAnalysis := parentRelationAnalysis(plan, parent)
	if parentAnalysis == nil {
		return nil
	}

	patterns := buildListUsersetPatternInputs(*parentAnalysis)
	if len(patterns) == 0 {
		return nil
	}

	blocks := make([]TypedQueryBlock, 0, len(patterns))
	for _, pattern := range patterns {
		// classifyParentRelation routes complex userset patterns to
		// subject_pool, but guard defensively in case a future change widens
		// the closure path further.
		if pattern.IsComplex {
			continue
		}
		blocks = append(blocks, buildListSubjectsRecursiveTTUBlockParentClosureUsersetPattern(plan, parent, pattern))
	}
	return blocks
}

// buildListSubjectsRecursiveTTUBlockParentClosureUsersetPattern builds a
// single (parent_closure ⋈ grant ⋈ member) JOIN for one userset pattern.
func buildListSubjectsRecursiveTTUBlockParentClosureUsersetPattern(plan ListPlan, parent ListParentRelationData, pattern listUsersetPatternInput) TypedQueryBlock {
	const grantAlias, memberAlias = "g", "m"

	memberExclusions := buildExclusionInput(plan.Analysis, plan.DatabaseSchema, ObjectID, Col{Table: memberAlias, Column: "subject_type"}, Col{Table: memberAlias, Column: "subject_id"})

	whereConditions := []Expr{
		In{Expr: Col{Table: grantAlias, Column: "relation"}, Values: pattern.SourceRelations},
		Eq{Left: Col{Table: grantAlias, Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: Col{Table: grantAlias, Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: grantAlias, Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
		Eq{Left: Col{Table: memberAlias, Column: "subject_type"}, Right: SubjectType},
		In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
		NoUserset{Source: Col{Table: memberAlias, Column: "subject_id"}},
	}

	if plan.ExcludeWildcard() {
		whereConditions = append(whereConditions, Ne{Left: Col{Table: memberAlias, Column: "subject_id"}, Right: Lit("*")})
	}
	whereConditions = append(whereConditions, memberExclusions.BuildPredicates()...)

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: memberAlias, Column: "subject_id"}},
		FromExpr:    TableAs("", "parent_closure", "p"),
		Joins: []JoinClause{
			{
				Type:   "INNER",
				Schema: "",
				Table:  "melange_tuples",
				Alias:  grantAlias,
				On: And(
					Eq{Left: Col{Table: grantAlias, Column: "object_type"}, Right: Col{Table: "p", Column: "subject_type"}},
					Eq{Left: Col{Table: grantAlias, Column: "object_id"}, Right: Col{Table: "p", Column: "subject_id"}},
				),
			},
			{
				Type:   "INNER",
				Schema: "",
				Table:  "melange_tuples",
				Alias:  memberAlias,
				On: And(
					Eq{Left: Col{Table: memberAlias, Column: "object_type"}, Right: Lit(pattern.SubjectType)},
					Eq{Left: Col{Table: memberAlias, Column: "object_id"}, Right: UsersetObjectID{Source: Col{Table: grantAlias, Column: "subject_id"}}},
					In{Expr: Col{Table: memberAlias, Column: "relation"}, Values: pattern.SatisfyingRelations},
				),
			},
		},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU userset: subjects via %s -> %s -> %s#%s (parent closure)", parent.LinkingRelation, parent.Relation, pattern.SubjectType, pattern.SubjectRelation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveTTUBlockSubjectPool builds a TTU block using subject_pool + check_permission_internal.
// This is used when the parent relation is complex (has intersection, exclusion, etc.) and cannot use
// the parent closure optimization. It verifies each subject-parent combination via permission check.
func buildListSubjectsRecursiveTTUBlockSubjectPool(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	// Build WHERE clause for linking relation tuples
	linkWhere := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		linkWhere = append(linkWhere, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	// Build check_permission_internal call
	// For closure patterns, verify through the SOURCE relation (e.g., "reader") not the current relation (e.g., "can_read")
	// This ensures exclusions and other complex features from the source relation are honored
	var checkCallSQL string
	var comment string
	if parent.IsClosurePattern {
		// Closure pattern: verify through source relation on the TARGET object
		// Example: can_read->owner->repo_admin should verify "reader" on the target object (repo)
		// to honor "reader: repo_admin from owner but not restricted"
		checkCallSQL = fmt.Sprintf(
			"%s(%s, %s, %s, %s, %s) = 1",
			sqldsl.PrefixIdent("check_permission_internal", plan.DatabaseSchema),
			SubjectType.SQL(), // subject_type (param)
			Col{Table: "sp", Column: "subject_id"}.SQL(), // subject_id (from subject_pool)
			Lit(parent.SourceRelation).SQL(),             // relation to check (SOURCE relation like "reader")
			Lit(plan.ObjectType).SQL(),                   // object_type (target object type like "repo")
			ObjectID.SQL(),                               // object_id (target object id - param)
		)
		comment = fmt.Sprintf("-- TTU: subjects via %s -> %s (closure pattern from %s - verifying through source relation)", parent.LinkingRelation, parent.Relation, parent.SourceRelation)
	} else {
		// Direct parent pattern: verify the parent relation on the parent object
		// check_permission_internal(subject_type, subject_id, relation, object_type, object_id)
		checkCallSQL = fmt.Sprintf(
			"%s(%s, %s, %s, %s, %s) = 1",
			sqldsl.PrefixIdent("check_permission_internal", plan.DatabaseSchema),
			SubjectType.SQL(), // subject_type (param)
			Col{Table: "sp", Column: "subject_id"}.SQL(),     // subject_id (from subject_pool)
			Lit(parent.Relation).SQL(),                       // relation to check on parent
			Col{Table: "link", Column: "subject_type"}.SQL(), // object_type (parent type)
			Col{Table: "link", Column: "subject_id"}.SQL(),   // object_id (parent id)
		)
		comment = fmt.Sprintf("-- TTU: subjects via %s -> %s (complex parent relation - using subject_pool)", parent.LinkingRelation, parent.Relation)
	}

	// Build the query: CROSS JOIN subject_pool with parent links, filter by permission check
	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "sp", Column: "subject_id"}},
		FromExpr:    TableAs("", "subject_pool", "sp"),
		Joins: []JoinClause{{
			Type:   "CROSS",
			Schema: "",
			Table:  "melange_tuples",
			Alias:  "link",
		}},
		Where: And(append(linkWhere, Raw(checkCallSQL))...),
	}

	return TypedQueryBlock{
		Comments: []string{comment},
		Query:    stmt,
	}
}

// buildSubjectsRecursiveUsersetFilterTypedBlocks builds the userset filter path blocks.
func buildSubjectsRecursiveUsersetFilterTypedBlocks(plan ListPlan, parentRelations []ListParentRelationData) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)
	blocks = append(blocks, buildListSubjectsRecursiveUsersetFilterDirectBlock(plan))

	for _, parent := range parentRelations {
		blocks = append(blocks,
			buildListSubjectsRecursiveUsersetFilterTTUBlock(plan, parent),
			buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan, parent),
			buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan, parent),
		)
	}

	filterUsersetExpr := Concat{Parts: []Expr{Param("v_filter_type"), Lit("#"), Param("v_filter_relation")}}
	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		funcName := listSubjectsFunctionName(plan.ObjectType, rel)
		stmt := SelectStmt{
			Distinct:    true,
			ColumnExprs: []Expr{Col{Table: "ics", Column: "subject_id"}},
			FromExpr: FunctionCallExpr{
				Schema: plan.DatabaseSchema,
				Name:   funcName,
				Args:   []Expr{ObjectID, filterUsersetExpr},
				Alias:  "ics",
			},
		}
		blocks = append(blocks, TypedQueryBlock{
			Comments: []string{fmt.Sprintf("-- Intersection closure: %s", rel)},
			Query:    stmt,
		})
	}

	return blocks, nil
}

// buildListSubjectsRecursiveUsersetFilterDirectBlock builds the direct userset tuples block.
func buildListSubjectsRecursiveUsersetFilterDirectBlock(plan ListPlan) TypedQueryBlock {
	checkExpr := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
		Relation:    plan.Relation,
		Object:      LiteralObject(plan.ObjectType, Param("p_object_id")),
		ExpectAllow: true,
	}

	closureStmt := SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    ClosureTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Raw("substring(t.subject_id from position('#' in t.subject_id) + 1)")},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	subjectExpr := Alias{
		Expr: Concat{Parts: []Expr{
			UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			Lit("#"),
			Param("v_filter_relation"),
		}},
		Name: "subject_id",
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("", "melange_tuples", "t"),
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
		Comments: []string{"-- Direct userset tuples"},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUBlock builds the TTU path block for userset filter.
func buildListSubjectsRecursiveUsersetFilterTTUBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	closureRelStmt := SelectStmt{
		Columns:  []string{"c.satisfying_relation"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
		),
	}

	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "subj_c"),
		Where: And(
			Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Param("v_filter_type")},
			Eq{Left: Col{Table: "subj_c", Column: "relation"}, Right: Raw("substring(pt.subject_id from position('#' in pt.subject_id) + 1)")},
			Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

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

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	subjectExpr := Raw("substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("", "melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:   "INNER",
			Schema: "",
			Table:  "melange_tuples",
			Alias:  "pt",
			On: And(
				Eq{Left: Col{Table: "pt", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
				Eq{Left: Col{Table: "pt", Column: "object_id"}, Right: Col{Table: "link", Column: "subject_id"}},
				Raw("pt.relation IN ("+closureRelStmt.SQL()+")"),
			),
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- TTU userset: %s -> %s", parent.LinkingRelation, parent.Relation)},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock builds the intermediate TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUIntermediateBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	closureExistsStmt := SelectStmt{
		Columns:  []string{"1"},
		FromExpr: TypedClosureValuesTable(plan.Inline.ClosureRows, "c"),
		Where: And(
			Eq{Left: Col{Table: "c", Column: "object_type"}, Right: Col{Table: "link", Column: "subject_type"}},
			Eq{Left: Col{Table: "c", Column: "relation"}, Right: Lit(parent.Relation)},
			Eq{Left: Col{Table: "c", Column: "satisfying_relation"}, Right: Param("v_filter_relation")},
		),
	}

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
		Eq{Left: Col{Table: "link", Column: "subject_type"}, Right: Param("v_filter_type")},
		Exists{Query: closureExistsStmt},
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	subjectExpr := Raw("link.subject_id || '#' || v_filter_relation AS subject_id")

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{subjectExpr},
		FromExpr:    TableAs("", "melange_tuples", "link"),
		Where:       And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- TTU intermediate: parent object as userset reference"},
		Query:    stmt,
	}
}

// buildListSubjectsRecursiveUsersetFilterTTUNestedBlock builds the nested TTU block.
func buildListSubjectsRecursiveUsersetFilterTTUNestedBlock(plan ListPlan, parent ListParentRelationData) TypedQueryBlock {
	lateralCall := LateralFunction{
		Schema: plan.DatabaseSchema,
		Name:   "list_accessible_subjects",
		Args: []Expr{
			Col{Table: "link", Column: "subject_type"},
			Col{Table: "link", Column: "subject_id"},
			Lit(parent.Relation),
			SubjectType,
		},
		Alias: "nested",
	}

	whereConditions := []Expr{
		Eq{Left: Col{Table: "link", Column: "object_type"}, Right: Lit(plan.ObjectType)},
		Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "link", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
	}

	if len(parent.AllowedLinkingTypesSlice) > 0 {
		whereConditions = append(whereConditions, In{Expr: Col{Table: "link", Column: "subject_type"}, Values: parent.AllowedLinkingTypesSlice})
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "nested", Column: "subject_id"}},
		FromExpr:    TableAs("", "melange_tuples", "link"),
		Joins: []JoinClause{{
			Type:      "CROSS",
			TableExpr: lateralCall,
		}},
		Where: And(whereConditions...),
	}

	return TypedQueryBlock{
		Comments: []string{"-- TTU nested: multi-hop chain resolution"},
		Query:    stmt,
	}
}
