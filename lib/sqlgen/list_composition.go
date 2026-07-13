package sqlgen

// listCompositionSafe reports whether the list function for
// (fromType, fromRelation) may compose with — i.e. call — the list function
// for (targetType, targetRelation) without risking infinite recursion or an
// unconditional depth-limit raise.
//
// List functions carry no p_visited cycle guard (unlike check functions), so
// a list→list call is only safe when the callee can never call back into the
// caller. That is decided at compile time by walking the relation-reference
// graph from the target: if (fromType, fromRelation) is unreachable, no chain
// of generated functions starting at the target can re-enter the caller.
//
// Every list→list call the renderers emit is itself gated by this function
// (or corresponds to a reference edge below), so pairwise gating composes:
// any cycle of emitted calls would need one gated edge whose walk sees the
// rest of the cycle as reference edges and refuses it.
//
// The walk also refuses compositions that can reach a DepthExceeded relation:
// its list function raises unconditionally, and that raise would surface
// through the composition where the per-candidate check fallback used to
// resolve the query (check functions carry their own depth guard).
//
// The edge set (relationReferences) over-approximates the calls generated SQL
// can make, so a "safe" verdict is sound; an "unsafe" verdict merely keeps the
// per-candidate check_permission_internal fallback, which is always correct.
func listCompositionSafe(lookup map[string]*RelationAnalysis, fromType, fromRelation, targetType, targetRelation string) bool {
	if lookup == nil {
		return false
	}
	from := fromType + "." + fromRelation
	start := targetType + "." + targetRelation
	if start == from {
		return false
	}

	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		a, ok := lookup[key]
		if !ok {
			continue
		}
		if a.ListStrategy == ListStrategyDepthExceeded {
			return false
		}
		for _, next := range relationReferences(a) {
			if next == from {
				return false
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return true
}

// relationReferences returns every "type.relation" key the generated functions
// for a may reference — same-type closure/implied/excluded relations, TTU
// parents, and userset targets, both direct and inherited through closure.
// Deliberately over-inclusive: extra edges can only make listCompositionSafe
// more conservative, never incorrect.
//
// MAINTENANCE: this must cover every reference-carrying field of
// RelationAnalysis; a missed edge means listCompositionSafe can approve a
// cyclic composition, which recurses infinitely at query time. A companion
// reflection test (TestRelationReferencesFieldCoverage) fails when
// RelationAnalysis gains a new field that has not been reviewed for edges.
// analysis/analysis_types.go's sortByDependency walks a related (narrower)
// edge set for ordering; keep both in mind when adding reference kinds.
func relationReferences(a *RelationAnalysis) []string {
	var refs []string
	sameType := func(rels ...string) {
		for _, r := range rels {
			if r != "" {
				refs = append(refs, a.ObjectType+"."+r)
			}
		}
	}
	parents := func(infos ...ParentRelationInfo) {
		for _, p := range infos {
			for _, t := range p.AllowedLinkingTypes {
				refs = append(refs, t+"."+p.Relation)
			}
		}
	}

	sameType(a.SatisfyingRelations...)
	sameType(a.DirectImpliedBy...)
	sameType(a.SimpleClosureRelations...)
	sameType(a.ComplexClosureRelations...)
	sameType(a.IntersectionClosureRelations...)
	sameType(a.ExcludedRelations...)
	sameType(a.SimpleExcludedRelations...)
	sameType(a.ComplexExcludedRelations...)
	sameType(a.ClosureExcludedRelations...)

	parents(a.ParentRelations...)
	parents(a.ClosureParentRelations...)
	parents(a.ExcludedParentRelations...)

	for _, u := range a.UsersetPatterns {
		refs = append(refs, u.SubjectType+"."+u.SubjectRelation)
	}
	for _, u := range a.ClosureUsersetPatterns {
		refs = append(refs, u.SubjectType+"."+u.SubjectRelation)
	}
	for _, u := range a.SelfReferentialUsersets {
		refs = append(refs, u.SubjectType+"."+u.SubjectRelation)
	}

	groups := func(gs ...IntersectionGroupInfo) {
		for _, g := range gs {
			for _, p := range g.Parts {
				sameType(p.Relation, p.ExcludedRelation)
				if p.ParentRelation != nil {
					parents(*p.ParentRelation)
				}
			}
		}
	}
	groups(a.IntersectionGroups...)
	groups(a.ExcludedIntersectionGroups...)

	if a.IndirectAnchor != nil {
		refs = append(refs, a.IndirectAnchor.AnchorType+"."+a.IndirectAnchor.AnchorRelation)
		for _, step := range a.IndirectAnchor.Path {
			switch step.Type {
			case "ttu":
				for _, t := range step.AllTargetTypes {
					refs = append(refs, t+"."+step.TargetRelation)
				}
				for _, t := range step.RecursiveTypes {
					refs = append(refs, t+"."+step.TargetRelation)
				}
			case "userset":
				refs = append(refs, step.SubjectType+"."+step.SubjectRelation)
			}
		}
	}

	return refs
}

// reachesWildcard reports whether the list_subjects function for
// (targetType, targetRelation) can surface a wildcard token ('*') — either the
// relation itself HasWildcard, or it transitively reaches a wildcard relation
// through its TTU parents / closure / userset reference edges. list_*_sub
// returns '*' for such grants, not the concrete subjects, so an IN/LATERAL
// membership composed against it would drop concrete candidates that hold the
// relation only via the wildcard (under-report).
//
// Features.HasWildcard is NOT propagated across TTU parent edges by the
// analyzer: e.g. folder.viewer = "[user, group#member] or viewer from org"
// with org.viewer = [user:*] has folder.viewer.HasWildcard=false, yet
// list_folder_viewer_sub returns '*' from the parent. This walk (over the same
// over-approximating edge set as relationReferences, so a "reaches" verdict is
// sound) closes that gap. Callers skip composition and keep the subject_pool /
// per-candidate fallback, which resolves wildcards correctly.
func reachesWildcard(lookup map[string]*RelationAnalysis, targetType, targetRelation string) bool {
	if lookup == nil {
		return true // no analysis: assume unsafe
	}
	start := targetType + "." + targetRelation
	visited := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		a, ok := lookup[key]
		if !ok {
			continue
		}
		if a.Features.HasWildcard {
			return true
		}
		for _, next := range relationReferences(a) {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	return false
}

// composableListTargetLookup reports whether the list_objects function for
// (fromType, fromRelation) may compose with the list_objects function for
// (targetType, targetRelation): the target must be list-generatable and
// composition must be cycle-free and unable to reach an always-raising
// DepthExceeded list function (listCompositionSafe checks DepthExceeded on
// every reachable node, including the target itself).
func composableListTargetLookup(lookup map[string]*RelationAnalysis, fromType, fromRelation, targetType, targetRelation string) bool {
	if lookup == nil {
		return false
	}
	target, ok := lookup[targetType+"."+targetRelation]
	if !ok || !target.Capabilities.ListAllowed {
		return false
	}
	return listCompositionSafe(lookup, fromType, fromRelation, targetType, targetRelation)
}

// composableListTarget is the ListPlan-scoped form of
// composableListTargetLookup, from this plan's relation to the target.
func composableListTarget(plan ListPlan, targetType, targetRelation string) bool {
	return composableListTargetLookup(plan.AnalysisLookup, plan.ObjectType, plan.Relation, targetType, targetRelation)
}

// composableListSubjectsTarget reports whether this plan's list_subjects
// function may compose against the target's list_{targetType}_{targetRelation}_sub
// function (complex closure, complex userset, TTU subject-first direct + closure
// patterns).
//
// A wildcard-reaching target is composable: its list_*_sub emits '*' for wildcard
// grants, that '*' flows into base_results, and the wildcard-completion tail
// (wildcardSubjectsTailWhere) verifies it against the full relation. Concrete
// subjects that hold the relation only via that wildcard are represented by the
// '*' row — which is the OpenFGA list_subjects semantics (public access is
// reported as the type-bound wildcard, not enumerated). Cycle/depth safety is
// still enforced by composableListTarget.
func composableListSubjectsTarget(plan ListPlan, targetType, targetRelation string) bool {
	return composableListTarget(plan, targetType, targetRelation)
}

// composedListObjectsMembership is the positive-membership predicate shared by
// every list_objects composition site: "the subject holds targetRel on the
// candidate object objectIDExpr". It semi-joins against the target relation's
// list_objects set and keeps a check arm guarded to userset-typed subjects
// (position('#' in subjectID) > 0) for parity — the list function is complete
// for plain subjects but a Recursive/Composed target may under-report a userset
// subject ("group:eng#member"), and an under-reported positive membership drops
// objects (under-permissive); for plain subjects the guard is false and the arm
// short-circuits. Callers gate composability first and pass the per-candidate
// check as the fallback arm; anti-join sites negate the result. The alias only
// affects the rendered subquery alias and must stay unique within a predicate.
func composedListObjectsMembership(schema, targetType, targetRel string, objectIDExpr, subjectType, subjectID Expr, alias string, check Expr) Expr {
	return Or(
		InFunctionSelect{
			Expr:      objectIDExpr,
			Schema:    schema,
			FuncName:  ListObjectsFunctionName(targetType, targetRel),
			Args:      []Expr{subjectType, subjectID, Null{}, Null{}},
			Alias:     alias,
			SelectCol: "object_id",
		},
		And(HasUserset{Source: subjectID}, check),
	)
}

// complexClosureMembership returns the predicate proving the subject holds rel
// on the candidate object t.object_id, for a complex closure relation.
//
// When composition is safe it replaces the per-candidate
// check_permission_internal with a semi-join against rel's list_objects set
// (the set of objects the subject holds rel on), keeping a check arm guarded to
// userset-typed subjects for parity: the list function is complete for plain
// subjects but a Recursive/Composed target may under-report userset subjects
// ("group:eng#member"), and an under-reported positive membership drops objects
// (under-permissive). For plain subjects the guard is false and the arm
// short-circuits before the check. When composition is unsafe it returns the
// per-candidate check alone. Mirrors usersetMembership / complexExclusionAntiJoin.
func complexClosureMembership(plan ListPlan, rel string) Expr {
	check := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectParams(),
		Relation:    rel,
		Object:      LiteralObject(plan.ObjectType, Col{Table: "t", Column: "object_id"}),
		ExpectAllow: true,
	}
	if !composableListTarget(plan, plan.ObjectType, rel) {
		return check
	}
	return composedListObjectsMembership(plan.DatabaseSchema, plan.ObjectType, rel, Col{Table: "t", Column: "object_id"}, SubjectType, SubjectID, "obj", check)
}

// complexClosureSubjectMembership returns the predicate proving the candidate
// subject t.subject_id holds rel on the object p_object_id, for a complex
// closure relation in list_subjects.
//
// When composition is safe it replaces the per-candidate
// check_permission_internal with a semi-join against rel's list_subjects set —
// list_{type}_{rel}_sub(p_object_id, p_subject_type) is exactly the set of
// subjects holding rel on the object. The list_objects mirror keeps a userset-
// guarded check arm, but list_subjects composition is gated OUT here when
// this relation HasWildcard: list_{type}_{rel}_sub returns '*' for wildcard
// grants, not the concrete subjects, so an IN membership would drop a concrete
// candidate that holds rel only via a wildcard (under-report). Mirrors
// composableSubjectFirstUserset's HasWildcard gate. (Unlike
// buildSubjectFirstTTUSubjectBlocks, whose '*' rows flow through base_results to
// the wildcard-completion tail, there is no such tail on an IN-set membership.)
//
// When composition is unsafe it returns the per-candidate check alone, which is
// always correct. Mirrors complexClosureMembership.
func complexClosureSubjectMembership(plan ListPlan, rel string) Expr {
	check := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectRef{Type: SubjectType, ID: Col{Table: "t", Column: "subject_id"}},
		Relation:    rel,
		Object:      LiteralObject(plan.ObjectType, ObjectID),
		ExpectAllow: true,
	}
	if plan.Analysis.Features.HasWildcard {
		return check
	}
	if !composableListSubjectsTarget(plan, plan.ObjectType, rel) {
		return check
	}
	return InFunctionSelect{
		Expr:      Col{Table: "t", Column: "subject_id"},
		Schema:    plan.DatabaseSchema,
		FuncName:  listSubjectsFunctionName(plan.ObjectType, rel),
		Args:      []Expr{ObjectID, SubjectType},
		Alias:     "sub",
		SelectCol: "subject_id",
	}
}

// intersectionPartComposable reports whether a list_objects intersection part
// may compose with rel's list_objects set. INTERSECT is only sound when every
// part is complete: if any semi-joined part under-reports, the whole object is
// dropped. Keep this proof to direct, non-wildcard targets. Recursive/userset/
// composed targets can make check_permission_internal true for a plain concrete
// subject through paths their list_objects function may not enumerate; the
// userset-parity arm cannot recover that case because its guard is false for
// plain subjects.
func intersectionPartComposable(plan ListPlan, rel string) bool {
	target := plan.AnalysisLookup[plan.ObjectType+"."+rel]
	if target == nil || target.Features.HasWildcard || target.ListStrategy != ListStrategyDirect {
		return false
	}
	return composableListTarget(plan, plan.ObjectType, rel)
}

// intersectionPartMembership returns the predicate proving the subject holds
// rel on the candidate object objectID, for one positive part of an INTERSECT
// group in list_objects.
//
// When composition is safe it replaces the per-candidate check_permission_internal
// with a semi-join against rel's list_objects set, keeping a check arm guarded to
// userset-typed subjects for parity (a Recursive/Composed target may under-report
// a userset subject like "group:eng#member"; an under-reported positive membership
// drops objects that should stay in the group — under-permissive). For plain
// subjects the guard is false and the arm short-circuits. When composition is
// unsafe it returns the per-candidate check alone. Mirrors complexClosureMembership.
func intersectionPartMembership(plan ListPlan, rel string, objectID Expr) Expr {
	check := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectParams(),
		Relation:    rel,
		Object:      LiteralObject(plan.ObjectType, objectID),
		ExpectAllow: true,
	}
	if !intersectionPartComposable(plan, rel) {
		return check
	}
	return composedListObjectsMembership(plan.DatabaseSchema, plan.ObjectType, rel, objectID, SubjectType, SubjectID, "obj", check)
}

