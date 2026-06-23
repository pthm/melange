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
		{"has recursive (TTU)", func(a *RelationAnalysis) {
			a.Features.HasRecursive = true
		}, "slice 2.2"},
		{"has intersection", func(a *RelationAnalysis) {
			a.Features.HasIntersection = true
		}, "slice 2.2"},
		{"has exclusion", func(a *RelationAnalysis) {
			a.Features.HasExclusion = true
		}, "slice 2.2"},
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
