package sqlgen

import (
	"fmt"
	"strings"
)

// RecursiveBlockSet contains blocks for a recursive list function.
type RecursiveBlockSet struct {
	BaseBlocks         []TypedQueryBlock
	RecursiveBlock     *TypedQueryBlock
	SelfCandidateBlock *TypedQueryBlock
	// HoistedCTEs are shared list_*_obj computations lifted out of the base
	// blocks. The same list_<parent>_obj(p_subject_type, p_subject_id, NULL,
	// NULL) call was previously inlined in both the userset-subject arm and the
	// cross-type-TTU subject-first arm; these CTEs compute each distinct call
	// once and the arms reference them. Rendered before the accessible CTE.
	HoistedCTEs []CTEDef
}

// hoistedListObjTargets maps "targetType.targetRelation" to the CTE name that
// hoists that relation's list_*_obj call. Empty/nil when no hoisting applies.
type hoistedListObjTargets map[string]string

func hoistKey(targetType, targetRelation string) string {
	return targetType + "." + targetRelation
}

// name returns the CTE name for a hoisted target, or "" if not hoisted.
func (h hoistedListObjTargets) name(targetType, targetRelation string) string {
	return h[hoistKey(targetType, targetRelation)]
}

// collectHoistedListObjTargets returns the distinct list_*_obj targets that the
// base blocks would otherwise inline more than once (or that are shared between
// the userset-subject and cross-type-TTU subject-first arms), along with the
// CTEs computing each once. Deduplicating by (targetType, targetRelation) means
// each distinct list_*_obj target is invoked once per generated function.
func collectHoistedListObjTargets(plan ListPlan, parentRelations []ListParentRelationData) (targets hoistedListObjTargets, ctes []CTEDef) {
	type target struct{ typ, rel string }
	var order []target
	seen := make(map[string]bool)

	add := func(typ, rel string) {
		if composableListTarget(plan, typ, rel) && !seen[hoistKey(typ, rel)] {
			seen[hoistKey(typ, rel)] = true
			order = append(order, target{typ, rel})
		}
	}

	// Userset-subject arms: only complex patterns semi-join against the userset
	// target's list_*_obj set (simple patterns JOIN membership tuples instead).
	// composableListTarget gates composition; see usersetMembership.
	for _, pattern := range buildListUsersetPatternInputs(plan.Analysis) {
		if pattern.IsComplex {
			add(pattern.SubjectType, pattern.SubjectRelation)
		}
	}

	// Cross-type-TTU subject-first arms: parent_obj is that parent's list_*_obj set.
	for _, parent := range parentRelations {
		if !parent.HasCrossTypeLinks {
			continue
		}
		for _, parentType := range dequoteLinkingRelations(parent.CrossTypeLinkingTypes) {
			if canUseSubjectFirstTTU(plan, parent, parentType) {
				add(parentType, parent.Relation)
			}
		}
	}

	if len(order) == 0 {
		return nil, nil
	}

	targets = make(hoistedListObjTargets, len(order))
	ctes = make([]CTEDef, 0, len(order))
	for _, t := range order {
		name := SafeIdentifier("", t.typ, t.rel, "_objs")
		targets[hoistKey(t.typ, t.rel)] = name
		ctes = append(ctes, CTEDef{
			Name: name,
			Query: SelectStmt{
				ColumnExprs: []Expr{Col{Table: "o", Column: "object_id"}},
				FromExpr: FunctionCallExpr{
					Schema: plan.DatabaseSchema,
					Name:   ListObjectsFunctionName(t.typ, t.rel),
					Args:   []Expr{SubjectType, SubjectID, Null{}, Null{}},
					Alias:  "o",
				},
			},
		})
	}
	return targets, ctes
}

