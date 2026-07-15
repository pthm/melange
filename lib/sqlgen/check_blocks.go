package sqlgen

import "sort"

// CheckBlocks contains all the DSL blocks needed to generate a check function.
type CheckBlocks struct {
	DirectCheck             Expr       // EXISTS check for direct/implied tuple lookup
	UsersetCheck            Expr       // EXISTS check for userset membership
	ExclusionCheck          Expr       // EXISTS check for exclusion (denial)
	UsersetSubjectSelfCheck SelectStmt // Validates when subject IS a userset

	// UsersetSubjectComputedCheck validates userset subject via tuple join
	UsersetSubjectComputedCheck SelectStmt

	ParentRelationBlocks []ParentRelationBlock    // TTU pattern checks
	ImpliedFunctionCalls []ImpliedFunctionCheck   // Complex implied relation checks
	IntersectionGroups   []IntersectionGroupCheck // AND groups for intersection patterns
	HasStandaloneAccess  bool                     // Access paths outside intersections exist
}

// ParentRelationBlock represents a TTU check through a parent relation.
type ParentRelationBlock struct {
	LinkingRelation     string
	ParentRelation      string
	AllowedLinkingTypes []string
	Query               SelectStmt
}

// ImpliedFunctionCheck represents a check that calls another check function.
type ImpliedFunctionCheck struct {
	Relation     string
	FunctionName string
	Check        Expr
}

// IntersectionGroupCheck represents an AND group where all checks must pass.
type IntersectionGroupCheck struct {
	Parts []IntersectionPartCheck
}

// IntersectionPartCheck represents one part of an intersection check.
type IntersectionPartCheck struct {
	Relation         string
	ExcludedRelation string
	IsThis           bool // [user]-direct grant probe at the wrapping relation
	IsParent         bool
	ParentRelation   string
	LinkingRelation  string
	Check            Expr
}

// BuildCheckBlocks builds all DSL blocks for a check function.
func BuildCheckBlocks(plan CheckPlan) (CheckBlocks, error) {
	blocks := CheckBlocks{
		HasStandaloneAccess: plan.HasStandaloneAccess,
	}

	if plan.HasDirect || plan.HasImplied {
		blocks.DirectCheck = buildDirectCheck(plan)
	}
	if plan.HasUserset {
		blocks.UsersetCheck = buildUsersetCheck(plan)
	}
	if plan.HasExclusion {
		blocks.ExclusionCheck = buildExclusionCheck(plan)
	}

	blocks.UsersetSubjectSelfCheck, blocks.UsersetSubjectComputedCheck = buildUsersetSubjectChecks(plan)

	if plan.HasParentRelations {
		blocks.ParentRelationBlocks = buildParentRelationBlocks(plan)
	}
	if plan.HasImpliedFunctionCall {
		blocks.ImpliedFunctionCalls = buildImpliedFunctionCalls(plan)
	}
	if plan.HasIntersection {
		blocks.IntersectionGroups = buildIntersectionGroups(plan)
	}

	return blocks, nil
}

