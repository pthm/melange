package sqlgen

import (
	"strings"
	"testing"
)

// TestWildcardSubjectsTailRevalidatesUnderExclusion pins the F5 invariant that
// the list_subjects per-arm exclusion/check strip depends on: when a relation
// has an exclusion, the wildcard-completion tail must NOT short-circuit on
// has_wildcard and must re-validate every non-'*' subject with the no-wildcard
// check. If the short-circuit were reintroduced under exclusion, stripped
// per-arm predicates would let unvalidated (e.g. banned) subjects through when
// the result set has no '*' row — an over-report / access leak.
func TestWildcardSubjectsTailRevalidatesUnderExclusion(t *testing.T) {
	withExcl := wildcardSubjectsTailWhere("", "viewer", "doc", true).SQL()
	if strings.Contains(withExcl, "has_wildcard") {
		t.Errorf("exclusion tail must not short-circuit on has_wildcard (skips re-validation): %s", withExcl)
	}
	if !strings.Contains(withExcl, "check_permission_nw_internal") || !strings.Contains(withExcl, "<> '*'") {
		t.Errorf("exclusion tail must re-validate non-'*' subjects with the no-wildcard check: %s", withExcl)
	}

	noExcl := wildcardSubjectsTailWhere("", "viewer", "doc", false).SQL()
	if !strings.Contains(noExcl, "has_wildcard") {
		t.Errorf("non-exclusion tail should keep the has_wildcard short-circuit: %s", noExcl)
	}
}

// TestEmitsNoWildcardCoversAllReferenceEdges guards the F6 invariant: a _nw
// check variant is emitted for any relation that reaches a [user:*] grant,
// including transitively. emitsNoWildcard delegates to reachesWildcard, which
// walks relationReferences; if a future change drops an edge kind from
// relationReferences, a wildcard-reaching relation would be misclassified as
// "no _nw needed" and its _nw dispatcher/call sites would route to the base
// function that surfaces the wildcard — an over-report/leak. This pins that
// every reference edge propagates wildcard-reachability.
func TestEmitsNoWildcardCoversAllReferenceEdges(t *testing.T) {
	// Wildcard-bearing targets `ref` can reach: same-type ("t.wild") and, for TTU
	// edges, a cross-type parent ("p.wild").
	wild := &RelationAnalysis{ObjectType: "t", Relation: "wild", Features: RelationFeatures{HasWildcard: true}}
	pwild := &RelationAnalysis{ObjectType: "p", Relation: "wild", Features: RelationFeatures{HasWildcard: true}}
	ttu := []ParentRelationInfo{{Relation: "wild", AllowedLinkingTypes: []string{"p"}}}

	// Each case sets exactly one reference edge on `ref` pointing at a wildcard.
	edges := []struct {
		name string
		set  func(a *RelationAnalysis)
	}{
		{"SatisfyingRelations", func(a *RelationAnalysis) { a.SatisfyingRelations = []string{"wild"} }},
		{"DirectImpliedBy", func(a *RelationAnalysis) { a.DirectImpliedBy = []string{"wild"} }},
		{"SimpleClosureRelations", func(a *RelationAnalysis) { a.SimpleClosureRelations = []string{"wild"} }},
		{"ComplexClosureRelations", func(a *RelationAnalysis) { a.ComplexClosureRelations = []string{"wild"} }},
		{"IntersectionClosureRelations", func(a *RelationAnalysis) { a.IntersectionClosureRelations = []string{"wild"} }},
		{"ExcludedRelations", func(a *RelationAnalysis) { a.ExcludedRelations = []string{"wild"} }},
		{"SimpleExcludedRelations", func(a *RelationAnalysis) { a.SimpleExcludedRelations = []string{"wild"} }},
		{"ComplexExcludedRelations", func(a *RelationAnalysis) { a.ComplexExcludedRelations = []string{"wild"} }},
		{"ClosureExcludedRelations", func(a *RelationAnalysis) { a.ClosureExcludedRelations = []string{"wild"} }},
		{"ParentRelations", func(a *RelationAnalysis) { a.ParentRelations = ttu }},
		{"ClosureParentRelations", func(a *RelationAnalysis) { a.ClosureParentRelations = ttu }},
		{"ExcludedParentRelations", func(a *RelationAnalysis) { a.ExcludedParentRelations = ttu }},
	}

	for _, e := range edges {
		t.Run(e.name, func(t *testing.T) {
			ref := &RelationAnalysis{ObjectType: "t", Relation: "ref"}
			e.set(ref)
			lookup := map[string]*RelationAnalysis{
				"t.wild": wild,
				"p.wild": pwild,
				"t.ref":  ref,
			}
			if !emitsNoWildcard(lookup, *ref) {
				t.Errorf("emitsNoWildcard=false for a relation reaching a wildcard via %s; "+
					"relationReferences no longer propagates this edge, so a _nw variant "+
					"would be wrongly skipped (wildcard leak on the base function)", e.name)
			}
		})
	}

	// Control: a relation with no wildcard-reaching edge must NOT emit _nw.
	plain := RelationAnalysis{ObjectType: "t", Relation: "plain", DirectImpliedBy: []string{"other"}}
	lookup := map[string]*RelationAnalysis{
		"t.plain": &plain,
		"t.other": {ObjectType: "t", Relation: "other"},
	}
	if emitsNoWildcard(lookup, plain) {
		t.Errorf("emitsNoWildcard=true for a relation reaching no wildcard; _nw would be emitted needlessly")
	}
}