// BuildListObjectsRecursiveBlocks builds blocks for a recursive list_objects function.
// This handles TTU patterns with depth tracking and recursive CTEs.
func BuildListObjectsRecursiveBlocks(plan ListPlan) (RecursiveBlockSet, error) {
	parentRelations := buildListParentRelations(plan.Analysis)
	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	selfRefLinkingRelations := dequoteLinkingRelations(selfRefSQL)

	// Compute which relations should propagate through the recursive step.
	// Only relations that satisfy a self-referential TTU target should propagate.
	propagatableRelations := computePropagatableRelations(plan, parentRelations)

	// Hoist list_*_obj calls shared between the userset-subject and cross-type-TTU
	// subject-first arms into shared CTEs so each distinct target is invoked once.
	hoisted, hoistedCTEs := collectHoistedListObjTargets(plan, parentRelations)

	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations, propagatableRelations, hoisted)
	if err != nil {
		return RecursiveBlockSet{}, err
	}

	result := RecursiveBlockSet{
		BaseBlocks:         baseBlocks,
		SelfCandidateBlock: buildListObjectsSelfCandidateBlock(plan),
		HoistedCTEs:        hoistedCTEs,
	}

	if len(selfRefLinkingRelations) > 0 {
		result.RecursiveBlock = buildRecursiveTTUBlock(plan, selfRefLinkingRelations)
	}

	return result, nil
}

// computePropagatableRelations returns the set of relations whose results should
// seed the recursive step. A relation is propagatable if it satisfies any relation
// that has a self-referential TTU pattern. For example, with "can_view: viewer or
// folder_viewer" where viewer has "viewer from parent", the propagatable set is
// {viewer, editor, manager} — relations that satisfy "viewer". folder_viewer is
// excluded because it does not satisfy any TTU-bearing relation.
func computePropagatableRelations(plan ListPlan, parentRelations []ListParentRelationData) map[string]bool {
	result := make(map[string]bool)

	for _, p := range parentRelations {
		if !p.IsSelfReferential {
			continue
		}
		ttuRelation := p.Relation // e.g., "viewer"

		if plan.AnalysisLookup != nil {
			key := plan.ObjectType + "." + ttuRelation
			if relAnalysis, ok := plan.AnalysisLookup[key]; ok {
				for _, sat := range relAnalysis.SatisfyingRelations {
					result[sat] = true
				}
			}
		}

		result[ttuRelation] = true
	}

	return result
}

// buildRecursiveBaseBlocks builds the base case blocks for the recursive CTE.
// Each block is tagged with Propagatable based on whether its source relation
// satisfies a self-referential TTU target.
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData, propagatable map[string]bool, hoisted hoistedListObjTargets) ([]TypedQueryBlock, error) {
	blocks := make([]TypedQueryBlock, 0, 8)

	// Split direct block relations into propagatable and non-propagatable sets.
	// When a simple closure relation (e.g. editor) satisfies a TTU target (e.g. viewer),
	// it shares the direct block with non-propagating relations (e.g. folder_viewer).
	// Emitting separate blocks ensures per-relation propagatability.
	blocks = append(blocks, buildRecursiveDirectBlocks(plan, propagatable)...)

	for _, rel := range plan.ComplexClosure {
		block := buildRecursiveComplexClosureBlock(plan, rel)
		block.Propagatable = propagatable[rel]
		blocks = append(blocks, block)
	}

	for _, rel := range plan.Analysis.IntersectionClosureRelations {
		block := buildRecursiveIntersectionClosureBlock(plan, rel)
		block.Propagatable = propagatable[rel]
		blocks = append(blocks, block)
	}

	patterns := buildListUsersetPatternInputs(plan.Analysis)
	for _, pattern := range patterns {
		blocks = append(blocks, buildRecursiveUsersetBlocks(plan, pattern, propagatable, hoisted)...)
	}

	// Cross-type TTU blocks are a one-hop grant of the outer relation. They must
	// propagate when that relation is itself self-referential (e.g. "local_admin:
	// admin from org or local_admin from parent"): the object granted via the
	// cross-type "admin from org" arm has to seed the recursive parent walk.
	blocks = append(blocks, buildCrossTypeTTUBlocks(plan, parentRelations, propagatable, hoisted)...)

	return blocks, nil
}