func buildDirectCheck(plan CheckPlan) Expr {
	q := Tuples(plan.DatabaseSchema, "").
		ObjectType(plan.ObjectType).
		Relations(plan.RelationList...).
		Where(
			Eq{Left: Col{Column: "object_id"}, Right: ObjectID},
			In{Expr: Col{Column: "subject_type"}, Values: plan.AllowedSubjectTypes},
			Eq{Left: Col{Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Select("1")

	// No LIMIT 1: this query is wrapped in EXISTS, which short-circuits on the
	// first row. ponytail: same for the other Exists-wrapped probes below.
	return Exists{Query: q}
}

func buildUsersetCheck(plan CheckPlan) Expr {
	patterns := plan.Analysis.UsersetPatterns
	if len(patterns) == 0 {
		return nil
	}

	// Order simple patterns (tuple JOIN) before complex ones (recursive function call)
	// so the OR short-circuits on the cheaper check first.
	sorted := make([]UsersetPattern, len(patterns))
	copy(sorted, patterns)
	sort.SliceStable(sorted, func(i, j int) bool {
		return !sorted[i].IsComplex && sorted[j].IsComplex
	})

	visitedWithKey := VisitedWithKey(plan.ObjectType, plan.Relation, ObjectID)
	checks := make([]Expr, 0, len(sorted))

	for _, pattern := range sorted {
		checks = append(checks, buildUsersetPatternCheck(plan, pattern, visitedWithKey))
	}

	if len(checks) == 1 {
		return checks[0]
	}
	return Or(checks...)
}

func buildUsersetPatternCheck(plan CheckPlan, pattern UsersetPattern, visitedWithKey Expr) Expr {
	grantTuple := Col{Table: "grant_tuple", Column: "subject_id"}

	baseWhere := []Expr{
		Eq{Left: Col{Table: "grant_tuple", Column: "object_id"}, Right: ObjectID},
		Eq{Left: Col{Table: "grant_tuple", Column: "subject_type"}, Right: Lit(pattern.SubjectType)},
		HasUserset{Source: grantTuple},
		Eq{Left: UsersetRelation{Source: grantTuple}, Right: Lit(pattern.SubjectRelation)},
	}

	if pattern.IsComplex {
		// Complex pattern: use check_permission_internal for recursive membership verification
		q := Tuples(plan.DatabaseSchema, "grant_tuple").
			ObjectType(plan.ObjectType).
			Relations(plan.Relation).
			Where(append(baseWhere, CheckPermission{
				Schema:   plan.DatabaseSchema,
				Subject:  SubjectParams(),
				Relation: pattern.SubjectRelation,
				Object: ObjectRef{
					Type: Lit(pattern.SubjectType),
					ID:   UsersetObjectID{Source: grantTuple},
				},
				Visited:     visitedWithKey,
				ExpectAllow: true,
			})...).
			Select("1")
		return Exists{Query: q}
	}

	// Simple pattern: use tuple JOIN for membership lookup
	q := Tuples(plan.DatabaseSchema, "grant_tuple").
		ObjectType(plan.ObjectType).
		Relations(plan.Relation).
		Where(baseWhere...).
		JoinTuples("membership",
			Eq{Left: Col{Table: "membership", Column: "object_type"}, Right: Lit(pattern.SubjectType)},
			Eq{Left: Col{Table: "membership", Column: "object_id"}, Right: UsersetObjectID{Source: grantTuple}},
			In{Expr: Col{Table: "membership", Column: "relation"}, Values: pattern.SatisfyingRelations},
			Eq{Left: Col{Table: "membership", Column: "subject_type"}, Right: SubjectType},
			SubjectIDMatch(Col{Table: "membership", Column: "subject_id"}, SubjectID, plan.AllowWildcard),
		).
		Select("1")
	return Exists{Query: q}
}

func buildExclusionCheck(plan CheckPlan) Expr {
	if !plan.Exclusions.HasExclusions() {
		return nil
	}

	var checks []Expr
	obj := LiteralObject(plan.ObjectType, ObjectID)

	// Simple exclusions: EXISTS (excluded tuple)
	for _, rel := range plan.Exclusions.SimpleExcludedRelations {
		q := Tuples(plan.DatabaseSchema, "excl").
			ObjectType(plan.ObjectType).
			Relations(rel).
			Where(
				Eq{Left: Col{Table: "excl", Column: "object_id"}, Right: ObjectID},
				Eq{Left: Col{Table: "excl", Column: "subject_type"}, Right: SubjectType},
				Or(
					Eq{Left: Col{Table: "excl", Column: "subject_id"}, Right: SubjectID},
					IsWildcard{Source: Col{Table: "excl", Column: "subject_id"}},
				),
			).
			Select("1")
		checks = append(checks, Exists{Query: q})
	}

	// Complex exclusions: check_permission_internal
	for _, rel := range plan.Exclusions.ComplexExcludedRelations {
		checks = append(checks, checkPermissionAllow(plan.DatabaseSchema, rel, obj))
	}

	// TTU exclusions
	for _, rel := range plan.Exclusions.ExcludedParentRelations {
		checks = append(checks, buildTTUExclusionCheck(plan, rel))
	}

	// Intersection exclusions
	for _, group := range plan.Exclusions.ExcludedIntersection {
		if expr := buildIntersectionExclusionCheck(plan, group); expr != nil {
			checks = append(checks, expr)
		}
	}

	return orExprs(checks)
}

func buildTTUExclusionCheck(plan CheckPlan, rel ExcludedParentRelation) Expr {
	linkQuery := Tuples(plan.DatabaseSchema, "link").
		ObjectType(plan.ObjectType).
		Relations(rel.LinkingRelation).
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			CheckPermission{
				Schema:   plan.DatabaseSchema,
				Subject:  SubjectParams(),
				Relation: rel.Relation,
				Object: ObjectRef{
					Type: Col{Table: "link", Column: "subject_type"},
					ID:   Col{Table: "link", Column: "subject_id"},
				},
				Visited:     Visited,
				ExpectAllow: true,
			},
		).
		Select("1")

	if len(rel.AllowedLinkingTypes) > 0 {
		linkQuery.WhereSubjectTypeIn(rel.AllowedLinkingTypes...)
	}
	return Exists{Query: linkQuery}
}

func buildIntersectionExclusionCheck(plan CheckPlan, group ExcludedIntersectionGroup) Expr {
	obj := LiteralObject(plan.ObjectType, ObjectID)
	parts := make([]Expr, 0, len(group.Parts))

	for _, part := range group.Parts {
		switch {
		case part.ParentRelation != nil:
			parts = append(parts, buildTTUExclusionCheck(plan, *part.ParentRelation))
		case part.ExcludedRelation != "":
			parts = append(parts, And(
				checkPermissionAllow(plan.DatabaseSchema, part.Relation, obj),
				checkPermissionDeny(plan.DatabaseSchema, part.ExcludedRelation, obj),
			))
		default:
			parts = append(parts, checkPermissionAllow(plan.DatabaseSchema, part.Relation, obj))
		}
	}

	if len(parts) == 0 {
		return nil
	}
	return And(parts...)
}

func checkPermissionAllow(databaseSchema, relation string, obj ObjectRef) CheckPermission {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      obj,
		Visited:     Visited,
		ExpectAllow: true,
	}
}

func checkPermissionDeny(databaseSchema, relation string, obj ObjectRef) CheckPermission {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      obj,
		Visited:     Visited,
		ExpectAllow: false,
	}
}

