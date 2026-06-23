package sqlgen

import (
	"strings"
	"testing"
)

// TestRenderExpandFunction_DirectOnly pins the SQL shape for a pure
// direct relation (`define owner: [user]`). Single rewrite → no Union
// envelope; the root's value slot is the Leaf.Users directly.
func TestRenderExpandFunction_DirectOnly(t *testing.T) {
	a := mkAnalysis("document", "owner", RelationFeatures{HasDirect: true}, false)
	a.SatisfyingRelations = []string{"owner"}
	a.AllowedSubjectTypes = []string{"user"}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("plan should be eligible")
	}
	got := RenderExpandFunction(plan)

	wants := []string{
		"CREATE OR REPLACE FUNCTION expand_document_owner",
		"p_object_id TEXT",
		"p_subject_type TEXT DEFAULT NULL",
		"p_max_leaf INTEGER DEFAULT NULL",
		"RETURNS JSONB",
		// Root envelope
		"jsonb_build_object('root',",
		// Node name carries "<type>:<id>#<relation>" form
		"'document' || ':' || p_object_id || '#owner'",
		// Single rewrite → leaf rendered directly under the root
		"jsonb_build_object('users'",
		// Jsonb_agg over melange_tuples with the relation filter
		"FROM melange_tuples",
		"relation = 'owner'",
		"object_type = 'document'",
		"subject_type IN ('user')",
		// Per-call subject_type filter must be honoured
		"(p_subject_type IS NULL OR subject_type = p_subject_type)",
		// COALESCE so empty results yield [] not null
		"COALESCE(",
		"'[]'::jsonb",
		// Multi-rewrite Union wrapper must NOT appear for a single rewrite
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
	if strings.Contains(got, "'union'") {
		t.Errorf("single-rewrite relation must not emit a Union wrapper; got:\n%s", got)
	}
	// users_truncated belongs to slice 2.4 — must not leak in early
	if strings.Contains(got, "users_truncated") {
		t.Errorf("users_truncated is slice 2.4; got:\n%s", got)
	}
}

// TestRenderExpandFunction_DirectAndComputed pins the multi-rewrite
// case: `define admin: [user] or owner` emits a Union of two children —
// a Leaf.Users for the direct grant and a Leaf.Computed pointer to the
// implied relation (with NO recursive resolution; that's the caller's job).
func TestRenderExpandFunction_DirectAndComputed(t *testing.T) {
	a := mkAnalysis("organization", "admin", RelationFeatures{HasDirect: true, HasImplied: true}, false)
	a.SatisfyingRelations = []string{"admin", "owner"}
	a.AllowedSubjectTypes = []string{"user"}
	a.DirectImpliedBy = []string{"owner"}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("plan should be eligible")
	}
	got := RenderExpandFunction(plan)

	wants := []string{
		"CREATE OR REPLACE FUNCTION expand_organization_admin",
		// Multi-rewrite → Union envelope wraps two child nodes
		"jsonb_build_object('union'",
		"jsonb_build_array(",
		// Direct rewrite leaf
		"jsonb_build_object('users'",
		"relation = 'admin'",
		// Computed pointer to the implied relation — shallow, no recursion
		"jsonb_build_object('computed'",
		"'organization' || ':' || p_object_id || '#owner'",
		// Both children share the parent relation's name field
		"'organization' || ':' || p_object_id || '#admin'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
	// Defensive: the implied relation must NOT be folded into a
	// `relation IN (...)` lookup. That's what Check does; OpenFGA Expand
	// keeps the rewrites separate so the consumer sees the structure.
	if strings.Contains(got, "relation IN ('admin', 'owner')") ||
		strings.Contains(got, "relation IN ('owner', 'admin')") {
		t.Errorf("Expand must NOT flatten the implied chain into a closure list:\n%s", got)
	}
}

// TestRenderExpandFunction_PureComputed pins a relation with no direct
// access (`define can_read: member`): single Computed rewrite, no
// Union wrapper.
func TestRenderExpandFunction_PureComputed(t *testing.T) {
	a := mkAnalysis("organization", "can_read", RelationFeatures{HasImplied: true}, false)
	a.SatisfyingRelations = []string{"can_read", "member"}
	a.DirectImpliedBy = []string{"member"}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("plan should be eligible")
	}
	got := RenderExpandFunction(plan)

	if !strings.Contains(got, "jsonb_build_object('computed'") {
		t.Errorf("computed leaf missing:\n%s", got)
	}
	if strings.Contains(got, "'union'") {
		t.Errorf("single computed rewrite must not emit a Union wrapper:\n%s", got)
	}
	// No direct rewrite → must not generate a melange_tuples lookup
	if strings.Contains(got, "FROM melange_tuples") {
		t.Errorf("pure-computed relation must not query melange_tuples:\n%s", got)
	}
}