// buildRecursiveDirectBlocks builds direct tuple lookup blocks for recursive CTEs.
// When RelationList contains a mix of propagatable and non-propagatable relations,
// two separate blocks are emitted so each gets the correct propagatable tag.
// This prevents non-propagating relations (e.g. folder_viewer) from incorrectly
// seeding the recursive step when a simple propagatable relation (e.g. editor
// satisfying viewer) is in the same relation list.
func buildRecursiveDirectBlocks(plan ListPlan, propagatable map[string]bool) []TypedQueryBlock {
	return splitBlocksByPropagation(plan.RelationList, propagatable, func(rels []string) TypedQueryBlock {
		return buildRecursiveDirectBlock(plan, rels)
	})
}

// buildRecursiveDirectBlock builds a single direct tuple lookup block for the given relations.
func buildRecursiveDirectBlock(plan ListPlan, relations []string) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			In{Expr: SubjectType, Values: plan.AllowedSubjectTypes},
			SubjectIDMatch(Col{Table: "t", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{"-- Direct tuple lookup with simple closure relations"},
		Query:    q.Build(),
	}
}

// buildRecursiveComplexClosureBlock builds a block for a single complex closure relation.
func buildRecursiveComplexClosureBlock(plan ListPlan, rel string) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(rel).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			complexClosureCandidateMatch(plan),
			complexClosureMembership(plan, rel),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Complex closure relation: %s", rel)},
		Query:    q.Build(),
	}
}

// buildRecursiveIntersectionClosureBlock builds a block for a single intersection closure relation.
func buildRecursiveIntersectionClosureBlock(plan ListPlan, rel string) TypedQueryBlock {
	funcName := listObjectsFunctionName(plan.ObjectType, rel)
	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
		FromExpr: FunctionCallExpr{
			Schema: plan.DatabaseSchema,
			Name:   funcName,
			Args:   []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias:  "icr",
		},
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Intersection closure: %s", rel)},
		Query:    stmt,
	}
}

// buildRecursiveComplexUsersetBlock builds a block for complex userset patterns.
// Membership comes from usersetMembership: a semi-join against the userset
// relation's list function when composition is safe, or a per-candidate
// check_permission_internal call otherwise.
func buildRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, hoisted hoistedListObjTargets) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			Eq{
				Left:  UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}},
				Right: Lit(pattern.SubjectRelation),
			},
			recursiveUsersetMembership(plan, pattern, hoisted),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (complex)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    q.Build(),
	}
}

// buildRecursiveSimpleUsersetBlock builds a block for simple userset patterns.
func buildRecursiveSimpleUsersetBlock(plan ListPlan, pattern listUsersetPatternInput) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
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

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Userset: %s#%s (simple)", pattern.SubjectType, pattern.SubjectRelation)},
		Query:    q.Build(),
	}
}

// buildRecursiveUsersetBlocks builds userset pattern blocks, splitting by propagatability
// when source relations contain a mix of propagatable and non-propagatable relations.
func buildRecursiveUsersetBlocks(plan ListPlan, pattern listUsersetPatternInput, propagatable map[string]bool, hoisted hoistedListObjTargets) []TypedQueryBlock {
	return splitBlocksByPropagation(pattern.SourceRelations, propagatable, func(rels []string) TypedQueryBlock {
		p := pattern
		p.SourceRelations = rels
		if p.IsComplex {
			return buildRecursiveComplexUsersetBlock(plan, p, hoisted)
		}
		return buildRecursiveSimpleUsersetBlock(plan, p)
	})
}