func orExprs(exprs []Expr) Expr {
	switch len(exprs) {
	case 0:
		return nil
	case 1:
		return exprs[0]
	default:
		return Or(exprs...)
	}
}

// usersetSelfSatisfyingExpr folds the userset self-check closure lookup — "is
// the subject a userset on THIS object type whose relation satisfies
// a.Relation?" — to a compile-time IN-list. object_type and relation are
// constants (a.ObjectType, a.Relation), so the closure's satisfying_relation
// set for that (type, relation) is statically known: a.SatisfyingRelations
// (populated directly from the same closure rows in analyzeRelation). This
// replaces a VALUES scan over the whole model's closure with
// split_part(subject_id, '#', 2) IN ('rel', ...), so the emitted SQL no longer
// embeds the closure table and its size is independent of unrelated relations.
// Empty SatisfyingRelations → `... IN ()` renders FALSE, matching the VALUES
// scan (which also never matches when the (type, relation) has no rows).
//
// Shared by the check self-check (buildUsersetSubjectChecks) and the
// list_objects self-candidate blocks (buildListObjectsSelfCandidateBlock,
// buildComposedObjectsSelfBlock): all three key the same constant (object_type,
// relation) on split_part(subject_id, '#', 2) against the same closure rows, so
// they fold to the identical IN-list.
func usersetSelfSatisfyingExpr(a RelationAnalysis) Expr {
	return In{Expr: UsersetRelation{Source: SubjectID}, Values: a.SatisfyingRelations}
}

