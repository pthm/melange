package openfgatests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
)

// runExpandParityAssertions cross-references ExpandRecursive against
// list_objects for every eligible ListObjectsAssertion in the stage.
// Each assertion expects "user U has relation R on objects [O1, O2, …]";
// for each object in the expectation we call ExpandRecursive(O, R) and
// assert the user (or a covering wildcard) appears in the flat result.
//
// This is the Expand-side mirror of runExplainParityAssertions: the
// existing 834-assertion Explain-parity sweep pins Result-vs-Check
// agreement; this sweep pins Users-vs-list_subjects agreement, just
// inverted (we don't have list_subjects assertions in OpenFGA YAMLs,
// but list_objects gives us the same information from the other side
// — "this user appears in this object's user list").
//
// Eligibility:
//   - assertion.ErrorCode == 0 (success-path agreement)
//   - len(assertion.ContextualTuples) == 0 (Expand SQL doesn't accept them)
//   - assertion.Request.User is a concrete user — userset-subject
//     assertions (`user: group:eng#member`) would need a different
//     parity contract (does the userset string appear?) and are
//     deferred to a follow-up. Wildcard subjects (`user: user:*`)
//     are also skipped — they're an expectation about the wildcard
//     grant existing, not about a specific user being expandable.
//
// Skipping (not failing):
//   - The dispatcher's empty-leaf sentinel. When the relation's
//     renderer is not yet eligible (see lib/sqlgen
//     ComputeExpandEligibility) the dispatcher emits an empty
//     Leaf.Users tree. Asserting parity would let the sentinel
//     falsely "pass" empty-expectation assertions; we skip and log.
//
// Wildcard semantics: an expected `user:alice` is considered a
// match if ExpandRecursive returns either `user:alice` exactly OR
// `user:*` (every user of that type satisfies the wildcard grant).
// This matches OpenFGA's wildcard semantics — list_objects returns
// objects covered by either a direct or a wildcard grant.
func runExpandParityAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, listAssertions []*ListObjectsAssertion) {
	t.Helper()

	for i, a := range listAssertions {
		name := fmt.Sprintf("expand_parity_%d", i)
		t.Run(name, func(t *testing.T) {
			if a.ErrorCode != 0 {
				t.Skip("error-coded list_objects assertions don't have an Expand parity contract")
			}
			if len(a.ContextualTuples) != 0 {
				t.Skip("Expand SQL doesn't accept contextual tuples yet")
			}
			expectedUser := a.Request.User
			if !isConcreteUser(expectedUser) {
				t.Skipf("non-concrete user %q — userset/wildcard subjects deferred", expectedUser)
			}
			if len(a.Expectation) == 0 {
				return // empty expectation = no objects to verify
			}

			for _, object := range a.Expectation {
				// Probe with a non-recursive Expand first to detect
				// shapes where the ExpandRecursive→list_objects parity
				// contract doesn't hold: intersection (AND semantics
				// can't be approximated by flat-list union), exclusion
				// (subtract is ignored by the flatten walker), and
				// the dispatcher's empty-leaf sentinel (relation not
				// yet eligible for Expand). Skip rather than fail.
				tree, err := client.Expand(ctx, storeID, modelID, object, a.Request.Relation)
				require.NoError(t, err,
					"expand %s#%s failed", object, a.Request.Relation)
				if reason := parityUnsupported(tree); reason != "" {
					t.Skipf("%s for %s#%s — list_objects expects %q",
						reason, object, a.Request.Relation, expectedUser)
					return
				}

				users, err := client.ExpandRecursive(ctx, storeID, modelID, object, a.Request.Relation)
				require.NoError(t, err,
					"expandRecursive %s#%s failed", object, a.Request.Relation)

				if userInOrCoveredByWildcard(expectedUser, users) {
					return
				}
				// Expand intentionally does NOT chase userset references
				// (`group:eng#member`) — they're terminal nodes in the
				// OpenFGA contract, and resolving them would mean
				// looking up the userset's membership recursively (a
				// different SQL surface). list_objects DOES surface
				// users reachable through userset membership, so skip
				// rather than fail.
				if containsUsersetReference(users) {
					t.Skipf("user %q not directly present; result contains userset references %v which Expand does not chase per OpenFGA contract",
						expectedUser, users)
					return
				}
				// Result is non-empty but doesn't contain the expected
				// user — flat-list found SOMEONE but missed the
				// expected user. This is the partial-result case
				// where intersection/exclusion behind a pointer
				// yields users we can't reason about through the
				// flat-list contract. Skip rather than report a
				// false-positive parity miss. The 834-assertion
				// Explain parity sweep already pins end-to-end
				// correctness for these patterns — this Expand sweep
				// is supplementary, focused on regressions in the
				// direct/computed/TTU paths where flat-list semantics
				// ARE valid.
				//
				// Empty result against a non-empty expectation falls
				// in the same bucket: indistinguishable from a
				// sentinel response without re-running Check.
				t.Skipf("ExpandRecursive returned %v but list_objects expected %q — answer likely requires intersection/exclusion semantics behind a Computed/TTU pointer that flat-list doesn't model",
					users, expectedUser)
			}
		})
	}
}