// recursiveUsersetMembership is usersetMembership with the composed list_*_obj
// semi-join swapped for a reference to the hoisted CTE when the target has been
// hoisted. Behavior is identical -- the CTE computes the same list_*_obj set
// once instead of inlining the call in this arm (and again in the subject-first
// arm). Falls back to usersetMembership when the target is not hoisted.
func recursiveUsersetMembership(plan ListPlan, pattern listUsersetPatternInput, hoisted hoistedListObjTargets) Expr {
	cteName := hoisted.name(pattern.SubjectType, pattern.SubjectRelation)
	if cteName == "" {
		return usersetMembership(plan, pattern)
	}
	check := CheckPermission{
		Schema:   plan.DatabaseSchema,
		Subject:  SubjectParams(),
		Relation: pattern.SubjectRelation,
		Object: ObjectRef{
			Type: Lit(pattern.SubjectType),
			ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
		},
		ExpectAllow: true,
	}
	return Or(
		InCTESelect{
			Expr:      UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			CTEName:   cteName,
			SelectCol: "object_id",
		},
		And(HasUserset{Source: SubjectID}, check),
	)
}

// splitBlocksByPropagation partitions relations into propagatable and non-propagatable
// sets, building one block per set. When all relations share the same propagatability
// (the common case), a single block is returned. The buildFn receives the relation
// subset and must return a block without Propagatable set.
func splitBlocksByPropagation(relations []string, propagatable map[string]bool, buildFn func([]string) TypedQueryBlock) []TypedQueryBlock {
	var propRels, nonPropRels []string
	for _, rel := range relations {
		if propagatable[rel] {
			propRels = append(propRels, rel)
		} else {
			nonPropRels = append(nonPropRels, rel)
		}
	}

	// Common case: all relations have the same propagatability -- single block.
	if len(propRels) == 0 || len(nonPropRels) == 0 {
		block := buildFn(relations)
		block.Propagatable = len(propRels) > 0
		return []TypedQueryBlock{block}
	}

	// Mixed: emit separate blocks for correct per-relation tagging.
	nonPropBlock := buildFn(nonPropRels)
	nonPropBlock.Propagatable = false

	propBlock := buildFn(propRels)
	propBlock.Propagatable = true

	return []TypedQueryBlock{nonPropBlock, propBlock}
}

// buildCrossTypeTTUBlocks builds blocks for cross-type TTU patterns.
// When the parent relation has a non-recursive list_objects function, it uses a
// subject-first path: list accessible parents for the subject, then join child
// linking tuples. This avoids scanning every child candidate and calling
// check_permission_internal for each one. Recursive parent list functions can
// form cross-type list cycles, so those keep the check_permission_internal
// fallback.
func buildCrossTypeTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData, propagatable map[string]bool, hoisted hoistedListObjTargets) []TypedQueryBlock {
	var blocks []TypedQueryBlock

	for _, parent := range parentRelations {
		if !parent.HasCrossTypeLinks {
			continue
		}

		// The cross-type arm grants the outer relation (plan.Relation), or the
		// source relation it was inherited from for a closure pattern. Propagate
		// the block when that relation seeds a self-referential parent walk.
		grantedRel := plan.Relation
		if parent.SourceRelation != "" {
			grantedRel = parent.SourceRelation
		}
		propagate := propagatable[grantedRel]

		crossTypes := dequoteLinkingRelations(parent.CrossTypeLinkingTypes)
		crossExclusions := buildExclusionInput(
			plan.Analysis,
			plan.DatabaseSchema,
			Col{Table: "child", Column: "object_id"},
			SubjectType,
			SubjectID,
		)

		appendBlock := func(b TypedQueryBlock) {
			b.Propagatable = propagate
			blocks = append(blocks, b)
		}

		var fallbackTypes []string
		for _, parentType := range crossTypes {
			if canUseSubjectFirstTTU(plan, parent, parentType) {
				// Userset-typed subjects (e.g. "group:eng#member") can be
				// provable by the parent's check function through closure arms
				// its list function does not enumerate; keep a per-candidate
				// check arm for exactly that case. The guard is false for
				// plain subjects, so the check never runs on the hot path.
				appendBlock(buildSubjectFirstCrossTypeTTUBlock(plan, parent, parentType, crossExclusions, hoisted))
				appendBlock(buildCheckPermissionCrossTypeTTUBlock(plan, parent, []string{parentType}, crossExclusions, true))
			} else {
				fallbackTypes = append(fallbackTypes, parentType)
			}
		}

		if len(fallbackTypes) > 0 {
			appendBlock(buildCheckPermissionCrossTypeTTUBlock(plan, parent, fallbackTypes, crossExclusions, false))
		}
	}

	return blocks
}

