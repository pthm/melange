package openfgatests

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/stretchr/testify/require"
)

// runListCheckParityAssertions cross-references every list assertion in the stage
// against Check, so the two APIs are proven to agree on the same data. For each
// ListObjects / ListUsers assertion it runs the list op and, over a candidate
// universe drawn from the stage's tuples, asserts:
//
//   - list ⊆ check: every element the list returns is ALLOWED by Check. A denied
//     element that the list still returns is an over-report — the class of bug in
//     issue #80 (list_accessible_subjects returning subjects Check denies).
//   - check ⊆ list: every universe candidate that Check ALLOWS is returned by the
//     list. A candidate Check allows but the list omits is an under-report.
//
// The candidate universe is every "type:id" token that appears in the stage's
// tuples (either side), which is exactly the set of subjects/objects that can
// participate — enough to make the comparison exhaustive for these fixtures.
//
// Skipped (not failed), because faithfully replaying their semantics on the Check
// side is out of scope and those paths are covered by other sweeps:
//   - assertions with an error code (the list is expected to fail),
//   - assertions with contextual tuples,
//   - wildcard results ("type:*") — Check-cover semantics differ from element
//     equality; a wildcard grant covers every subject of the type.
func runListCheckParityAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, stage *Stage) {
	t.Helper()

	tokens := collectTokensByType(stage.Tuples)

	for i, a := range stage.ListObjectsAssertions {
		t.Run(fmt.Sprintf("list_check_parity_objects_%d", i), func(t *testing.T) {
			if a.ErrorCode != 0 || len(a.ContextualTuples) != 0 {
				t.Skip("error-coded or contextual-tuple assertion — no Check parity contract")
			}
			resp, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
				StoreId: storeID, AuthorizationModelId: modelID,
				Type: a.Request.Type, Relation: a.Request.Relation, User: a.Request.User,
			})
			require.NoError(t, err)
			got := resp.GetObjects()
			if containsWildcard(got) {
				t.Skip("wildcard result — Check-cover semantics out of scope")
			}

			// Universe: every object of the requested type, plus whatever the list
			// returned and the assertion expected.
			universe := map[string]bool{}
			for _, id := range tokens[a.Request.Type] {
				universe[a.Request.Type+":"+id] = true
			}
			for _, o := range got {
				universe[o] = true
			}
			for _, o := range a.Expectation {
				universe[o] = true
			}

			gotSet := toSet(got)
			var over, under []string
			for obj := range universe {
				if strings.HasSuffix(obj, ":*") {
					continue
				}
				allowed := checkAllowed(t, ctx, client, storeID, modelID, a.Request.User, a.Request.Relation, obj)
				if gotSet[obj] && !allowed {
					over = append(over, obj)
				}
				if allowed && !gotSet[obj] {
					under = append(under, obj)
				}
			}
			sort.Strings(over)
			sort.Strings(under)
			require.Empty(t, over,
				"list_objects OVER-reports vs check: user=%s relation=%s type=%s returned-but-check-denies=%v",
				a.Request.User, a.Request.Relation, a.Request.Type, over)
			require.Empty(t, under,
				"list_objects UNDER-reports vs check: user=%s relation=%s type=%s check-allows-but-missing=%v",
				a.Request.User, a.Request.Relation, a.Request.Type, under)
		})
	}

	for i, a := range stage.ListUsersAssertions {
		t.Run(fmt.Sprintf("list_check_parity_users_%d", i), func(t *testing.T) {
			if a.ErrorCode != 0 || len(a.ContextualTuples) != 0 {
				t.Skip("error-coded or contextual-tuple assertion — no Check parity contract")
			}
			objType, objID, ok := strings.Cut(a.Request.Object, ":")
			if !ok {
				t.Skipf("unparseable object %q", a.Request.Object)
			}
			filters := make([]*openfgav1.UserTypeFilter, 0, len(a.Request.Filters))
			for _, f := range a.Request.Filters {
				filters = append(filters, parseUserTypeFilter(f))
			}
			resp, err := client.ListUsers(ctx, &openfgav1.ListUsersRequest{
				StoreId: storeID, AuthorizationModelId: modelID,
				Object:      &openfgav1.Object{Type: objType, Id: objID},
				Relation:    a.Request.Relation,
				UserFilters: filters,
			})
			require.NoError(t, err)

			var got []string
			for _, u := range resp.GetUsers() {
				if s := userString(u); s != "" {
					got = append(got, s)
				}
			}
			if containsWildcard(got) {
				t.Skip("wildcard result — Check-cover semantics out of scope")
			}

			// Universe: for each requested filter, every candidate subject of that
			// filter's shape (concrete "type:id" or userset "type:id#relation")
			// drawn from the tuple tokens, plus the list result and expectation.
			universe := map[string]bool{}
			for _, f := range a.Request.Filters {
				ftype, frel, isUserset := strings.Cut(f, "#")
				for _, id := range tokens[ftype] {
					if isUserset {
						universe[ftype+":"+id+"#"+frel] = true
					} else {
						universe[ftype+":"+id] = true
					}
				}
			}
			for _, u := range got {
				universe[u] = true
			}
			for _, u := range a.Expectation {
				universe[u] = true
			}

			gotSet := toSet(got)
			var over, under []string
			for subj := range universe {
				if strings.HasSuffix(subj, ":*") {
					continue
				}
				allowed := checkAllowedSubject(t, ctx, client, storeID, modelID, subj, a.Request.Relation, a.Request.Object)
				if gotSet[subj] && !allowed {
					over = append(over, subj)
				}
				if allowed && !gotSet[subj] {
					under = append(under, subj)
				}
			}
			sort.Strings(over)
			sort.Strings(under)
			require.Empty(t, over,
				"list_users OVER-reports vs check: object=%s relation=%s filters=%v returned-but-check-denies=%v",
				a.Request.Object, a.Request.Relation, a.Request.Filters, over)
			require.Empty(t, under,
				"list_users UNDER-reports vs check: object=%s relation=%s filters=%v check-allows-but-missing=%v",
				a.Request.Object, a.Request.Relation, a.Request.Filters, under)
		})
	}
}

