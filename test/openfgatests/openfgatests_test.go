package openfgatests_test

import (
	"context"
	"os"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
	"github.com/openfga/openfga/tests/check"
	"github.com/openfga/openfga/tests/listobjects"
	"github.com/openfga/openfga/tests/listusers"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/openfgatests"
)

// TestBasicCheck verifies basic permission checks work correctly.
func TestBasicCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := openfgatests.NewClient(t)

	// Create store
	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
		Name: "test",
	})
	require.NoError(t, err)
	storeID := storeResp.GetId()

	// Parse DSL to model
	model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`)
	require.NoError(t, err)

	// Write model
	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		TypeDefinitions: model.GetTypeDefinitions(),
		SchemaVersion:   model.GetSchemaVersion(),
		Conditions:      model.GetConditions(),
	})
	require.NoError(t, err)

	// Write tuples
	_, err = client.Write(ctx, &openfgav1.WriteRequest{
		StoreId: storeID,
		Writes: &openfgav1.WriteRequestWrites{
			TupleKeys: []*openfgav1.TupleKey{
				{User: "user:alice", Relation: "viewer", Object: "document:1"},
			},
		},
	})
	require.NoError(t, err)

	// Check - should allow
	checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:alice",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "alice should have viewer permission")

	// Check - should deny
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:bob",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.False(t, checkResp.GetAllowed(), "bob should not have viewer permission")
}

// TestRoleHierarchy verifies implied relations work correctly.
func TestRoleHierarchy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := openfgatests.NewClient(t)

	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
		Name: "hierarchy",
	})
	require.NoError(t, err)
	storeID := storeResp.GetId()

	model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`)
	require.NoError(t, err)

	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		TypeDefinitions: model.GetTypeDefinitions(),
		SchemaVersion:   model.GetSchemaVersion(),
		Conditions:      model.GetConditions(),
	})
	require.NoError(t, err)

	_, err = client.Write(ctx, &openfgav1.WriteRequest{
		StoreId: storeID,
		Writes: &openfgav1.WriteRequestWrites{
			TupleKeys: []*openfgav1.TupleKey{
				{User: "user:owner", Relation: "owner", Object: "document:1"},
				{User: "user:editor", Relation: "editor", Object: "document:1"},
			},
		},
	})
	require.NoError(t, err)

	// Owner should have viewer permission (via owner -> editor -> viewer)
	checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:owner",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "owner should have viewer permission through hierarchy")

	// Editor should have viewer permission
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:editor",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "editor should have viewer permission through hierarchy")

	// Owner should also have editor permission
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:owner",
			Relation: "editor",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "owner should have editor permission through hierarchy")
}

// TestParentInheritance verifies tuple-to-userset (parent inheritance) works correctly.
func TestParentInheritance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := openfgatests.NewClient(t)

	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
		Name: "inheritance",
	})
	require.NoError(t, err)
	storeID := storeResp.GetId()

	model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type org
  relations
    define member: [user]

type repo
  relations
    define org: [org]
    define reader: [user] or member from org