// usersetSubjClosureRows narrows the Case-2 subj_c closure VALUES (Finding 1.2)
// to the statically-compatible rows. The subj_c join keys three columns against
// the subject-side userset, whose type/relation the `m` join pins to a live
// userset pattern:
//   - object_type = t.subject_type = a pattern's SubjectType;
//   - satisfying_relation = the requested userset relation split_part(p_subject_id,
//     '#', 2), which is a pattern SubjectRelation (i.e. in its SatisfyingRelations,
//     which include the anchor);
//   - relation = the tuple's stored userset relation, which must closure-satisfy
//     that — also within the pattern's SatisfyingRelations.
//
// Both relation columns therefore range over the same per-type pattern relation
// set (NOT the outer relation's plan.SatisfyingRelations — that set names the
// document-side relations, not the group-side userset relations). A closure row
// whose type or either relation is outside these static sets can never satisfy
// the join, so dropping it is behavior-preserving while making the embedded
// VALUES independent of unrelated closure growth. Patterns come from both own
// UsersetPatterns and ClosureUsersetPatterns (the two sources that keep Case 2
// live per Finding 1.1).
func usersetSubjClosureRows(plan CheckPlan) []ValuesRow {
	// relationsByType[subjectType] holds the userset relations reachable for that
	// subject type; a closure row's relation AND satisfying_relation must both be
	// in the set keyed by its object_type.
	relationsByType := map[string]map[string]bool{}
	addPattern := func(p UsersetPattern) {
		set := relationsByType[p.SubjectType]
		if set == nil {
			set = map[string]bool{}
			relationsByType[p.SubjectType] = set
		}
		for _, rel := range p.SatisfyingRelations {
			set[rel] = true
		}
	}
	for _, p := range plan.Analysis.UsersetPatterns {
		addPattern(p)
	}
	for _, p := range plan.Analysis.ClosureUsersetPatterns {
		addPattern(p)
	}

	out := make([]ValuesRow, 0, len(plan.Inline.ClosureRows))
	for _, r := range plan.Inline.ClosureRows {
		// Keep rows whose columns aren't plain Lits (defensive, matches
		// filterRowsByObjectType) so a future row shape is never silently dropped.
		if closureRowCompatible(r, relationsByType) {
			out = append(out, r)
		}
	}
	return out
}

// closureRowCompatible reports whether a closure VALUES row (object_type,
// relation, satisfying_relation) can survive the subj_c join: its object_type
// must be a userset subject type, and both its relation columns must be in that
// type's reachable userset-relation set. Non-Lit columns are treated as
// compatible (kept).
func closureRowCompatible(r ValuesRow, relationsByType map[string]map[string]bool) bool {
	lit := func(i int) (string, bool) {
		if i >= len(r) {
			return "", false
		}
		l, ok := r[i].(Lit)
		return string(l), ok
	}
	objType, ok := lit(0)
	if !ok {
		return true // non-Lit object_type: keep defensively
	}
	relSet, ok := relationsByType[objType]
	if !ok {
		return false // object_type is not a userset subject type
	}
	inRelSet := func(i int) bool {
		v, ok := lit(i)
		return !ok || relSet[v]
	}
	return inRelSet(1) && inRelSet(2)
}

