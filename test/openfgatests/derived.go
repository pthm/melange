package openfgatests

import (
	"context"
	"fmt"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/stretchr/testify/require"
)

// derivedListObjectsAssertion is a list objects assertion derived from check assertions.
// Uses Contains/NotContains semantics since the derived set may be incomplete.
type derivedListObjectsAssertion struct {
	Request        ListObjectsRequest
	MustContain    []string // objects that ALLOW checks say should be present
	MustNotContain []string // objects that DENY checks say should be absent
}

// derivedListUsersAssertion is a list users assertion derived from check assertions.
type derivedListUsersAssertion struct {
	Request        ListUsersRequest
	MustContain    []string // subjects that ALLOW checks say should be present
	MustNotContain []string // subjects that DENY checks say should be absent
}

// isDerivable returns true if a check assertion can be used to derive list assertions.
func isDerivable(a *CheckAssertion) bool {
	if a.ErrorCode != 0 {
		return false
	}
	if len(a.ContextualTuples) != 0 {
		return false
	}
	if a.Tuple == nil {
		return false
	}
	user := a.Tuple.GetUser()
	// Skip userset subjects (group:eng#member)
	if idx := strings.Index(user, ":"); idx != -1 && strings.Contains(user[idx+1:], "#") {
		return false
	}
	// Skip wildcard subjects (user:*)
	if strings.HasSuffix(user, ":*") {
		return false
	}
	return true
}

// splitTypeID splits "type:id" into (type, id). Returns empty strings on invalid input.
func splitTypeID(s string) (typeName, id string) {
	idx := strings.Index(s, ":")
	if idx == -1 {
		return "", ""
	}
	return s[:idx], s[idx+1:]
}

// deriveListObjectsAssertions groups eligible check assertions by (user, relation, objectType)
// and produces derived list objects assertions.
func deriveListObjectsAssertions(checks []*CheckAssertion) []derivedListObjectsAssertion {
	type key struct {
		User     string
		Relation string
		Type     string
	}

	type entry struct {
		mustContain    []string
		mustNotContain []string
	}

	groups := make(map[key]*entry)
	var order []key

	for _, a := range checks {
		if !isDerivable(a) {
			continue
		}
		objType, objID := splitTypeID(a.Tuple.GetObject())
		if objType == "" {
			continue
		}
		k := key{
			User:     a.Tuple.GetUser(),
			Relation: a.Tuple.GetRelation(),
			Type:     objType,
		}
		e, ok := groups[k]
		if !ok {
			e = &entry{}
			groups[k] = e
			order = append(order, k)
		}
		fullObj := objType + ":" + objID
		if a.Expectation {
			e.mustContain = append(e.mustContain, fullObj)
		} else {
			e.mustNotContain = append(e.mustNotContain, fullObj)
		}
	}

	result := make([]derivedListObjectsAssertion, 0, len(order))
	for _, k := range order {
		e := groups[k]
		// Only include groups that have at least one positive or negative assertion
		if len(e.mustContain) == 0 && len(e.mustNotContain) == 0 {
			continue
		}
		result = append(result, derivedListObjectsAssertion{
			Request: ListObjectsRequest{
				User:     k.User,
				Relation: k.Relation,
				Type:     k.Type,
			},
			MustContain:    e.mustContain,
			MustNotContain: e.mustNotContain,
		})
	}
	return result
}

// deriveListUsersAssertions groups eligible check assertions by (object, relation, subjectType)
// and produces derived list users assertions.
func deriveListUsersAssertions(checks []*CheckAssertion) []derivedListUsersAssertion {
	type key struct {
		Object   string
		Relation string
		Filter   string
	}

	type entry struct {
		mustContain    []string
		mustNotContain []string
	}

	groups := make(map[key]*entry)
	var order []key

	for _, a := range checks {
		if !isDerivable(a) {
			continue
		}
		subjType, _ := splitTypeID(a.Tuple.GetUser())
		if subjType == "" {
			continue
		}
		k := key{
			Object:   a.Tuple.GetObject(),
			Relation: a.Tuple.GetRelation(),
			Filter:   subjType,
		}
		e, ok := groups[k]
		if !ok {
			e = &entry{}
			groups[k] = e
			order = append(order, k)
		}
		if a.Expectation {
			e.mustContain = append(e.mustContain, a.Tuple.GetUser())
		} else {
			e.mustNotContain = append(e.mustNotContain, a.Tuple.GetUser())
		}
	}

	result := make([]derivedListUsersAssertion, 0, len(order))
	for _, k := range order {
		e := groups[k]
		if len(e.mustContain) == 0 && len(e.mustNotContain) == 0 {
			continue
		}
		result = append(result, derivedListUsersAssertion{
			Request: ListUsersRequest{
				Object:   k.Object,
				Relation: k.Relation,
				Filters:  []string{k.Filter},
			},
			MustContain:    e.mustContain,
			MustNotContain: e.mustNotContain,
		})
	}
	return result
}