`)
	require.NoError(t, err)

	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		TypeDefinitions: model.GetTypeDefinitions(),
		SchemaVersion:   model.GetSchemaVersion(),
		Conditions:      model.GetConditions(),
	})
	require.NoError(t, err)

	_, err = client.Write(ctx, &openfgav1.WriteRequest{
		StoreId: storeID,
		Writes: &openfgav1.WriteRequestWrites{
			TupleKeys: []*openfgav1.TupleKey{
				{User: "user:alice", Relation: "member", Object: "org:acme"},
				{User: "org:acme", Relation: "org", Object: "repo:code"},
			},
		},
	})
	require.NoError(t, err)

	// Alice should have reader on repo through org membership
	checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:alice",
			Relation: "reader",
			Object:   "repo:code",
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "alice should have reader permission through org membership")

	// Bob should not have access
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:bob",
			Relation: "reader",
			Object:   "repo:code",
		},
	})
	require.NoError(t, err)
	require.False(t, checkResp.GetAllowed(), "bob should not have reader permission")
}

// TestListObjects verifies list objects functionality.
func TestListObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := openfgatests.NewClient(t)

	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
		Name: "listobjects",
	})
	require.NoError(t, err)
	storeID := storeResp.GetId()

	model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`)
	require.NoError(t, err)

	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		TypeDefinitions: model.GetTypeDefinitions(),
		SchemaVersion:   model.GetSchemaVersion(),
		Conditions:      model.GetConditions(),
	})
	require.NoError(t, err)

	_, err = client.Write(ctx, &openfgav1.WriteRequest{
		StoreId: storeID,
		Writes: &openfgav1.WriteRequestWrites{
			TupleKeys: []*openfgav1.TupleKey{
				{User: "user:alice", Relation: "viewer", Object: "document:1"},
				{User: "user:alice", Relation: "viewer", Object: "document:2"},
				{User: "user:alice", Relation: "viewer", Object: "document:3"},
				{User: "user:bob", Relation: "viewer", Object: "document:2"},
			},
		},
	})
	require.NoError(t, err)

	// Alice should see 3 documents
	listResp, err := client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
		StoreId:  storeID,
		Type:     "document",
		Relation: "viewer",
		User:     "user:alice",
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetObjects(), 3, "alice should see 3 documents")

	// Bob should see 1 document
	listResp, err = client.ListObjects(ctx, &openfgav1.ListObjectsRequest{
		StoreId:  storeID,
		Type:     "document",
		Relation: "viewer",
		User:     "user:bob",
	})
	require.NoError(t, err)
	require.Len(t, listResp.GetObjects(), 1, "bob should see 1 document")
}

// TestContextualTuples verifies contextual tuples are handled correctly.
func TestContextualTuples(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	client := openfgatests.NewClient(t)

	storeResp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{
		Name: "contextual",
	})
	require.NoError(t, err)
	storeID := storeResp.GetId()

	model, err := transformer.TransformDSLToProto(`
model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`)
	require.NoError(t, err)

	_, err = client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
		StoreId:         storeID,
		TypeDefinitions: model.GetTypeDefinitions(),
		SchemaVersion:   model.GetSchemaVersion(),
		Conditions:      model.GetConditions(),
	})
	require.NoError(t, err)

	// No stored tuples - check should fail
	checkResp, err := client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:alice",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.False(t, checkResp.GetAllowed(), "alice should not have permission without tuples")

	// Check with contextual tuple - should succeed
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:alice",
			Relation: "viewer",
			Object:   "document:1",
		},
		ContextualTuples: &openfgav1.ContextualTupleKeys{
			TupleKeys: []*openfgav1.TupleKey{
				{User: "user:alice", Relation: "viewer", Object: "document:1"},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, checkResp.GetAllowed(), "alice should have permission with contextual tuple")

	// Original check should still fail (contextual tuples are not persisted)
	checkResp, err = client.Check(ctx, &openfgav1.CheckRequest{
		StoreId: storeID,
		TupleKey: &openfgav1.CheckRequestTupleKey{
			User:     "user:alice",
			Relation: "viewer",
			Object:   "document:1",
		},
	})
	require.NoError(t, err)
	require.False(t, checkResp.GetAllowed(), "alice should not have permission after contextual tuple expires")
}

// =============================================================================
// Supported Feature Tests
// These test patterns cover features that Melange fully supports.
// See .claude/specs/openfga-dsl-support.md for the full feature matrix.
// =============================================================================

// TestOpenFGA_DirectAssignment tests direct relation assignment [user].
// This is the most basic pattern: explicitly granting a relation via tuples.
func TestOpenFGA_DirectAssignment(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Core direct assignment tests
	openfgatests.RunTestByName(t, client, "this")
	openfgatests.RunTestByName(t, client, "this_with_contextual_tuples")
	openfgatests.RunTestByName(t, client, "this_and_union")
}