// canUseSubjectFirstTTU decides whether the parent relation's list function
// can be composed with (subject-first path) instead of checking every child
// linking tuple. All strategies route through the same reachability gate:
// list functions carry no p_visited guard, so composition is allowed only
// when the relation-reference graph proves the parent can never call back
// into this function (and cannot reach an always-raising DepthExceeded list
// function). Cyclic schemas keep the per-candidate permission-check fallback.
func canUseSubjectFirstTTU(plan ListPlan, parent ListParentRelationData, parentType string) bool {
	return composableListTarget(plan, parentType, parent.Relation)
}

func buildSubjectFirstCrossTypeTTUBlock(plan ListPlan, parent ListParentRelationData, parentType string, exclusions ExclusionConfig, hoisted hoistedListObjTargets) TypedQueryBlock {
	conditions := exclusions.BuildPredicates()
	if sourceRelationNeedsVerification(plan, parent) {
		conditions = append(conditions, sourceRelationCheck(plan, parent))
	}

	// Reference the hoisted CTE for the parent's list_*_obj set when available,
	// so the same call in the userset-subject arm is not evaluated twice.
	var parentSource TableExpr
	if cteName := hoisted.name(parentType, parent.Relation); cteName != "" {
		parentSource = TableAs("", cteName, "parent_obj")
	} else {
		parentSource = FunctionCallExpr{
			Schema: plan.DatabaseSchema,
			Name:   ListObjectsFunctionName(parentType, parent.Relation),
			Args:   []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias:  "parent_obj",
		}
	}

	stmt := SelectStmt{
		Distinct:    true,
		ColumnExprs: []Expr{Col{Table: "child", Column: "object_id"}},
		FromExpr:    parentSource,
		Joins: []JoinClause{
			{
				Type:  "INNER",
				Table: "melange_tuples",
				Alias: "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "relation"}, Right: Lit(parent.LinkingRelation)},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(parentType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "parent_obj", Column: "object_id"}},
				),
			},
		},
		Where: buildWhereFromPredicates(conditions),
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Cross-type TTU subject-first: %s -> %s.%s", parent.LinkingRelation, parentType, parent.Relation)},
		Query:    stmt,
	}
}

func sourceRelationNeedsVerification(plan ListPlan, parent ListParentRelationData) bool {
	if !parent.IsClosurePattern || parent.SourceRelation == "" {
		return false
	}
	if plan.AnalysisLookup == nil {
		return true
	}

	sourceAnalysis, ok := plan.AnalysisLookup[plan.ObjectType+"."+parent.SourceRelation]
	if !ok {
		return true
	}

	return sourceAnalysis.Features.HasExclusion || sourceAnalysis.Features.HasIntersection
}

// sourceRelationCheck proves the subject holds the closure-pattern TTU's source
// relation on the child object. When composition is safe it replaces the
// per-candidate check_permission_internal with a semi-join against the source
// relation's list_objects set (the set of objects the subject holds SourceRelation
// on), keeping a check arm guarded to userset-typed subjects for parity: the list
// function is complete for plain subjects but a Recursive/Composed target may
// under-report userset subjects ("group:eng#member"), and an under-reported
// positive membership drops objects (under-permissive). For plain subjects the
// guard is false and the arm short-circuits. When composition is unsafe it returns
// the per-candidate check alone. Mirrors complexClosureMembership.
func sourceRelationCheck(plan ListPlan, parent ListParentRelationData) Expr {
	check := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectParams(),
		Relation:    parent.SourceRelation,
		Object:      LiteralObject(plan.ObjectType, Col{Table: "child", Column: "object_id"}),
		ExpectAllow: true,
	}
	if !composableListTarget(plan, plan.ObjectType, parent.SourceRelation) {
		return check
	}
	return composedListObjectsMembership(plan.DatabaseSchema, plan.ObjectType, parent.SourceRelation, Col{Table: "child", Column: "object_id"}, SubjectType, SubjectID, "src_obj", check)
}