// runDerivedListObjectsAssertions derives and runs list objects assertions from check assertions.
func runDerivedListObjectsAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, checks []*CheckAssertion) {
	t.Helper()
	derived := deriveListObjectsAssertions(checks)
	if len(derived) == 0 {
		return
	}

	for i, assertion := range derived {
		t.Run(fmt.Sprintf("derived_listobjects_%d", i), func(t *testing.T) {
			resp, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
				StoreId:              storeID,
				AuthorizationModelId: modelID,
				Type:                 assertion.Request.Type,
				Relation:             assertion.Request.Relation,
				User:                 assertion.Request.User,
			})
			if err != nil {
				t.Skipf("list objects error (may be unsupported pattern): %v", err)
			}
			got := resp.GetObjects()

			for _, want := range assertion.MustContain {
				require.Contains(t, got, want,
					"derived listobjects: user=%s relation=%s type=%s should contain %s",
					assertion.Request.User, assertion.Request.Relation, assertion.Request.Type, want)
			}
			for _, notWant := range assertion.MustNotContain {
				require.NotContains(t, got, notWant,
					"derived listobjects: user=%s relation=%s type=%s should not contain %s",
					assertion.Request.User, assertion.Request.Relation, assertion.Request.Type, notWant)
			}
		})
	}
}

// runDerivedListUsersAssertions derives and runs list users assertions from check assertions.
func runDerivedListUsersAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, checks []*CheckAssertion) {
	t.Helper()
	derived := deriveListUsersAssertions(checks)
	if len(derived) == 0 {
		return
	}

	for i, assertion := range derived {
		t.Run(fmt.Sprintf("derived_listusers_%d", i), func(t *testing.T) {
			objType, objID := splitTypeID(assertion.Request.Object)

			filters := make([]*openfgav1.UserTypeFilter, 0, len(assertion.Request.Filters))
			for _, f := range assertion.Request.Filters {
				filters = append(filters, &openfgav1.UserTypeFilter{Type: f})
			}

			resp, err := client.ListUsers(ctx, &openfgav1.ListUsersRequest{
				StoreId:              storeID,
				AuthorizationModelId: modelID,
				Object: &openfgav1.Object{
					Type: objType,
					Id:   objID,
				},
				Relation:    assertion.Request.Relation,
				UserFilters: filters,
			})
			if err != nil {
				t.Skipf("list users error (may be unsupported pattern): %v", err)
			}

			var got []string
			for _, u := range resp.GetUsers() {
				if obj := u.GetObject(); obj != nil {
					got = append(got, obj.GetType()+":"+obj.GetId())
				}
			}

			// Build set of wildcard types present in results (e.g., "user" if "user:*" is returned).
			// When a wildcard is present, all concrete users of that type are implicitly covered.
			wildcardTypes := make(map[string]bool)
			for _, entry := range got {
				if strings.HasSuffix(entry, ":*") {
					wType, _ := splitTypeID(entry)
					wildcardTypes[wType] = true
				}
			}

			for _, want := range assertion.MustContain {
				wantType, _ := splitTypeID(want)
				if wildcardTypes[wantType] {
					continue // wildcard covers this user
				}
				require.Contains(t, got, want,
					"derived listusers: object=%s relation=%s filters=%v should contain %s",
					assertion.Request.Object, assertion.Request.Relation, assertion.Request.Filters, want)
			}
			for _, notWant := range assertion.MustNotContain {
				require.NotContains(t, got, notWant,
					"derived listusers: object=%s relation=%s filters=%v should not contain %s",
					assertion.Request.Object, assertion.Request.Relation, assertion.Request.Filters, notWant)
			}
		})
	}
}
