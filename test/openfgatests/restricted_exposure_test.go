package openfgatests

import (
	"context"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
	"github.com/openfga/openfga/pkg/typesystem"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
)

// A case's stages share one database, so under a restricted exposure each
// stage must start without the functions an earlier stage's exposure
// installed; otherwise a callee missing from a later stage's closure would
// resolve to the leftover copy and the run could not catch it.
func TestRestrictedExposureStagesStartClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	client := NewRestrictedExposureClient(t, "melange")
	ctx := context.Background()
	resp, err := client.CreateStore(ctx, &openfgav1.CreateStoreRequest{Name: "stages"})
	require.NoError(t, err)
	model := testutils.MustTransformDSLToProtoWithID(`model
  schema 1.1
type user
type document
  relations
    define editor: [user]
    define viewer: [user]
`)

	installed := func(name string) bool {
		var exists bool
		require.NoError(t, client.StoreDB(resp.GetId()).QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = 'melange' AND p.proname = $1)`, name).Scan(&exists))
		return exists
	}
	stage := func(relation string) {
		client.exposure = &migrator.Exposure{Relations: compiler.RelationPatterns{Include: []string{"document#" + relation}}}
		_, err := client.WriteAuthorizationModel(ctx, &openfgav1.WriteAuthorizationModelRequest{
			StoreId:         resp.GetId(),
			SchemaVersion:   typesystem.SchemaVersion1_1,
			TypeDefinitions: model.GetTypeDefinitions(),
		})
		require.NoError(t, err)
	}

	stage("viewer")
	require.True(t, installed("check_document_viewer"))
	stage("editor")
	require.True(t, installed("check_document_editor"))
	require.False(t, installed("check_document_viewer"), "the first stage's functions are gone")
}
