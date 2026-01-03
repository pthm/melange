package openfgatests_test

import (
	"context"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"
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

// TestOpenFGACheckSuite is a placeholder for running the official OpenFGA check test suite.
// Uncomment and configure when ready to run the full test suite.
func TestOpenFGACheckSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OpenFGA test suite in short mode")
	}

	t.Skip("OpenFGA test suite integration pending - run individual tests for now")

	// To run the official OpenFGA tests:
	// import "github.com/openfga/openfga/tests/check"
	// client := openfgatests.NewClient(t)
	// check.RunAllTests(t, client)
}