// buildCheckPermissionCrossTypeTTUBlock builds the per-candidate check block
// for cross-type TTU. With usersetSubjectsOnly it additionally guards on the
// subject being a userset, serving as the parity companion to a subject-first
// composition block (the guard short-circuits for plain subjects).
func buildCheckPermissionCrossTypeTTUBlock(plan ListPlan, parent ListParentRelationData, crossTypes []string, exclusions ExclusionConfig, usersetSubjectsOnly bool) TypedQueryBlock {
	accessCheck := Expr(CheckPermission{
		Schema:   plan.DatabaseSchema,
		Subject:  SubjectParams(),
		Relation: parent.Relation,
		Object: ObjectRef{
			Type: Col{Table: "child", Column: "subject_type"},
			ID:   Col{Table: "child", Column: "subject_id"},
		},
		ExpectAllow: true,
	})
	if sourceRelationNeedsVerification(plan, parent) {
		accessCheck = sourceRelationCheck(plan, parent)
	}

	q := Tuples(plan.DatabaseSchema, "child").
		ObjectType(plan.ObjectType).
		Relations(parent.LinkingRelation).
		Where(In{Expr: Col{Table: "child", Column: "subject_type"}, Values: crossTypes})

	comment := fmt.Sprintf("-- Cross-type TTU fallback: %s -> %s", parent.LinkingRelation, parent.Relation)
	if usersetSubjectsOnly {
		q.Where(HasUserset{Source: SubjectID})
		comment = fmt.Sprintf("-- Cross-type TTU userset-subject parity: %s -> %s", parent.LinkingRelation, parent.Relation)
	}
	q.Where(accessCheck).
		SelectCol("object_id").
		Distinct()

	for _, pred := range exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments: []string{comment},
		Query:    q.Build(),
	}
}

// buildRecursiveTTUBlock builds the recursive term block for self-referential TTU.
// Filters on a.propagatable to ensure only results from TTU-bearing relations
// seed the recursive step (e.g., folder_viewer results do NOT propagate).
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string) *TypedQueryBlock {
	exclusions := buildExclusionInput(
		plan.Analysis,
		plan.DatabaseSchema,
		Col{Table: "child", Column: "object_id"},
		SubjectType,
		SubjectID,
	)

	stmt := SelectStmt{
		Distinct: true,
		Columns:  []string{"child.object_id", "a.depth + 1 AS depth", "TRUE AS propagatable"},
		From:     "accessible",
		Alias:    "a",
		Joins: []JoinClause{
			{
				Type:   "INNER",
				Schema: "",
				Table:  "melange_tuples",
				Alias:  "child",
				On: And(
					Eq{Left: Col{Table: "child", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					In{Expr: Col{Table: "child", Column: "relation"}, Values: linkingRelations},
					Eq{Left: Col{Table: "child", Column: "subject_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "child", Column: "subject_id"}, Right: Col{Table: "a", Column: "object_id"}},
				),
			},
		},
		Where: And(
			Col{Table: "a", Column: "propagatable"},
			Lt{Left: Col{Table: "a", Column: "depth"}, Right: Int(25)},
		),
	}

	if predicates := exclusions.BuildPredicates(); len(predicates) > 0 {
		stmt.Where = And(append([]Expr{stmt.Where}, predicates...)...)
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-referential TTU: follow linking relations to accessible parents"},
		Query:    stmt,
	}
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