func buildUsersetSubjectChecks(plan CheckPlan) (selfCheck, computedCheck SelectStmt) {
	selfCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		Where:       usersetSelfSatisfyingExpr(plan.Analysis),
		Limit:       1,
	}

	// The `c` closure join only kept tuples whose relation satisfies plan.Relation
	// (c.object_type/relation are compile-time constants, c.satisfying_relation =
	// t.relation) and fed c.satisfying_relation to the m join. That set is exactly
	// plan.Analysis.SatisfyingRelations, so test t.relation against it directly and
	// drop the `c` closure VALUES entirely — leaving only the subject-side subj_c.
	computedCheck = SelectStmt{
		ColumnExprs: []Expr{Int(1)},
		FromExpr:    TableAs("", "melange_tuples", "t"),
		Joins: []JoinClause{
			{
				Type:      "INNER",
				TableExpr: UsersetTable(plan.Inline.UsersetRows, "m"),
				On: And(
					Eq{Left: Col{Table: "m", Column: "object_type"}, Right: Lit(plan.ObjectType)},
					Eq{Left: Col{Table: "m", Column: "relation"}, Right: Col{Table: "t", Column: "relation"}},
					Eq{Left: Col{Table: "m", Column: "subject_type"}, Right: Col{Table: "t", Column: "subject_type"}},
				),
			},
			{
				Type:      "INNER",
				TableExpr: ClosureTable(usersetSubjClosureRows(plan), "subj_c"),
				On: And(
					Eq{Left: Col{Table: "subj_c", Column: "object_type"}, Right: Col{Table: "t", Column: "subject_type"}},
					Eq{Left: Col{Table: "subj_c", Column: "satisfying_relation"}, Right: UsersetRelation{Source: SubjectID}},
				),
			},
		},
		Where: And(
			Eq{Left: Col{Table: "t", Column: "object_type"}, Right: Lit(plan.ObjectType)},
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			// t.relation must satisfy plan.Relation (previously enforced by the c
			// closure join; now tested directly against the static satisfying set).
			In{Expr: Col{Table: "t", Column: "relation"}, Values: plan.Analysis.SatisfyingRelations},
			Ne{Left: Col{Table: "t", Column: "subject_id"}, Right: Lit("*")},
			HasUserset{Source: Col{Table: "t", Column: "subject_id"}},
			// Finding 1.3: equality on the indexed t.subject_id (sargable) folds the
			// object-id match and the subj_c.relation join into one predicate —
			// t.subject_id = <requested object>#<satisfying userset relation>.
			Eq{
				Left:  Col{Table: "t", Column: "subject_id"},
				Right: NormalizedUsersetSubject(SubjectID, Col{Table: "subj_c", Column: "relation"}),
			},
		),
		Limit: 1,
	}

	return selfCheck, computedCheck
}

func buildParentRelationBlocks(plan CheckPlan) []ParentRelationBlock {
	parents := make([]ParentRelationInfo, len(plan.Analysis.ParentRelations))
	copy(parents, plan.Analysis.ParentRelations)
	sort.SliceStable(parents, func(i, j int) bool {
		return parentRelationScore(parents[i], plan.ComplexityByRelation) <
			parentRelationScore(parents[j], plan.ComplexityByRelation)
	})

	blocks := make([]ParentRelationBlock, 0, len(parents))

	for _, parent := range parents {
		q := Tuples(plan.DatabaseSchema, "link").
			ObjectType(plan.ObjectType).
			Relations(parent.LinkingRelation).
			Where(Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID}).
			Select("link.subject_type", "link.subject_id")

		if len(parent.AllowedLinkingTypes) > 0 {
			q.WhereSubjectTypeIn(parent.AllowedLinkingTypes...)
		}

		blocks = append(blocks, ParentRelationBlock{
			LinkingRelation:     parent.LinkingRelation,
			ParentRelation:      parent.Relation,
			AllowedLinkingTypes: parent.AllowedLinkingTypes,
			Query:               q.Build(),
		})
	}

	return blocks
}

// parentRelationScore returns the worst-case complexity for a TTU parent
// relation across its allowed parent types. Used to order ParentRelationBlocks
// so cheaper parents are evaluated first in the sequential IF chain.
func parentRelationScore(p ParentRelationInfo, complexity map[string]map[string]int) int {
	worst := 0
	for _, parentType := range p.AllowedLinkingTypes {
		if score, ok := complexity[parentType][p.Relation]; ok && score > worst {
			worst = score
		}
	}
	return worst
}