// TestBuildExpandPlan_Ineligible documents which feature combinations
// the slice 2.1 gate rejects. Each later slice flips one of these to
// true and updates the test.
func TestBuildExpandPlan_Ineligible(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*RelationAnalysis)
		slice   string
	}{
		{"has wildcard", func(a *RelationAnalysis) {
			a.Features.HasWildcard = true
		}, "slice 2.3"},
		{"has userset", func(a *RelationAnalysis) {
			a.Features.HasUserset = true
		}, "slice 2.3"},
		{"has intersection", func(a *RelationAnalysis) {
			a.Features.HasIntersection = true
		}, "slice 2.2"},
		{"has multi-exclusion", func(a *RelationAnalysis) {
			a.Features.HasExclusion = true
			a.ExcludedRelations = []string{"banned", "author"}
		}, "follow-up (slice 2.2b ships single exclusion only)"},
		{"has TTU exclusion", func(a *RelationAnalysis) {
			a.Features.HasExclusion = true
			a.ExcludedRelations = []string{"banned"}
			a.ExcludedParentRelations = []ParentRelationInfo{{Relation: "banned", LinkingRelation: "parent"}}
		}, "follow-up"},
		{"has intersection exclusion", func(a *RelationAnalysis) {
			a.Features.HasExclusion = true
			a.ExcludedRelations = []string{"banned"}
			a.ExcludedIntersectionGroups = []IntersectionGroupInfo{{}}
		}, "follow-up"},
		{"has complex userset patterns", func(a *RelationAnalysis) {
			a.HasComplexUsersetPatterns = true
		}, "follow-up"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true}, false)
			a.AllowedSubjectTypes = []string{"user"}
			tc.mutate(&a)
			if _, ok := BuildExpandPlan(a, ""); ok {
				t.Errorf("plan should be ineligible (%s) for %s", tc.name, tc.slice)
			}
		})
	}
}

// TestBuildExpandPlan_NoAccessPaths confirms a relation with no
// direct grants AND no implied rewrites is treated as ineligible
// rather than emitting a structurally empty tree.
func TestBuildExpandPlan_NoAccessPaths(t *testing.T) {
	a := mkAnalysis("doc", "phantom", RelationFeatures{}, false)
	if _, ok := BuildExpandPlan(a, ""); ok {
		t.Errorf("plan with no rewrites must be ineligible — let the dispatcher sentinel handle it")
	}
}

// TestRenderExpandFunction_TTUOnly pins the slice 2.2a TTU emission:
// `define can_deploy: can_admin from org` with parent: [organization]
// yields a single Leaf.TupleToUserset whose tupleset names the linking
// relation ("<obj>:#org") and whose computed array is a jsonb_agg over
// the org-linking tuples, each projecting "organization:<id>#can_admin".
func TestRenderExpandFunction_TTUOnly(t *testing.T) {
	a := mkAnalysis("repository", "can_deploy", RelationFeatures{HasRecursive: true}, false)
	a.SatisfyingRelations = []string{"can_deploy"}
	a.ParentRelations = []ParentRelationInfo{{
		Relation:            "can_admin",
		LinkingRelation:     "org",
		AllowedLinkingTypes: []string{"organization"},
	}}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("TTU-only plan should be eligible after slice 2.2a")
	}
	got := RenderExpandFunction(plan)

	wants := []string{
		"CREATE OR REPLACE FUNCTION expand_repository_can_deploy",
		// Tupleset names the linking relation on the current object
		"'repository' || ':' || p_object_id || '#org'",
		// Computed pointers project the parent relation per linked object
		"subject_type || ':' || subject_id || '#can_admin'",
		// jsonb_agg with stable ORDER BY so output is deterministic
		"jsonb_agg(jsonb_build_object('userset'",
		"ORDER BY subject_type, subject_id",
		// Linking-type filter from AllowedLinkingTypes
		"subject_type IN ('organization')",
		// Linking relation filter
		"relation = 'org'",
		// OpenFGA-shape envelope
		"'tuple_to_userset'",
		"'tupleset'",
		"'computed'",
		// Leaf wrapper so the tree node deserialises as Node.Leaf
		"jsonb_build_object('leaf', jsonb_build_object('tuple_to_userset'",
		// COALESCE so an empty computed array becomes [] not null
		"COALESCE(",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
	// Single-rewrite relations skip the Union envelope — TTU on its own
	// shouldn't wrap in union.
	if strings.Contains(got, "'union'") {
		t.Errorf("single TTU rewrite must not emit a Union envelope:\n%s", got)
	}
}