// intersectionPartExclusion returns the "but not rel" predicate for a nested
// exclusion inside an INTERSECT part: the object is kept when the subject does
// NOT hold rel on it. When composition is safe it is the negation of rel's
// list_objects membership (plus a userset-guarded check arm — an under-reported
// exclusion is over-permissive); otherwise the per-candidate check_permission_internal
// with ExpectAllow=false. Mirrors complexExclusionAntiJoin.
func intersectionPartExclusion(plan ListPlan, rel string, objectID Expr) Expr {
	check := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectParams(),
		Relation:    rel,
		Object:      LiteralObject(plan.ObjectType, objectID),
		ExpectAllow: false,
	}
	if !intersectionPartComposable(plan, rel) {
		return check
	}
	positiveCheck := CheckPermission{
		Schema:      plan.DatabaseSchema,
		Subject:     SubjectParams(),
		Relation:    rel,
		Object:      LiteralObject(plan.ObjectType, objectID),
		ExpectAllow: true,
	}
	return Not(composedListObjectsMembership(plan.DatabaseSchema, plan.ObjectType, rel, objectID, SubjectType, SubjectID, "excl_obj", positiveCheck))
}

// usersetMembership returns the membership predicate for a complex userset
// arm: does the subject hold pattern.SubjectRelation on the userset object?
//
// When composition is safe it semi-joins against the userset relation's list
// function — the set-oriented replacement for a per-candidate check — but
// keeps a check_permission_internal arm guarded to userset-typed subjects:
// check functions can prove membership for subjects like "group:eng#member"
// through closure arms that not every list strategy enumerates. For plain
// subjects the guard is false and the arm short-circuits before the check.
//
// When composition is not possible it returns the per-candidate check alone.
func usersetMembership(plan ListPlan, pattern listUsersetPatternInput) Expr {
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
	if !composableListTarget(plan, pattern.SubjectType, pattern.SubjectRelation) {
		return check
	}
	return composedListObjectsMembership(plan.DatabaseSchema, pattern.SubjectType, pattern.SubjectRelation, UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}}, SubjectType, SubjectID, "obj", check)
}
