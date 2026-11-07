package sqlgen

import (
	"fmt"
	"slices"
	"strings"
)

// RecursiveBlockSet contains blocks for a recursive list function.
type RecursiveBlockSet struct {
	BaseBlocks         []TypedQueryBlock
	RecursiveBlock     *TypedQueryBlock
	SelfCandidateBlock *TypedQueryBlock
	// VerifyWithCheck filters the final objects through check_permission. Set
	// when the walk follows more than one linking relation: every propagating
	// row follows every linking relation, so the walk is a superset there.
	VerifyWithCheck bool
	// WalkRelations are the relations that recurse through the linking
	// relations ("v" for "v from parent"), set with VerifyWithCheck. An object
	// passes the walk on only if the subject holds one of them on it.
	WalkRelations []string
	// HoistedCTEs are shared list_*_obj computations lifted out of the base
	// blocks. The same list_<parent>_obj(p_subject_type, p_subject_id, NULL,
	// NULL) call was previously inlined in both the userset-subject arm and the
	// cross-type-TTU subject-first arm; these CTEs compute each distinct call
	// once and the arms reference them. Rendered ahead of the base blocks.
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

// BuildListObjectsRecursiveBlocks builds blocks for a recursive list_objects function:
// the seed blocks and, for self-referential TTU, the step of the breadth-first walk.
func BuildListObjectsRecursiveBlocks(plan ListPlan) (RecursiveBlockSet, error) {
	parentRelations := buildListParentRelations(plan.Analysis)

	// gateWalk: the relation recurses on itself, so its exclusion decides
	// whether a node passes the relation to its children (see the exclusion
	// note below). An implied relation's own recursion ("v from parent" under
	// "r: (v or r from parent) but not b") must not be gated by r's exclusion,
	// and one CTE cannot carry both rules, so such relations are listed whole
	// from their own list function and only seed r's walk.
	gateWalk := slices.ContainsFunc(parentRelations, func(p ListParentRelationData) bool {
		return p.IsSelfReferential && !p.IsClosurePattern
	})
	var seedWhole []string
	if gateWalk {
		plan, seedWhole = splitClosureWalks(plan, parentRelations)
		parentRelations = buildListParentRelations(plan.Analysis)
	}

	selfRefSQL := buildSelfReferentialLinkingRelations(parentRelations)
	selfRefLinkingRelations := dequoteLinkingRelations(selfRefSQL)

	// Without a self-referential walk no block seeds propagation, so closure
	// relations can be listed whole and the arms they cover dropped.
	var compose []string
	if len(selfRefLinkingRelations) == 0 {
		plan, compose = coverListObjectsClosure(plan, parentRelations)
		parentRelations = buildListParentRelations(plan.Analysis)
	}

	// Compute which relations should propagate through the recursive step.
	// Only relations that satisfy a self-referential TTU target should propagate.
	propagatableRelations := computePropagatableRelations(plan, parentRelations)

	// Hoist list_*_obj calls shared between the userset-subject and cross-type-TTU
	// subject-first arms into shared CTEs so each distinct target is invoked once.
	hoisted, hoistedCTEs := collectHoistedListObjTargets(plan, parentRelations)

	// The recursive renderer always emits an outer NOT-EXISTS(excluded) filter over
	// the combined `accessible` set (list_objects_render_recursive.go). For a
	// NON-recursive composed plan (no self-referential linking relation, so no
	// RecursiveBlock) that outer filter is authoritative on the final object, and
	// OpenFGA exclusion applies to the final object — so the per-arm copies on each
	// UNION arm are pure redundancy. Strip them. When the relation recurses on
	// ITSELF ("r: ([user] or r from parent) but not b") the per-step exclusion
	// gates whether a node propagates to its descendants (the recursive block
	// builds its own exclusion fresh), so per-arm exclusion is load-bearing there
	// and must be kept. When the walk is only an implied relation's recursion
	// ("r: v but not b", "v: [user] or v from parent") the exclusion is r's, not
	// v's: an excluded ancestor still passes v down, so only the outer filter
	// applies.
	if !gateWalk {
		plan.Exclusions = ExclusionConfig{}
	}

	baseBlocks, err := buildRecursiveBaseBlocks(plan, parentRelations, propagatableRelations, hoisted, gateWalk)
	if err != nil {
		return RecursiveBlockSet{}, err
	}
	for _, rel := range compose {
		block := buildRecursiveComposedClosureBlock(plan, rel)
		block.Comments = []string{fmt.Sprintf("-- Closure relation %s: compose its list_objects", rel)}
		baseBlocks = append(baseBlocks, block)
	}
	for _, rel := range seedWhole {
		block := buildRecursiveComposedClosureBlock(plan, rel)
		block.Comments = []string{fmt.Sprintf("-- Closure relation %s recurses on its own: list it whole, gate only the seed", rel)}
		block.Propagatable = propagatableRelations[rel]
		baseBlocks = append(baseBlocks, block)
	}

	result := RecursiveBlockSet{
		BaseBlocks:         baseBlocks,
		SelfCandidateBlock: buildListObjectsSelfCandidateBlock(plan),
		HoistedCTEs:        hoistedCTEs,
	}

	if len(selfRefLinkingRelations) > 0 {
		result.RecursiveBlock = buildRecursiveTTUBlock(plan, selfRefLinkingRelations, gateWalk)
	}
	// ponytail: "r1 from owner" rows also walk parent edges and vice versa.
	// Walking extra edges only adds rows, so a final check keeps it exact at
	// one check per candidate. Upgrade path if this shape gets hot: tag each
	// accessible row with the linking relations it may continue through.
	result.VerifyWithCheck = len(selfRefLinkingRelations) > 1
	if result.VerifyWithCheck {
		for _, p := range parentRelations {
			if p.IsSelfReferential && !slices.Contains(result.WalkRelations, p.Relation) {
				result.WalkRelations = append(result.WalkRelations, p.Relation)
			}
		}
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
func buildRecursiveBaseBlocks(plan ListPlan, parentRelations []ListParentRelationData, propagatable map[string]bool, hoisted hoistedListObjTargets, recursive bool) ([]TypedQueryBlock, error) {
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

	for _, rel := range composedClosureRelations(plan.Analysis) {
		block := buildRecursiveComposedClosureBlock(plan, rel)
		block.Propagatable = propagatable[rel]
		blocks = append(blocks, block)
	}

	patterns := buildListUsersetPatternInputs(plan.Analysis)
	for _, pattern := range patterns {
		blocks = append(blocks, indirect(buildRecursiveUsersetBlocks(plan, pattern, propagatable, hoisted))...)
	}

	// Cross-type TTU blocks are a one-hop grant of the outer relation. They must
	// propagate when that relation is itself self-referential (e.g. "local_admin:
	// admin from org or local_admin from parent"): the object granted via the
	// cross-type "admin from org" arm has to seed the recursive parent walk.
	blocks = append(blocks, indirect(buildCrossTypeTTUBlocks(plan, parentRelations, propagatable, hoisted, recursive))...)

	if seed := buildRecursiveSelfCandidateSeedBlock(plan, propagatable); seed != nil {
		blocks = append(blocks, *seed)
	}

	return blocks, nil
}

// indirect marks blocks as Indirect.
func indirect(blocks []TypedQueryBlock) []TypedQueryBlock {
	for i := range blocks {
		blocks[i].Indirect = true
	}
	return blocks
}

// buildRecursiveSelfCandidateSeedBlock seeds the recursive walk with the
// self-candidate (folder:root for subject folder:root#viewer) so its descendants
// are found too. Only userset relations that satisfy a self-referential TTU
// target may propagate; other self-candidates are added outside the CTE.
func buildRecursiveSelfCandidateSeedBlock(plan ListPlan, propagatable map[string]bool) *TypedQueryBlock {
	var rels []string
	for _, rel := range plan.Analysis.SatisfyingRelations {
		if propagatable[rel] {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil
	}
	return &TypedQueryBlock{
		Comments: []string{"-- Self-candidate seed: userset subject on a propagating relation"},
		Query: SelectStmt{
			ColumnExprs: []Expr{Alias{Expr: UsersetObjectID{Source: SubjectID}, Name: "object_id"}},
			Where: And(
				HasUserset{Source: SubjectID},
				Eq{Left: SubjectType, Right: Lit(plan.ObjectType)},
				In{Expr: UsersetRelation{Source: SubjectID}, Values: rels},
			),
		},
		Propagatable:       true,
		UsersetSubjectOnly: true,
	}
}

// buildRecursiveDirectBlocks builds direct tuple lookup blocks for recursive CTEs.
// When RelationList contains a mix of propagatable and non-propagatable relations,
// two separate blocks are emitted so each gets the correct propagatable tag.
// This prevents non-propagating relations (e.g. folder_viewer) from incorrectly
// seeding the recursive step when a simple propagatable relation (e.g. editor
// satisfying viewer) is in the same relation list.
func buildRecursiveDirectBlocks(plan ListPlan, propagatable map[string]bool) []TypedQueryBlock {
	blocks := splitBlocksByPropagation(plan.RelationList, propagatable, func(rels []string) TypedQueryBlock {
		return buildRecursiveDirectBlock(plan, rels)
	})
	// A userset query subject (group:g#member) named directly on a tuple is a
	// candidate too. Kept in its own block, as in the standard strategy, so the
	// plain-subject block stays a simple indexable lookup.
	if !plan.HasUsersetSubject {
		return blocks
	}
	return append(blocks, splitBlocksByPropagation(plan.RelationList, propagatable, func(rels []string) TypedQueryBlock {
		return buildRecursiveUsersetSubjectBlock(plan, rels)
	})...)
}

func buildRecursiveUsersetSubjectBlock(plan ListPlan, relations []string) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(relations...).
		Where(
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			usersetSubjectCandidateMatch(plan),
		).
		SelectCol("object_id").
		Distinct()

	for _, pred := range plan.Exclusions.BuildPredicates() {
		q.Where(pred)
	}

	return TypedQueryBlock{
		Comments:           []string{"-- Userset subject matching: subject IS a userset (e.g., group:fga#member)"},
		Query:              q.Build(),
		UsersetSubjectOnly: true,
	}
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

// buildRecursiveComposedClosureBlock builds a block for a single composed closure relation.
func buildRecursiveComposedClosureBlock(plan ListPlan, rel string) TypedQueryBlock {
	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "icr", Column: "object_id"}},
		FromExpr:    accessibleObjectsCall(plan.DatabaseSchema, plan.ObjectType, rel, "icr"),
	}
	// plan.Exclusions is non-empty only when the walk is gated: an excluded
	// object must not seed propagation from this arm any more than from the
	// others.
	if len(plan.Exclusions.BuildPredicates()) > 0 {
		stmt.Where = And(buildExclusionInput(plan.Analysis, plan.DatabaseSchema, Col{Table: "icr", Column: "object_id"}, SubjectType, SubjectID).BuildPredicates()...)
	}

	return TypedQueryBlock{
		Comments: []string{fmt.Sprintf("-- Composed closure: %s", rel)},
		Query:    stmt,
	}
}

// buildRecursiveComplexUsersetBlock builds a block for complex userset patterns,
// with membership the predicate that the tuple's userset subject holds the
// pattern relation for the query subject.
func buildRecursiveComplexUsersetBlock(plan ListPlan, pattern listUsersetPatternInput, membership ...Expr) TypedQueryBlock {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(pattern.SourceRelations...).
		Where(Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: Lit(pattern.SubjectType)}).
		Where(membership...).
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
	split := func(build func(listUsersetPatternInput) TypedQueryBlock) []TypedQueryBlock {
		return splitBlocksByPropagation(pattern.SourceRelations, propagatable, func(rels []string) TypedQueryBlock {
			p := pattern
			p.SourceRelations = rels
			return build(p)
		})
	}
	if !pattern.IsComplex {
		return split(func(p listUsersetPatternInput) TypedQueryBlock { return buildRecursiveSimpleUsersetBlock(plan, p) })
	}
	var blocks []TypedQueryBlock
	for _, arm := range complexUsersetArms(plan, pattern, hoisted) {
		blocks = append(blocks, split(func(p listUsersetPatternInput) TypedQueryBlock {
			b := buildRecursiveComplexUsersetBlock(plan, p, arm.membership...)
			b.UsersetSubjectOnly = arm.usersetSubjectOnly
			return b
		})...)
	}
	return blocks
}