func buildImpliedFunctionCalls(plan CheckPlan) []ImpliedFunctionCheck {
	relations := make([]string, len(plan.Analysis.ComplexClosureRelations))
	copy(relations, plan.Analysis.ComplexClosureRelations)
	sameType := plan.ComplexityByRelation[plan.ObjectType]
	sort.SliceStable(relations, func(i, j int) bool {
		return sameType[relations[i]] < sameType[relations[j]]
	})

	calls := make([]ImpliedFunctionCheck, 0, len(relations))

	for _, rel := range relations {
		funcName := functionName(plan.ObjectType, rel)
		if plan.NoWildcard {
			funcName = functionNameNoWildcard(plan.ObjectType, rel)
		}

		calls = append(calls, ImpliedFunctionCheck{
			Relation:     rel,
			FunctionName: funcName,
			Check: CheckPermissionCall{
				Schema:       plan.DatabaseSchema,
				FunctionName: funcName,
				Subject:      SubjectParams(),
				Relation:     rel,
				Object:       LiteralObject(plan.ObjectType, ObjectID),
				ExpectAllow:  true,
			},
		})
	}

	return calls
}

func buildIntersectionGroups(plan CheckPlan) []IntersectionGroupCheck {
	groups := make([]IntersectionGroupCheck, 0, len(plan.Analysis.IntersectionGroups))
	visitedWithKey := VisitedWithKey(plan.ObjectType, plan.Relation, ObjectID)

	for _, group := range plan.Analysis.IntersectionGroups {
		sortedParts := make([]IntersectionPart, len(group.Parts))
		copy(sortedParts, group.Parts)
		sort.SliceStable(sortedParts, func(i, j int) bool {
			return intersectionPartScore(sortedParts[i], plan) <
				intersectionPartScore(sortedParts[j], plan)
		})

		parts := make([]IntersectionPartCheck, 0, len(sortedParts))
		for _, part := range sortedParts {
			parts = append(parts, buildIntersectionPartCheck(plan, part, visitedWithKey))
		}
		groups = append(groups, IntersectionGroupCheck{Parts: parts})
	}

	return groups
}

// intersectionPartScore returns a cost class for an intersection AND-part so
// the AND chain can be ordered cheap-first. Within an AND, putting the cheap
// (and likely-to-fail) check first short-circuits the rest of the chain.
func intersectionPartScore(part IntersectionPart, plan CheckPlan) int {
	score := 0
	switch {
	case part.IsThis:
		score = 0 // direct grant on same relation: index lookup
	case part.IsSimple:
		score = 1 // inline EXISTS
	case part.ParentRelation != nil:
		score = parentRelationScore(*part.ParentRelation, plan.ComplexityByRelation) + 4
	default:
		score = plan.ComplexityByRelation[plan.ObjectType][part.Relation]
		if score == 0 {
			score = 3 // unknown but non-simple: assume function call
		}
	}
	if part.ExcludedRelation != "" {
		if part.IsExcludedSimple {
			score++
		} else {
			score += 3
		}
	}
	return score
}

// buildSimpleRelationCheck builds an inline EXISTS query for a simple relation check.
// Simple relations have no userset, recursion, exclusion, or intersection logic,
// so they can be checked with a direct tuple lookup instead of a function call.
func buildSimpleRelationCheck(plan CheckPlan, relation string) Expr {
	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(relation).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			Or(
				Eq{Left: Col{Table: "t", Column: "subject_id"}, Right: SubjectID},
				IsWildcard{Source: Col{Table: "t", Column: "subject_id"}},
			),
		).
		Select("1")

	return Exists{Query: q}
}

