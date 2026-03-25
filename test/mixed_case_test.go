package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/test/testutil"
)

// TestMixedCaseRelations_RemigrateSurvives verifies that relations with uppercase
// characters don't get dropped on re-migration. This is a regression test for #26
// where CollectFunctionNames preserved the original casing but PostgreSQL stores
// function names as lowercase, causing the orphan detection to drop valid functions.
func TestMixedCaseRelations_RemigrateSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	const schema = `
model
  schema 1.1

type user

type organization
  relations
    define Member: [user]
    define Viewer: [user] or Member
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	types, err := parser.ParseSchemaString(schema)
	require.NoError(t, err)

	m := migrator.NewMigrator(db, "")

	// Use MigrateWithTypesAndOptions which includes orphan detection,
	// unlike MigrateWithTypes which only does CREATE OR REPLACE.
	opts := migrator.InternalMigrateOptions{
		SchemaContent: schema,
	}

	// First migration: creates the functions.
	err = m.MigrateWithTypesAndOptions(ctx, types, opts)
	require.NoError(t, err)

	expectedFunctions := []string{
		"check_organization_member",
		"check_organization_member_nw",
		"check_organization_viewer",
		"check_organization_viewer_nw",
		"list_organization_member_obj",
		"list_organization_member_sub",
		"list_organization_viewer_obj",
		"list_organization_viewer_sub",
		"check_permission",
	}

	for _, fn := range expectedFunctions {
		assert.True(t, functionExists(t, ctx, db, fn), "function %s should exist", fn)
	}

	// Second migration (forced): triggers orphan detection which previously
	// dropped functions due to case mismatch between CollectFunctionNames
	// (which preserved original casing) and pg_proc (which stores lowercase).
	opts.Force = true
	err = m.MigrateWithTypesAndOptions(ctx, types, opts)
	require.NoError(t, err)

	for _, fn := range expectedFunctions {
		assert.True(t, functionExists(t, ctx, db, fn), "function %s should exist after re-migration", fn)
	}
}