// complexUsersetArm is the membership predicate of one arm of a complex
// userset pattern over tuple t.
type complexUsersetArm struct {
	membership         []Expr
	usersetSubjectOnly bool
}

// complexUsersetArms returns the arms proving that tuple t's userset subject
// (group:g#member) holds the pattern relation for the query subject. When the
// usersets the subject is a member of can be listed (usersetMemberSource),
// they are matched as tuple subject IDs, so the lookup is an index probe per
// userset rather than a scan of every userset tuple; the list function can
// under-report userset query subjects (group:eng#member), so those also check
// each candidate, in an arm plain subjects skip. Otherwise one arm checks each
// candidate.
func complexUsersetArms(plan ListPlan, pattern listUsersetPatternInput, hoisted hoistedListObjTargets) []complexUsersetArm {
	check := usersetMembershipCheck(plan, pattern)
	members := usersetMemberSource(plan, pattern, hoisted)
	if members == nil {
		return []complexUsersetArm{{membership: append(usersetSubjectOfRelation(pattern), check)}}
	}
	return []complexUsersetArm{
		// An object ID holding '#' would name another userset (g#member||'#member'
		// matches the subject g#member#member, whose object is g), so skip it.
		{membership: []Expr{Raw(fmt.Sprintf("t.subject_id IN (SELECT m.object_id || %s FROM %s WHERE position('#' in m.object_id) = 0)",
			Lit("#"+pattern.SubjectRelation).SQL(), members.TableSQL()))}},
		{membership: append(usersetSubjectOfRelation(pattern), HasUserset{Source: SubjectID}, check), usersetSubjectOnly: true},
	}
}