func buildIntersectionPartCheck(plan CheckPlan, part IntersectionPart, visitedWithKey Expr) IntersectionPartCheck {
	pc := IntersectionPartCheck{
		Relation:         part.Relation,
		ExcludedRelation: part.ExcludedRelation,
	}

	switch {
	case part.ParentRelation != nil:
		pc.IsParent = true
		pc.ParentRelation = part.ParentRelation.Relation
		pc.LinkingRelation = part.ParentRelation.LinkingRelation
		pc.Check = buildParentCheck(plan, part.ParentRelation, visitedWithKey)

	case part.IsThis:
		pc.IsThis = true
		pc.Check = buildThisCheck(plan, part, visitedWithKey)

	case part.IsSimple:
		// Optimization: inline simple relations as EXISTS instead of function calls
		pc.Check = buildSimpleRelationCheck(plan, part.Relation)

	default:
		pc.Check = CheckPermission{
			Schema:      plan.DatabaseSchema,
			Subject:     SubjectParams(),
			Relation:    part.Relation,
			Object:      LiteralObject(plan.ObjectType, ObjectID),
			Visited:     visitedWithKey,
			ExpectAllow: true,
		}
	}

	if part.ExcludedRelation != "" {
		var exclusionCheck Expr
		if part.IsExcludedSimple {
			// Optimization: inline simple exclusions as NOT EXISTS
			exclusionCheck = Not(buildSimpleRelationCheck(plan, part.ExcludedRelation))
		} else {
			exclusionCheck = CheckPermission{
				Schema:      plan.DatabaseSchema,
				Subject:     SubjectParams(),
				Relation:    part.ExcludedRelation,
				Object:      LiteralObject(plan.ObjectType, ObjectID),
				Visited:     visitedWithKey,
				ExpectAllow: false,
			}
		}
		pc.Check = And(pc.Check, exclusionCheck)
	}

	return pc
}

func buildParentCheck(plan CheckPlan, parent *ParentRelationInfo, visitedWithKey Expr) Expr {
	q := Tuples(plan.DatabaseSchema, "link").
		ObjectType(plan.ObjectType).
		Relations(parent.LinkingRelation).
		Where(
			Eq{Left: Col{Table: "link", Column: "object_id"}, Right: ObjectID},
			CheckPermission{
				Schema:   plan.DatabaseSchema,
				Subject:  SubjectParams(),
				Relation: parent.Relation,
				Object: ObjectRef{
					Type: Col{Table: "link", Column: "subject_type"},
					ID:   Col{Table: "link", Column: "subject_id"},
				},
				Visited:     visitedWithKey,
				ExpectAllow: true,
			},
		).
		Select("1")

	if len(parent.AllowedLinkingTypes) > 0 {
		q.WhereSubjectTypeIn(parent.AllowedLinkingTypes...)
	}
	return Exists{Query: q}
}

// buildThisCheck resolves the direct-assignment ("This") leg of an intersection.
// The leg's grants live on the wrapping relation itself, so besides the direct
// subject_id tuple probe we must also resolve any userset assignments
// ([group#member]) declared on that relation — otherwise a schema like
// `audience: [group#member] and active` drops the group-membership grant and
// denies legitimate members. plan.Analysis.UsersetPatterns are exactly this
// relation's own userset assignments, resolved the same way buildUsersetCheck
// resolves them for a standalone relation.
func buildThisCheck(plan CheckPlan, part IntersectionPart, visitedWithKey Expr) Expr {
	allowWildcard := part.HasWildcard && plan.AllowWildcard
	subjectCol := Col{Table: "t", Column: "subject_id"}

	var subjectCheck Expr
	if allowWildcard {
		subjectCheck = Or(
			Eq{Left: subjectCol, Right: SubjectID},
			IsWildcard{Source: subjectCol},
		)
	} else {
		subjectCheck = And(
			Eq{Left: subjectCol, Right: SubjectID},
			NotExpr{Expr: IsWildcard{Source: subjectCol}},
		)
	}

	q := Tuples(plan.DatabaseSchema, "t").
		ObjectType(plan.ObjectType).
		Relations(part.Relation).
		Where(
			Eq{Left: Col{Table: "t", Column: "object_id"}, Right: ObjectID},
			Eq{Left: Col{Table: "t", Column: "subject_type"}, Right: SubjectType},
			subjectCheck,
		).
		Select("1")

	checks := make([]Expr, 0, 1+len(plan.Analysis.UsersetPatterns))
	checks = append(checks, Exists{Query: q})
	for _, pattern := range plan.Analysis.UsersetPatterns {
		checks = append(checks, buildUsersetPatternCheck(plan, pattern, visitedWithKey))
	}
	if len(checks) == 1 {
		return checks[0]
	}
	return Or(checks...)
}