// TestOpenFGA_ComputedUserset tests computed relations (role hierarchy via implied_by).
// Pattern: define admin: [user] or owner (owner implies admin)
func TestOpenFGA_ComputedUserset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Basic computed userset
	openfgatests.RunTestByName(t, client, "computed_userset")
	openfgatests.RunTestByName(t, client, "computed_userset_and_union")
	openfgatests.RunTestByName(t, client, "computed_userset_and_exclusion")
	openfgatests.RunTestByName(t, client, "computed_userset_and_computed_userset")
}

// TestOpenFGA_TupleToUserset tests parent inheritance (FROM pattern).
// Pattern: define can_read: can_read from org
func TestOpenFGA_TupleToUserset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Basic tuple-to-userset (parent inheritance)
	openfgatests.RunTestByName(t, client, "tuple_to_userset")
	openfgatests.RunTestByName(t, client, "tuple_to_userset_and_union")
	openfgatests.RunTestByName(t, client, "tuple_to_userset_and_exclusion")
	openfgatests.RunTestByName(t, client, "tuple_to_userset_and_computed_userset")
	openfgatests.RunTestByName(t, client, "tuple_to_userset_and_tuple_to_userset")

	// Note: The following tests use userset references [type#relation] which
	// we only partially support. See .claude/specs/openfga-dsl-support.md
	// - ttu_mix_with_userset
	// - ttu_multiple_parents
	// - ttu_to_ttu
	// - ttu_to_userset
}

// TestOpenFGA_Wildcards tests public access via wildcards [user:*].
// Pattern: define public: [user:*]
func TestOpenFGA_Wildcards(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Wildcard tests (passing)
	openfgatests.RunTestByName(t, client, "wildcard_direct")
	openfgatests.RunTestByName(t, client, "wildcard_computed_userset")
	openfgatests.RunTestByName(t, client, "simple_ttu_child_wildcard")
	openfgatests.RunTestByName(t, client, "combined_public_wildcard_userset")

	// Note: The following tests use userset references [type#relation] which
	// we only partially support. See .claude/specs/openfga-dsl-support.md
	// - wildcard_and_userset_restriction
	// - simple_userset_child_wildcard
}

// TestOpenFGA_Exclusion tests the BUT NOT pattern.
// Pattern: define can_review: can_read but not author
func TestOpenFGA_Exclusion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Exclusion tests (passing)
	openfgatests.RunTestByName(t, client, "exclusion_and_computed_userset")
	openfgatests.RunTestByName(t, client, "exclusion_and_union_in_base")
	openfgatests.RunTestByName(t, client, "exclusion_and_tuple_to_userset_in_base")
	openfgatests.RunTestByName(t, client, "exclusion_for_some_relations")

	// Note: The following tests have complex exclusion patterns that need investigation:
	// - exclusion_and_exclusion_in_base (check_1 fails)
	// - exclusion_and_exclusion_in_subtract
	// - exclusion_and_union_in_subtract
	// - exclusion_and_tuple_to_userset_in_subtract
	// - exclusion_between_userset_and_type (uses userset references)
}

// TestOpenFGA_Union tests the OR pattern.
// Pattern: define viewer: [user] or editor or admin
func TestOpenFGA_Union(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Union tests
	openfgatests.RunTestByName(t, client, "union_and_union")
	openfgatests.RunTestByName(t, client, "union_and_exclusion")
	openfgatests.RunTestByName(t, client, "union_and_tuple_to_userset")
}

// TestOpenFGA_ComplexPatterns tests combinations of supported features.
func TestOpenFGA_ComplexPatterns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Complex patterns combining multiple features (passing)
	openfgatests.RunTestByName(t, client, "nested_usersets_are_recursively_expanded")
	openfgatests.RunTestByName(t, client, "nested_ttu_involving_exclusion")

	// Note: The following tests use unsupported patterns or have issues:
	// - three_prong_relation (cyclic schema in parent relations)
	// - evaluate_userset_in_computed_relation_of_ttu (userset reference - partially supported)
	// - relation_with_userset_involving_exclusion (userset reference - now supported)
	// - relation_with_wildcard_involving_exclusion (wildcard+exclusion edge case)
}