// TestRenderExpandFunction_ExclusionWraps pins slice 2.2b: a relation
// with a simple `but not X` exclusion (`can_review: can_read but not
// author`) wraps the rewrites-derived tree in Difference{base,
// subtract}. Base shares the parent relation's name; subtract names the
// excluded relation. OpenFGA-shape named slots — not positional
// children.
func TestRenderExpandFunction_ExclusionWraps(t *testing.T) {
	a := mkAnalysis("repository", "can_review", RelationFeatures{HasImplied: true, HasExclusion: true}, false)
	a.SatisfyingRelations = []string{"can_review", "can_read"}
	a.DirectImpliedBy = []string{"can_read"}
	a.ExcludedRelations = []string{"author"}
	a.SimpleExcludedRelations = []string{"author"}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("simple-exclusion plan should be eligible after slice 2.2b")
	}
	if plan.Exclusion != "author" {
		t.Errorf("plan.Exclusion: got %q, want %q", plan.Exclusion, "author")
	}
	got := RenderExpandFunction(plan)

	wants := []string{
		"CREATE OR REPLACE FUNCTION expand_repository_can_review",
		// Difference wrapper with named slots
		"jsonb_build_object('difference'",
		"'base'",
		"'subtract'",
		// Base node carries the parent relation's name (the same as the
		// root) because it represents "the relation without exclusion".
		"'repository' || ':' || p_object_id || '#can_review'",
		// Subtract names the excluded relation.
		"'repository' || ':' || p_object_id || '#author'",
		// Base contains the rewrites tree (here: a Computed pointer
		// to can_read since the relation is pure-computed).
		"'repository' || ':' || p_object_id || '#can_read'",
		// Subtract emits a leaf — Computed pointer, never resolved
		// here (the caller chases it).
		"'leaf', jsonb_build_object('computed'",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in generated SQL:\n%s", w, got)
		}
	}
}

// TestRenderExpandFunction_TTUWithExclusion exercises the
// `pull_request.can_review: can_read from repo but not author` shape:
// base is a Leaf.TupleToUserset (from slice 2.2a's TTU emission);
// subtract is a Computed pointer to the excluded relation. The two
// features compose without either branch needing knowledge of the
// other.
func TestRenderExpandFunction_TTUWithExclusion(t *testing.T) {
	a := mkAnalysis("pull_request", "can_review", RelationFeatures{HasRecursive: true, HasExclusion: true}, false)
	a.SatisfyingRelations = []string{"can_review"}
	a.ParentRelations = []ParentRelationInfo{{
		Relation:            "can_read",
		LinkingRelation:     "repo",
		AllowedLinkingTypes: []string{"repository"},
	}}
	a.ExcludedRelations = []string{"author"}
	a.SimpleExcludedRelations = []string{"author"}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("TTU+exclusion plan should be eligible")
	}
	got := RenderExpandFunction(plan)
	if !strings.Contains(got, "'difference'") {
		t.Errorf("missing difference wrapper:\n%s", got)
	}
	// Base must be the TTU leaf (tuple_to_userset emission from 2.2a)
	if !strings.Contains(got, "tuple_to_userset") {
		t.Errorf("base of difference must carry the TTU rewrite:\n%s", got)
	}
	// Subtract is the Computed pointer to author
	if !strings.Contains(got, "'pull_request' || ':' || p_object_id || '#author'") {
		t.Errorf("subtract must name the excluded relation:\n%s", got)
	}
}

// TestRenderExpandFunction_DirectAndTTU exercises the multi-rewrite
// path where the relation has both direct grants and a TTU
// ("viewer: [user] or viewer from parent"). Both rewrites surface as
// siblings under a Nodes union.
func TestRenderExpandFunction_DirectAndTTU(t *testing.T) {
	a := mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true, HasRecursive: true}, false)
	a.SatisfyingRelations = []string{"viewer"}
	a.AllowedSubjectTypes = []string{"user"}
	a.ParentRelations = []ParentRelationInfo{{
		Relation:            "viewer",
		LinkingRelation:     "parent",
		AllowedLinkingTypes: []string{"folder"},
	}}

	plan, ok := BuildExpandPlan(a, "")
	if !ok {
		t.Fatalf("direct+TTU plan should be eligible")
	}
	got := RenderExpandFunction(plan)

	if !strings.Contains(got, "'union'") {
		t.Errorf("multi-rewrite relation must emit Union envelope:\n%s", got)
	}
	// Both rewrites present
	if !strings.Contains(got, "subject_type IN ('user')") {
		t.Errorf("direct rewrite missing:\n%s", got)
	}
	if !strings.Contains(got, "tuple_to_userset") {
		t.Errorf("TTU rewrite missing:\n%s", got)
	}
}