// usersetSubjectOfRelation restricts tuple t to userset subjects of the
// pattern relation (group:g#member).
func usersetSubjectOfRelation(pattern listUsersetPatternInput) []Expr {
	return []Expr{
		HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
		Eq{Left: UsersetRelation{Source: Col{Table: "t", Column: "subject_id"}}, Right: Lit(pattern.SubjectRelation)},
	}
}

// usersetMembershipCheck checks that the query subject holds the pattern
// relation on tuple t's userset object.
func usersetMembershipCheck(plan ListPlan, pattern listUsersetPatternInput) Expr {
	return CheckPermission{
		Schema:   plan.DatabaseSchema,
		Subject:  SubjectParams(),
		Relation: pattern.SubjectRelation,
		Object: ObjectRef{
			Type: Lit(pattern.SubjectType),
			ID:   UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
		},
		ExpectAllow: true,
	}
}

// usersetMemberSource returns the objects (as m.object_id) on which the query
// subject holds the pattern relation: the hoisted CTE, or the relation's list
// function when composition is safe; nil when neither applies.
func usersetMemberSource(plan ListPlan, pattern listUsersetPatternInput, hoisted hoistedListObjTargets) TableExpr {
	if cteName := hoisted.name(pattern.SubjectType, pattern.SubjectRelation); cteName != "" {
		return TableAs("", cteName, "m")
	}
	if !composableListTarget(plan, pattern.SubjectType, pattern.SubjectRelation) {
		return nil
	}
	return FunctionCallExpr{
		Schema: plan.DatabaseSchema,
		Name:   ListObjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation),
		Args:   []Expr{SubjectType, SubjectID, Null{}, Null{}},
		Alias:  "m",
	}
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
func buildCrossTypeTTUBlocks(plan ListPlan, parentRelations []ListParentRelationData, propagatable map[string]bool, hoisted hoistedListObjTargets, recursive bool) []TypedQueryBlock {
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
		// Non-recursive plans get the authoritative outer exclusion filter over the
		// combined set; per-arm exclusion here is redundant (see
		// BuildListObjectsRecursiveBlocks). Recursive plans keep it: an excluded
		// intermediate must not seed the parent walk.
		var crossExclusions ExclusionConfig
		if recursive {
			crossExclusions = buildExclusionInput(
				plan.Analysis,
				plan.DatabaseSchema,
				Col{Table: "child", Column: "object_id"},
				SubjectType,
				SubjectID,
			)
		}

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

// buildRecursiveTTUBlock builds one step of the breadth-first walk: the objects
// linked by a self-referential relation to an object in the frontier. Only
// propagating objects enter the frontier (e.g., folder_viewer results do NOT
// propagate), so the step itself needs no propagation filter.
func buildRecursiveTTUBlock(plan ListPlan, linkingRelations []string, gateWalk bool) *TypedQueryBlock {
	exclusions := ExclusionConfig{}
	if gateWalk {
		exclusions = buildExclusionInput(
			plan.Analysis,
			plan.DatabaseSchema,
			Col{Table: "child", Column: "object_id"},
			SubjectType,
			SubjectID,
		)
	}

	stmt := SelectStmt{
		ColumnExprs: []Expr{Col{Table: "child", Column: "object_id"}},
		FromExpr:    unnestObjectIDs(walkFrontier, "a"),
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
	}

	if predicates := exclusions.BuildPredicates(); len(predicates) > 0 {
		stmt.Where = And(predicates...)
	}

	return &TypedQueryBlock{
		Comments: []string{"-- Self-referential TTU: follow linking relations from the frontier"},
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
		trimmed := strings.Trim(strings.TrimSpace(part), "'")
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// coverListObjectsClosure is closureCoverage for a non-recursive list_objects
// plan. It returns the plan with every closure-derived arm of a covered
// relation removed (complex and composed closure relations, closure userset
// patterns, closure parent lookups) and the relations to compose instead.
// can_view (`viewer but not blocked`) then lists viewer once rather than
// re-deriving editor and viewer arm by arm and validating each through the
// full list function.
func coverListObjectsClosure(plan ListPlan, parents []ListParentRelationData) (covered ListPlan, compose []string) {
	a := plan.Analysis
	candidates := slices.Concat(plan.ComplexClosure, composedClosureRelations(a))
	for _, p := range parents {
		if p.IsClosurePattern {
			candidates = append(candidates, p.SourceRelation)
		}
	}
	for _, p := range a.ClosureUsersetPatterns {
		candidates = append(candidates, p.SourceRelation)
	}
	compose, isComposedOrCovered := closureCoverage(plan, candidates, func(r string) bool {
		return composableListTarget(plan, plan.ObjectType, r)
	})
	if len(compose) == 0 {
		return plan, nil
	}
	isCovered := func(r string) bool { return isComposedOrCovered[r] }
	a.ClosureUsersetPatterns = slices.DeleteFunc(slices.Clone(a.ClosureUsersetPatterns), func(p UsersetPattern) bool { return isCovered(p.SourceRelation) })
	a.ClosureParentRelations = slices.DeleteFunc(slices.Clone(a.ClosureParentRelations), func(p ParentRelationInfo) bool { return isCovered(p.SourceRelation) })
	a.IntersectionClosureRelations = slices.DeleteFunc(slices.Clone(a.IntersectionClosureRelations), isCovered)
	a.ExclusionClosureRelations = slices.DeleteFunc(slices.Clone(a.ExclusionClosureRelations), isCovered)
	plan.Analysis = a
	plan.ComplexClosure = slices.DeleteFunc(slices.Clone(plan.ComplexClosure), isCovered)
	return plan, compose
}

// splitClosureWalks removes the self-referential closure TTU patterns whose
// source relation can be listed whole (composableListTarget) and returns
// those source relations. Patterns that cannot be composed stay in the walk.
// ponytail: those keep the gated walk and can under-report descendants of an
// excluded ancestor; route them through check_permission_internal if a
// cyclic schema needs it.
func splitClosureWalks(plan ListPlan, parents []ListParentRelationData) (split ListPlan, whole []string) {
	for _, p := range parents {
		if p.IsSelfReferential && p.IsClosurePattern && !slices.Contains(whole, p.SourceRelation) &&
			composableListTarget(plan, plan.ObjectType, p.SourceRelation) {
			whole = append(whole, p.SourceRelation)
		}
	}
	if len(whole) == 0 {
		return plan, nil
	}
	a := plan.Analysis
	a.ClosureParentRelations = slices.DeleteFunc(slices.Clone(a.ClosureParentRelations), func(p ParentRelationInfo) bool {
		return slices.Contains(whole, p.SourceRelation) && p.Relation == p.SourceRelation
	})
	plan.Analysis = a
	return plan, whole
}