// TestOpenFGA_UsersetReferences tests userset references [type#relation].
// Pattern: define viewer: [group#member] means members of groups can be viewers.
func TestOpenFGA_UsersetReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Basic userset reference tests (all passing)
	openfgatests.RunTestByName(t, client, "userset_as_user")
	openfgatests.RunTestByName(t, client, "simple_userset_child_wildcard")
	openfgatests.RunTestByName(t, client, "simple_userset_child_wildcard_only")
	openfgatests.RunTestByName(t, client, "userset_discard_invalid")
	openfgatests.RunTestByName(t, client, "userset_discard_invalid_wildcard")
	openfgatests.RunTestByName(t, client, "nested_usersets_are_recursively_expanded")
	openfgatests.RunTestByName(t, client, "relation_with_userset_involving_exclusion")
	openfgatests.RunTestByName(t, client, "userset_orphan_parent")

	// Note: The following tests have complex patterns that need further investigation:
	// - simple_userset_child_computed_userset (computed userset nested in userset)
	// - ttu_mix_with_userset (complex nested TTU with userset)
	// - ttu_to_userset, userset_to_ttu, userset_to_userset (chained patterns)
	// - userset_with_intersection_in_computed_relation_of_ttu (intersection - Phase 3)
}

// TestOpenFGA_CycleHandling tests that cycles are handled correctly.
func TestOpenFGA_CycleHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := openfgatests.NewClient(t)

	// Cycle handling tests (passing)
	openfgatests.RunTestByName(t, client, "immediate_cycle_return_false")
	openfgatests.RunTestByName(t, client, "cycle_or_cycle_return_false")
	openfgatests.RunTestByName(t, client, "false_butnot_cycle_return_false")

	// Note: The following tests involve AND patterns (intersection) we don't support:
	// - cycle_and_true_return_false (AND pattern)
	// - true_butnot_cycle_return_false (cycle in subtracted relation)
}

// =============================================================================
// Full Test Suites (use sparingly - these run many tests)
// =============================================================================

// TestOpenFGACheckSuite runs the full official OpenFGA check test suite.
// Use -run TestOpenFGACheckSuite to run all tests.
func TestOpenFGACheckSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}
	client := openfgatests.NewClient(t)
	check.RunAllTests(t, client)
}

// TestOpenFGAListObjectsSuite runs the full official OpenFGA list objects test suite.
func TestOpenFGAListObjectsSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}
	client := openfgatests.NewClient(t)
	listobjects.RunAllTests(t, client)
}

// TestOpenFGAListUsersSuite runs the full official OpenFGA list users test suite.
func TestOpenFGAListUsersSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}
	client := openfgatests.NewClient(t)
	listusers.RunAllTests(t, client)
}

// =============================================================================
// Helper Tests
// =============================================================================

// TestOpenFGAByName runs a specific test by exact name.
// Use: OPENFGA_TEST_NAME=wildcard_direct go test -run TestOpenFGAByName ./openfgatests/...
// Or:  just test-openfga-name wildcard_direct
func TestOpenFGAByName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	name := os.Getenv("OPENFGA_TEST_NAME")
	if name == "" {
		name = "this" // default test
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestByName(t, client, name)
}

// TestOpenFGAByPattern runs tests matching a regex pattern.
// Use: OPENFGA_TEST_PATTERN="^wildcard" go test -run TestOpenFGAByPattern ./openfgatests/...
// Or:  just test-openfga-pattern "^wildcard"
func TestOpenFGAByPattern(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	pattern := os.Getenv("OPENFGA_TEST_PATTERN")
	if pattern == "" {
		pattern = "^computed_userset$" // default pattern
	}
	client := openfgatests.NewClient(t)
	openfgatests.RunTestsByPattern(t, client, pattern)
}

// TestOpenFGAListAvailableTests prints all available test names (useful for discovery).
func TestOpenFGAListAvailableTests(t *testing.T) {
	names, err := openfgatests.ListTestNames()
	require.NoError(t, err)

	t.Logf("Available OpenFGA tests (%d total):", len(names))
	for _, name := range names {
		t.Logf("  - %s", name)
	}
}