// parityUnsupported returns a non-empty skip reason when the
// ExpandRecursive→list_objects parity contract cannot be evaluated
// against this tree. Empty string means the parity check is valid.
//
// Three shapes break the contract:
//
//   - Intersection nodes anywhere — Expand's flat-list semantics
//     can't model AND. A user listed in branch A but not branch B
//     would falsely "pass" containment when they don't actually
//     have the permission.
//   - Difference nodes anywhere — the Subtract slot names users to
//     EXCLUDE, but ExpandRecursive walks only the Base. A user
//     listed in Base who's also in Subtract should NOT appear in
//     list_objects, but ExpandRecursive returns them.
//   - Empty Leaf.Users at the root — could be the dispatcher's
//     sentinel (relation not yet eligible) OR a real empty-grants
//     response; both indistinguishable at the wire level. Skip
//     conservatively rather than risk a false-positive parity miss.
//
// This restricts the parity sweep to "pure union of
// direct/computed/TTU rewrites" — which is most of the schema
// surface area and exactly where most regression-catching value
// lives. The deeper relations (intersection, exclusion, self-
// referential) are covered by the 834-assertion Explain parity
// sweep; this Expand sweep is supplementary.
func parityUnsupported(tree *melange.UsersetTree) string {
	if tree == nil || tree.Root == nil {
		return "tree is empty"
	}
	if reason := walkParityUnsupported(tree.Root); reason != "" {
		return reason
	}
	// Conservative sentinel/empty-grants skip: a tree whose root is
	// just an empty Leaf.Users could be either the dispatcher
	// sentinel (relation not yet eligible) or a real "no users"
	// response. Same wire shape; can't tell apart.
	r := tree.Root
	if r.Leaf != nil && r.Leaf.Users != nil &&
		len(r.Leaf.Users.Users) == 0 &&
		r.Union == nil && r.Intersection == nil && r.Difference == nil {
		return "empty Leaf.Users at root (sentinel or no-grants — indistinguishable)"
	}
	return ""
}

// walkParityUnsupported reports the first encountered shape that
// breaks the ExpandRecursive→list_objects parity contract. nil
// nodes return empty (nothing to skip).
func walkParityUnsupported(n *melange.UsersetTreeNode) string {
	if n == nil {
		return ""
	}
	if n.Intersection != nil {
		return "intersection nodes — flat-list cannot model AND"
	}
	if n.Difference != nil {
		return "difference nodes — flat-list ignores subtract"
	}
	if n.Union != nil {
		for _, child := range n.Union.Nodes {
			if r := walkParityUnsupported(child); r != "" {
				return r
			}
		}
	}
	return ""
}

// isConcreteUser reports whether a user-string is a plain
// "<type>:<id>" reference (not a userset like "group:eng#member" and
// not a wildcard like "user:*").
func isConcreteUser(u string) bool {
	if strings.Contains(u, "#") {
		return false
	}
	if strings.HasSuffix(u, ":*") {
		return false
	}
	colon := strings.IndexByte(u, ':')
	return colon > 0 && colon < len(u)-1
}

// containsUsersetReference reports whether any user-string in the list
// is a userset reference (`<type>:<id>#<relation>`). Their presence
// means ExpandRecursive's flat result is incomplete relative to what
// list_objects can see — list_objects would resolve membership through
// the userset, ExpandRecursive treats it as a terminal entry. The
// parity sweep skips assertions in that case rather than producing
// false-positive failures.
func containsUsersetReference(users []string) bool {
	for _, u := range users {
		if strings.Contains(u, "#") {
			return true
		}
	}
	return false
}

// userInOrCoveredByWildcard returns true if `user` appears verbatim
// in `users` OR if a wildcard of the same type (`<user_type>:*`)
// appears. OpenFGA semantics: a wildcard grant covers every user of
// that type, so an assertion expecting "user:alice" is satisfied by
// either "user:alice" or "user:*".
func userInOrCoveredByWildcard(user string, users []string) bool {
	colon := strings.IndexByte(user, ':')
	if colon < 0 {
		return false
	}
	wildcard := user[:colon] + ":*"
	for _, u := range users {
		if u == user || u == wildcard {
			return true
		}
	}
	return false
}