// collectTokensByType returns, per type, the set of ids that appear in any tuple
// (either the user or object side). Userset subjects ("group:g#member") contribute
// their base id ("g" under type "group"); wildcards ("user:*") are dropped.
func collectTokensByType(tuples []*openfgav1.TupleKey) map[string][]string {
	sets := map[string]map[string]bool{}
	add := func(tok string) {
		if tok == "" {
			return
		}
		if h := strings.IndexByte(tok, '#'); h != -1 {
			tok = tok[:h] // strip "#relation" from a userset subject
		}
		typ, id, ok := strings.Cut(tok, ":")
		if !ok || id == "" || id == "*" {
			return
		}
		if sets[typ] == nil {
			sets[typ] = map[string]bool{}
		}
		sets[typ][id] = true
	}
	for _, tp := range tuples {
		add(tp.GetUser())
		add(tp.GetObject())
	}
	out := map[string][]string{}
	for typ, ids := range sets {
		for id := range ids {
			out[typ] = append(out[typ], id)
		}
		sort.Strings(out[typ])
	}
	return out
}

func checkAllowed(t *testing.T, ctx context.Context, client *Client, storeID, modelID, user, relation, object string) bool {
	return checkAllowedSubject(t, ctx, client, storeID, modelID, user, relation, object)
}

func checkAllowedSubject(t *testing.T, ctx context.Context, client *Client, storeID, modelID, subject, relation, object string) bool {
	t.Helper()
	resp, err := client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID, AuthorizationModelId: modelID,
		TupleKey: &openfgav1.CheckRequestTupleKey{User: subject, Relation: relation, Object: object},
	})
	require.NoError(t, err, "check %s#%s@%s", subject, relation, object)
	return resp.GetAllowed()
}

func toSet(xs []string) map[string]bool {
	s := make(map[string]bool, len(xs))
	for _, x := range xs {
		s[x] = true
	}
	return s
}

func containsWildcard(xs []string) bool {
	for _, x := range xs {
		if strings.HasSuffix(x, ":*") {
			return true
		}
	}
	return false
}
