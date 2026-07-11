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

// composableListTarget reports whether (targetType, targetRelation) has a
// usable list_objects function this plan's function may compose with: the
// target must be list-generatable and composition must be cycle-free and
// unable to reach an always-raising DepthExceeded list function.
func composableListTarget(plan ListPlan, targetType, targetRelation string) bool {
	if plan.AnalysisLookup == nil {
		return false
	}
	target, ok := plan.AnalysisLookup[targetType+"."+targetRelation]
	if !ok || !target.Capabilities.ListAllowed {
		return false
	}
	return listCompositionSafe(plan.AnalysisLookup, plan.ObjectType, plan.Relation, targetType, targetRelation)
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
	return Or(
		InFunctionSelect{
			Expr:      UsersetObjectID{Source: Col{Table: "t", Column: "subject_id"}},
			Schema:    plan.DatabaseSchema,
			FuncName:  ListObjectsFunctionName(pattern.SubjectType, pattern.SubjectRelation),
			Args:      []Expr{SubjectType, SubjectID, Null{}, Null{}},
			Alias:     "obj",
			SelectCol: "object_id",
		},
		And(HasUserset{Source: SubjectID}, check),
	)
}
