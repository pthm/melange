package test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// --- Helpers ---

// compiledSchema holds the outputs of the full compilation pipeline for a schema.
type compiledSchema struct {
	genSQL         compiler.GeneratedSQL
	listSQL        compiler.ListGeneratedSQL
	functionNames  []string
	namedFunctions []compiler.NamedFunction
}

// compilePipeline runs the full compilation pipeline for a schema string.
func compilePipeline(t *testing.T, schemaContent string) compiledSchema {
	t.Helper()

	types, err := parser.ParseSchemaString(schemaContent)
	require.NoError(t, err, "parsing schema")

	return compilePipelineFromTypes(t, types)
}

// compilePipelineFromTypes runs the full compilation pipeline for pre-parsed type definitions.
// This allows both single-file and modular schemas to share the same pipeline.
func compilePipelineFromTypes(t *testing.T, types []schema.TypeDefinition) compiledSchema {
	t.Helper()

	require.NoError(t, schema.DetectCycles(types), "schema has cycles")

	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inlineData := compiler.BuildInlineSQLData(closureRows, analyses)

	genSQL, err := compiler.GenerateSQL(analyses, inlineData, "")
	require.NoError(t, err, "generating check SQL")

	listSQL, err := compiler.GenerateListSQL(analyses, inlineData, "")
	require.NoError(t, err, "generating list SQL")

	return compiledSchema{
		genSQL:         genSQL,
		listSQL:        listSQL,
		functionNames:  compiler.CollectFunctionNames(analyses),
		namedFunctions: compiler.CollectNamedFunctions(genSQL, listSQL, analyses),
	}
}

// fullMigration compiles a schema and generates a full (initial) migration.
func fullMigration(t *testing.T, schemaContent, version string) (compiledSchema, compiler.MigrationSQL) {
	t.Helper()
	cs := compilePipeline(t, schemaContent)
	return cs, generateMigration(cs, migrator.ComputeSchemaChecksum(schemaContent), version, nil)
}

// fullMigrationFromTypes generates a full migration from pre-parsed type definitions.
func fullMigrationFromTypes(t *testing.T, types []schema.TypeDefinition, checksum, version string) (compiledSchema, compiler.MigrationSQL) {
	t.Helper()
	cs := compilePipelineFromTypes(t, types)
	return cs, generateMigration(cs, checksum, version, nil)
}

// incrementalMigration compiles a schema and generates an incremental migration
// relative to a previous compiled schema state.
func incrementalMigration(t *testing.T, schemaContent, version string, prev compiledSchema) (compiledSchema, compiler.MigrationSQL) {
	t.Helper()
	cs := compilePipeline(t, schemaContent)
	return cs, generateMigration(cs, migrator.ComputeSchemaChecksum(schemaContent), version, &prev)
}

// incrementalMigrationFromTypes generates an incremental migration from pre-parsed
// type definitions relative to a previous compiled schema state.
func incrementalMigrationFromTypes(t *testing.T, types []schema.TypeDefinition, checksum, version string, prev compiledSchema) (compiledSchema, compiler.MigrationSQL) {
	t.Helper()
	cs := compilePipelineFromTypes(t, types)
	return cs, generateMigration(cs, checksum, version, &prev)
}

// generateMigration builds MigrationSQL from a compiled schema. When prev is non-nil,
// comparison fields are populated to produce an incremental migration; otherwise a
// full (initial) migration is generated.
func generateMigration(cs compiledSchema, checksum, version string, prev *compiledSchema) compiler.MigrationSQL {
	opts := compiler.MigrationOptions{
		Version:        version,
		SchemaChecksum: checksum,
		CodegenVersion: migrator.CodegenVersion(),
		NamedFunctions: cs.namedFunctions,
	}
	if prev != nil {
		opts.PreviousFunctionNames = prev.functionNames
		opts.PreviousChecksums = migrator.ComputeFunctionChecksums(prev.namedFunctions)
		opts.PreviousSource = "test"
	}
	return compiler.GenerateMigrationSQL(cs.genSQL, cs.listSQL, cs.functionNames, opts)
}

// parseModularSchema parses a modular schema from in-memory module contents.
func parseModularSchema(t *testing.T, modules map[string]string) []schema.TypeDefinition {
	t.Helper()
	types, err := parser.ParseModularSchemaFromStrings(modules, "1.2")
	require.NoError(t, err, "parsing modular schema")
	return types
}

// applyMigrationUp executes the UP SQL from a MigrationSQL against a database.
func applyMigrationUp(t *testing.T, ctx context.Context, db *sql.DB, migration compiler.MigrationSQL) {
	t.Helper()
	_, err := db.ExecContext(ctx, migration.Up)
	require.NoError(t, err, "applying UP migration")
}

// applyMigrationDown executes the DOWN SQL from a MigrationSQL against a database.
func applyMigrationDown(t *testing.T, ctx context.Context, db *sql.DB, migration compiler.MigrationSQL) {
	t.Helper()
	_, err := db.ExecContext(ctx, migration.Down)
	require.NoError(t, err, "applying DOWN migration")
}

// getFunctionNames returns all melange-related function names from pg_proc.
func getFunctionNames(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = current_schema()
		AND (
			p.proname LIKE 'check_%'
			OR p.proname LIKE 'list_%'
			OR p.proname LIKE 'explain_%'
			OR p.proname LIKE 'expand_%'
		)
		ORDER BY p.proname
	`)
	require.NoError(t, err)
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, rows.Err())
	return names
}

// functionExists checks if a specific function exists in pg_proc.
func functionExists(t *testing.T, ctx context.Context, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = current_schema()
			AND p.proname = $1
		)
	`, name).Scan(&exists)
	require.NoError(t, err)
	return exists
}

// assertFunctions asserts that every named function exists in pg_proc.
func assertFunctions(t *testing.T, ctx context.Context, db *sql.DB, names map[string]string) {
	t.Helper()
	for name, msg := range names {
		assert.True(t, functionExists(t, ctx, db, name), msg)
	}
}

// assertFunctionsDropped asserts that none of the named functions exist in pg_proc.
func assertFunctionsDropped(t *testing.T, ctx context.Context, db *sql.DB, names map[string]string) {
	t.Helper()
	for name, msg := range names {
		assert.False(t, functionExists(t, ctx, db, name), msg)
	}
}

// migrateSchema parses a schema string and applies it via the built-in migrator.
func migrateSchema(t *testing.T, ctx context.Context, m *migrator.Migrator, schemaContent string, opts migrator.InternalMigrateOptions) {
	t.Helper()
	types, err := parser.ParseSchemaString(schemaContent)
	require.NoError(t, err)
	opts.SchemaContent = schemaContent
	err = m.MigrateWithTypesAndOptions(ctx, types, opts)
	require.NoError(t, err, "migration should succeed")
}

// migrationRecordCount returns the number of rows in melange_migrations.
func migrationRecordCount(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var count int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM melange_migrations").Scan(&count)
	require.NoError(t, err)
	return count
}

// --- Schemas used in tests ---

const schemaV1 = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`

const schemaV2 = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`

const schemaV3 = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor

type folder
  relations
    define owner: [user]
    define viewer: [user] or owner
`

// schemaIntersectionExclusion exercises slice 1.5's intersection and
// exclusion emissions. The generated explain bodies contain the
// intersection FOR-equivalent (per-part recursive calls) plus the
// success-return helper's exclusion wrapping. Verifies the SQL is valid
// in PG.
const schemaIntersectionExclusion = `model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define banned: [user]
    define both: writer and editor
    define safe_viewer: writer but not banned
`

// TestMigration_IntersectionExclusionSchema applies a schema with both
// intersection and exclusion patterns and confirms the migrate succeeds.
func TestMigration_IntersectionExclusionSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	cs, migration := fullMigration(t, schemaIntersectionExclusion, "v1.5.0")
	applyMigrationUp(t, ctx, db, migration)

	assertFunctions(t, ctx, db, map[string]string{
		"explain_document_both":        "intersection wrapper should generate an explain function",
		"explain_document_safe_viewer": "exclusion wrapper should generate an explain function",
		"explain_permission":           "dispatcher should exist",
	})

	installedFunctions := getFunctionNames(t, ctx, db)
	for _, expected := range cs.functionNames {
		assert.Contains(t, installedFunctions, expected, "function %s should be installed", expected)
	}
}

// schemaUserset exercises the explain renderer's userset (`[group#member]`)
// emission against real PG. The generated explain_document_viewer body
// contains a FOR loop over grant tuples with userset references and a
// recursive call into explain_permission_internal for the membership
// relation; this test ensures the SQL parses and applies cleanly.
const schemaUserset = `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type document
  relations
    define viewer: [user, group#member]
`

// --- Tests ---

// TestMigration_UsersetSchema applies a userset schema and confirms the
// explain function for document.viewer is installed. Real value of this
// test is that PG would refuse the generated SQL if the userset FOR-loop
// emission were malformed — a syntactic regression in
// buildExplainUsersetLoopStmt would surface as a migration failure here.
func TestMigration_UsersetSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	cs, migration := fullMigration(t, schemaUserset, "v1.4.0")
	applyMigrationUp(t, ctx, db, migration)

	assertFunctions(t, ctx, db, map[string]string{
		"explain_document_viewer": "userset wrapper should generate an explain function",
		"explain_group_member":    "userset target relation should also generate an explain function",
		"explain_permission":      "dispatcher should exist",
	})

	installedFunctions := getFunctionNames(t, ctx, db)
	for _, expected := range cs.functionNames {
		assert.Contains(t, installedFunctions, expected, "function %s should be installed", expected)
	}
}

// TestMigration_InitialApply verifies that a first-time migration (full mode)
// installs all expected functions into the database.
func TestMigration_InitialApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	cs, migration := fullMigration(t, schemaV1, "v0.7.3")

	// UP SQL should be non-empty and contain expected markers
	assert.Contains(t, migration.Up, "-- Melange Migration (UP)")
	assert.Contains(t, migration.Up, "Melange version: v0.7.3")

	applyMigrationUp(t, ctx, db, migration)

	// Verify specific functions were created
	assertFunctions(t, ctx, db, map[string]string{
		"check_permission":      "check_permission dispatcher should exist",
		"check_document_owner":  "check_document_owner should exist",
		"check_document_viewer": "check_document_viewer should exist",
	})

	// Verify all expected functions exist
	installedFunctions := getFunctionNames(t, ctx, db)
	for _, expected := range cs.functionNames {
		assert.Contains(t, installedFunctions, expected, "function %s should be installed", expected)
	}
}

// TestMigration_DownRemovesAll verifies that a DOWN migration drops all functions.
func TestMigration_DownRemovesAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	_, migration := fullMigration(t, schemaV1, "v0.7.3")

	applyMigrationUp(t, ctx, db, migration)
	applyMigrationDown(t, ctx, db, migration)

	remaining := getFunctionNames(t, ctx, db)
	assert.Empty(t, remaining, "all functions should be dropped after DOWN migration")
}

// TestMigration_IncrementalUpdate verifies that modifying a schema and generating
// a second migration with comparison mode correctly updates functions and drops orphans.
func TestMigration_IncrementalUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Apply V1 (full mode, initial migration)
	csV1, migV1 := fullMigration(t, schemaV1, "v0.7.3")
	applyMigrationUp(t, ctx, db, migV1)

	assert.True(t, functionExists(t, ctx, db, "check_document_viewer"), "V1 should have viewer")
	assert.False(t, functionExists(t, ctx, db, "check_document_editor"), "V1 should not have editor")

	// Generate and apply V2 (comparison mode with orphan + change detection)
	_, migV2 := incrementalMigration(t, schemaV2, "v0.7.4", csV1)
	applyMigrationUp(t, ctx, db, migV2)

	assertFunctions(t, ctx, db, map[string]string{
		"check_document_editor": "V2 should have editor",
		"check_document_viewer": "V2 should still have viewer",
		"check_document_owner":  "V2 should still have owner",
		"check_permission":      "dispatcher should still exist",
	})
}

// TestMigration_IncrementalAddType verifies adding a new type (folder) to an existing schema.
func TestMigration_IncrementalAddType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Apply V2 first
	csV2, migV2 := fullMigration(t, schemaV2, "v0.7.4")
	applyMigrationUp(t, ctx, db, migV2)

	// Apply V3 (adds folder type)
	_, migV3 := incrementalMigration(t, schemaV3, "v0.7.5", csV2)
	applyMigrationUp(t, ctx, db, migV3)

	assertFunctions(t, ctx, db, map[string]string{
		"check_document_owner":  "document owner should exist",
		"check_document_editor": "document editor should exist",
		"check_document_viewer": "document viewer should exist",
		"check_folder_owner":    "folder owner should exist",
		"check_folder_viewer":   "folder viewer should exist",
		"check_permission":      "dispatcher should exist",
	})
}

// TestMigration_RemoveType verifies removing a type drops orphaned functions.
func TestMigration_RemoveType(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Apply V3 (document + folder)
	csV3, migV3 := fullMigration(t, schemaV3, "v0.7.5")
	applyMigrationUp(t, ctx, db, migV3)

	assertFunctions(t, ctx, db, map[string]string{
		"check_folder_owner":  "folder owner should exist",
		"check_folder_viewer": "folder viewer should exist",
	})

	// Go back to V2 (removes folder type)
	_, migV2 := incrementalMigration(t, schemaV2, "v0.7.6", csV3)

	// The UP migration should contain DROP statements for folder functions
	assert.Contains(t, migV2.Up, "Drop removed functions", "should have orphan drop section")
	assert.Contains(t, migV2.Up, "check_folder_owner", "should drop folder owner")
	assert.Contains(t, migV2.Up, "check_folder_viewer", "should drop folder viewer")

	applyMigrationUp(t, ctx, db, migV2)

	assertFunctionsDropped(t, ctx, db, map[string]string{
		"check_folder_owner":  "folder owner should be dropped",
		"check_folder_viewer": "folder viewer should be dropped",
	})
	assertFunctions(t, ctx, db, map[string]string{
		"check_document_owner":  "document owner should still exist",
		"check_document_editor": "document editor should still exist",
		"check_document_viewer": "document viewer should still exist",
		"check_permission":      "dispatcher should still exist",
	})
}

// TestMigration_ChangeDetection verifies that incremental migrations only
// include functions whose SQL bodies have actually changed.
func TestMigration_ChangeDetection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	csV1 := compilePipeline(t, schemaV1)

	// Generate incremental migration from V1 -> V2 with change detection
	csV2, migV1toV2 := incrementalMigration(t, schemaV2, "v0.7.4", csV1)
	assert.Contains(t, migV1toV2.Up, "Changed functions:")

	// Generate "no-op" migration V2 -> V2 (same schema)
	_, migV2toV2 := incrementalMigration(t, schemaV2, "v0.7.4", csV2)
	assert.Contains(t, migV2toV2.Up, "Changed functions: 0")
	assert.NotContains(t, migV2toV2.Up, "Changed Functions (")
}

// TestMigration_BuiltinMigrate verifies the built-in Migrate API works end-to-end
// and creates the melange_migrations tracking table.
func TestMigration_BuiltinMigrate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})

	assertFunctions(t, ctx, db, map[string]string{
		"check_permission":      "dispatcher should exist",
		"check_document_owner":  "check_document_owner should exist",
		"check_document_viewer": "check_document_viewer should exist",
	})

	// Verify melange_migrations table was created
	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err, "GetLastMigration should succeed")
	require.NotNil(t, rec, "should have a migration record")
	assert.Equal(t, migrator.CodegenVersion(), rec.CodegenVersion)
	assert.NotEmpty(t, rec.FunctionNames, "function names should be recorded")
	assert.NotEmpty(t, rec.FunctionChecksums, "function checksums should be recorded")
}

// TestMigration_BuiltinMigrateSchemaEvolution applies V1 then V2 using
// MigrateWithTypesAndOptions, verifying that schema evolution works correctly
// with migration record tracking.
func TestMigration_BuiltinMigrateSchemaEvolution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	// Apply V1
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})

	assert.True(t, functionExists(t, ctx, db, "check_document_viewer"), "V1 viewer should exist")
	assert.False(t, functionExists(t, ctx, db, "check_document_editor"), "V1 should not have editor")

	// Apply V2 (replaces viewer definition, adds editor)
	migrateSchema(t, ctx, m, schemaV2, migrator.InternalMigrateOptions{
		Version: "v0.7.4",
	})

	assertFunctions(t, ctx, db, map[string]string{
		"check_document_editor": "V2 should have editor",
		"check_document_viewer": "V2 should still have viewer",
		"check_document_owner":  "V2 should still have owner",
	})

	// Verify the migration record was updated
	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, migrator.ComputeSchemaChecksum(schemaV2), rec.SchemaChecksum,
		"last migration should reflect V2 schema checksum")
}

// TestMigration_BuiltinSkipUnchanged verifies that re-applying the same schema
// is a no-op (skip detection).
func TestMigration_BuiltinSkipUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})
	assert.Equal(t, 1, migrationRecordCount(t, ctx, db))

	// Apply same schema again (should skip)
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})
	assert.Equal(t, 1, migrationRecordCount(t, ctx, db),
		"no new migration record should be created when skipping")
}

// TestMigration_BuiltinForceReapply verifies that Force=true re-applies even when unchanged.
func TestMigration_BuiltinForceReapply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})
	countBefore := migrationRecordCount(t, ctx, db)

	// Force re-apply
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
		Force:   true,
	})
	assert.Equal(t, countBefore+1, migrationRecordCount(t, ctx, db),
		"force should add a new migration record")
}

// TestMigration_Phase2SkipOnVersionBump verifies that upgrading the melange version
// with an identical schema skips re-applying functions (phase 2 skip) but still
// records the new migration state.
//
// To simulate a version upgrade, we apply V1 normally then update the stored
// codegen_version to an older value. The next migration sees a version mismatch
// (phase 1 doesn't skip), generates SQL, finds checksums are identical (phase 2
// skips the apply), and records the new state.
func TestMigration_Phase2SkipOnVersionBump(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	// Apply V1
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.3",
	})
	assert.Equal(t, 1, migrationRecordCount(t, ctx, db))

	// Simulate a version upgrade by changing the stored codegen_version
	// to something different from CodegenVersion(). This makes phase 1
	// think the melange version has changed.
	_, err := db.ExecContext(ctx,
		"UPDATE melange_migrations SET codegen_version = 'v-old'")
	require.NoError(t, err)

	// Re-apply same schema. Phase 1 won't skip (codegen_version mismatch),
	// phase 2 should skip (generated SQL is identical) and only record state.
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version: "v0.7.4",
	})
	assert.Equal(t, 2, migrationRecordCount(t, ctx, db),
		"phase 2 skip should still record a migration")

	// Functions should still exist and be unchanged
	assertFunctions(t, ctx, db, map[string]string{
		"check_permission":      "dispatcher should still exist",
		"check_document_owner":  "owner should still exist",
		"check_document_viewer": "viewer should still exist",
	})

	// The last migration record should reflect the current codegen version
	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, migrator.ComputeSchemaChecksum(schemaV1), rec.SchemaChecksum)
}

// TestMigration_DryRun verifies that dry-run mode outputs SQL without applying changes.
func TestMigration_DryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	types, err := parser.ParseSchemaString(schemaV1)
	require.NoError(t, err)

	var buf strings.Builder
	m := migrator.NewMigrator(db, "")
	err = m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		SchemaContent: schemaV1,
		Version:       "v0.7.3",
		DryRun:        &buf,
	})
	require.NoError(t, err, "dry-run should succeed")

	output := buf.String()
	assert.Contains(t, output, "Melange Migration (dry-run)")
	assert.Contains(t, output, "check_document_owner")
	assert.Contains(t, output, "check_document_viewer")
	assert.Contains(t, output, "check_permission")

	// No functions should exist in the database
	functions := getFunctionNames(t, ctx, db)
	assert.Empty(t, functions, "dry-run should not create any functions")
}

// --- Modular schema definitions ---
//
// These mirror the single-file schemaV1/V2/V3 definitions above but use
// modular format (Schema 1.2, multiple module files merged via parser).

var modularV1Modules = map[string]string{
	"core.fga": `module core

type user

type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`,
}

var modularV2Modules = map[string]string{
	"core.fga": `module core

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`,
}

// modularV2WithFolderModules extends V2 with a folder type in a separate module,
// exercising cross-module type definitions.
var modularV2WithFolderModules = map[string]string{
	"core.fga": `module core

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`,
	"folders.fga": `module folders

type folder
  relations
    define owner: [user]
    define viewer: [user] or owner
`,
}

// --- Modular schema migration tests ---

// TestMigration_Modular_InitialApply verifies that a modular schema can be compiled
// into a full migration and applied to a database, producing the same functions as
// an equivalent single-file schema.
func TestMigration_Modular_InitialApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	types := parseModularSchema(t, modularV1Modules)
	cs, migration := fullMigrationFromTypes(t, types, "modular-v1", "v0.8.0")

	assert.Contains(t, migration.Up, "-- Melange Migration (UP)")

	applyMigrationUp(t, ctx, db, migration)

	assertFunctions(t, ctx, db, map[string]string{
		"check_permission":      "dispatcher should exist",
		"check_document_owner":  "check_document_owner should exist",
		"check_document_viewer": "check_document_viewer should exist",
	})

	installedFunctions := getFunctionNames(t, ctx, db)
	for _, expected := range cs.functionNames {
		assert.Contains(t, installedFunctions, expected, "function %s should be installed", expected)
	}
}

// TestMigration_Modular_DownRemovesAll verifies DOWN migration works for modular schemas.
func TestMigration_Modular_DownRemovesAll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	types := parseModularSchema(t, modularV1Modules)
	_, migration := fullMigrationFromTypes(t, types, "modular-v1", "v0.8.0")

	applyMigrationUp(t, ctx, db, migration)
	applyMigrationDown(t, ctx, db, migration)

	remaining := getFunctionNames(t, ctx, db)
	assert.Empty(t, remaining, "all functions should be dropped after DOWN migration")
}

// TestMigration_Modular_IncrementalUpdate verifies incremental migration works
// when evolving a modular schema (V1 → V2: replace viewer, add editor).
func TestMigration_Modular_IncrementalUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Apply modular V1
	typesV1 := parseModularSchema(t, modularV1Modules)
	csV1, migV1 := fullMigrationFromTypes(t, typesV1, "modular-v1", "v0.8.0")
	applyMigrationUp(t, ctx, db, migV1)

	assert.True(t, functionExists(t, ctx, db, "check_document_viewer"), "V1 should have viewer")
	assert.False(t, functionExists(t, ctx, db, "check_document_editor"), "V1 should not have editor")

	// Apply modular V2 (incremental)
	typesV2 := parseModularSchema(t, modularV2Modules)
	_, migV2 := incrementalMigrationFromTypes(t, typesV2, "modular-v2", "v0.8.1", csV1)
	applyMigrationUp(t, ctx, db, migV2)

	assertFunctions(t, ctx, db, map[string]string{
		"check_document_editor": "V2 should have editor",
		"check_document_viewer": "V2 should still have viewer",
		"check_document_owner":  "V2 should still have owner",
		"check_permission":      "dispatcher should still exist",
	})
}

// TestMigration_Modular_AddModule verifies that adding a new module (folder type)
// correctly generates new functions in an incremental migration.
func TestMigration_Modular_AddModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Start with V2 (document only)
	typesV2 := parseModularSchema(t, modularV2Modules)
	csV2, migV2 := fullMigrationFromTypes(t, typesV2, "modular-v2", "v0.8.1")
	applyMigrationUp(t, ctx, db, migV2)

	// Add folder module (incremental)
	typesV2Folder := parseModularSchema(t, modularV2WithFolderModules)
	_, migV2Folder := incrementalMigrationFromTypes(t, typesV2Folder, "modular-v2-folder", "v0.8.2", csV2)
	applyMigrationUp(t, ctx, db, migV2Folder)

	assertFunctions(t, ctx, db, map[string]string{
		"check_document_owner":  "document owner should exist",
		"check_document_editor": "document editor should exist",
		"check_document_viewer": "document viewer should exist",
		"check_folder_owner":    "folder owner should exist",
		"check_folder_viewer":   "folder viewer should exist",
		"check_permission":      "dispatcher should exist",
	})
}

// TestMigration_Modular_RemoveModule verifies that removing a module drops
// its orphaned functions in an incremental migration.
func TestMigration_Modular_RemoveModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Start with V2 + folder module
	typesV2Folder := parseModularSchema(t, modularV2WithFolderModules)
	csV2Folder, migV2Folder := fullMigrationFromTypes(t, typesV2Folder, "modular-v2-folder", "v0.8.2")
	applyMigrationUp(t, ctx, db, migV2Folder)

	assertFunctions(t, ctx, db, map[string]string{
		"check_folder_owner":  "folder owner should exist before removal",
		"check_folder_viewer": "folder viewer should exist before removal",
	})

	// Remove folder module (back to V2 document-only)
	typesV2 := parseModularSchema(t, modularV2Modules)
	_, migV2 := incrementalMigrationFromTypes(t, typesV2, "modular-v2", "v0.8.3", csV2Folder)

	assert.Contains(t, migV2.Up, "Drop removed functions", "should have orphan drop section")
	assert.Contains(t, migV2.Up, "check_folder_owner", "should drop folder owner")
	assert.Contains(t, migV2.Up, "check_folder_viewer", "should drop folder viewer")

	applyMigrationUp(t, ctx, db, migV2)

	assertFunctionsDropped(t, ctx, db, map[string]string{
		"check_folder_owner":  "folder owner should be dropped",
		"check_folder_viewer": "folder viewer should be dropped",
	})
	assertFunctions(t, ctx, db, map[string]string{
		"check_document_owner":  "document owner should still exist",
		"check_document_editor": "document editor should still exist",
		"check_document_viewer": "document viewer should still exist",
		"check_permission":      "dispatcher should still exist",
	})
}

// TestMigration_Modular_EquivalentToSingleFile verifies that a modular schema
// produces identical migration SQL as an equivalent single-file schema.
func TestMigration_Modular_EquivalentToSingleFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Compile both pipelines
	csSingle := compilePipeline(t, schemaV1)

	typesModular := parseModularSchema(t, modularV1Modules)
	csModular := compilePipelineFromTypes(t, typesModular)

	assert.Equal(t, csSingle.functionNames, csModular.functionNames,
		"modular and single-file schemas should produce the same function names")

	// Both should apply cleanly to separate databases
	db1 := testutil.EmptyDB(t)
	db2 := testutil.EmptyDB(t)
	ctx := context.Background()

	migSingle := generateMigration(csSingle, "single", "v0.8.0", nil)
	migModular := generateMigration(csModular, "modular", "v0.8.0", nil)

	applyMigrationUp(t, ctx, db1, migSingle)
	applyMigrationUp(t, ctx, db2, migModular)

	fnSingle := getFunctionNames(t, ctx, db1)
	fnModular := getFunctionNames(t, ctx, db2)
	assert.Equal(t, fnSingle, fnModular,
		"installed functions should be identical between single-file and modular schemas")
}

// TestMigration_RecordsDeployedModel verifies that a migration persists the
// schema DSL, format, and parsed model, and that GetDeployedModel reads them
// back losslessly. This is the foundation for `melange schema pull` and diffs.
func TestMigration_RecordsDeployedModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version:      "v0.9.0",
		SchemaFormat: "single",
	})

	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model, "deployed model should be recorded")

	assert.Equal(t, schemaV1, model.DSL, "stored DSL should match the migrated schema")
	assert.Equal(t, "single", model.Format)
	assert.Equal(t, "v0.9.0", model.MelangeVersion)
	assert.False(t, model.MigratedAt.IsZero(), "migrated_at should be populated")

	// The parsed model round-trips to exactly what the schema parses to.
	wantTypes, err := parser.ParseSchemaString(schemaV1)
	require.NoError(t, err)
	assert.Equal(t, wantTypes, model.Types, "stored model should round-trip to the parsed types")
}

// TestMigration_DeployedModelAbsentColumns simulates a database migrated before
// model storage existed: GetLastMigration still works (dynamic column
// selection) and GetDeployedModel returns nil gracefully rather than erroring.
func TestMigration_DeployedModelAbsentColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.9.0"})

	// Drop the model columns to mimic an older installation.
	for _, col := range []string{"schema_dsl", "schema_format", "model_json"} {
		_, err := db.ExecContext(ctx, "ALTER TABLE melange_migrations DROP COLUMN "+col)
		require.NoError(t, err, "dropping column %s", col)
	}

	// GetLastMigration still reads the surviving columns.
	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.NotEmpty(t, rec.FunctionNames, "function names should still be read")
	assert.Empty(t, rec.SchemaDSL, "schema_dsl is absent on an old install")

	// GetDeployedModel reports "no model recorded" as nil, not an error.
	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	assert.Nil(t, model)
}

// TestMigration_DryRunScriptRecordsModel verifies that a dry-run migration
// script, applied separately, records a self-describing migration: applying the
// emitted SQL creates the tracking table and an INSERT that GetDeployedModel can
// read back. Guards the dry-run INSERT against dropping the model columns.
func TestMigration_DryRunScriptRecordsModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	types, err := parser.ParseSchemaString(schemaV1)
	require.NoError(t, err)

	var buf bytes.Buffer
	err = m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		DryRun:        &buf,
		Version:       "v0.9.0",
		SchemaContent: schemaV1,
		SchemaFormat:  "single",
	})
	require.NoError(t, err)

	// The emitted script must be valid SQL end-to-end.
	_, err = db.ExecContext(ctx, buf.String())
	require.NoError(t, err, "dry-run script should apply cleanly")

	// And it must have recorded a model the read-back API can return.
	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model, "applied dry-run script should record the model")
	assert.Equal(t, schemaV1, model.DSL)
	assert.Equal(t, "single", model.Format)
	assert.Equal(t, "v0.9.0", model.MelangeVersion)
}

// TestMigration_DryRunScriptUpgradesOldTable applies a dry-run script to a
// database whose melange_migrations table predates the deployed-model columns.
// The script must ADD the columns (CREATE TABLE IF NOT EXISTS is a no-op on the
// existing table) before its INSERT references them.
func TestMigration_DryRunScriptUpgradesOldTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	// Establish an older installation, then drop the model columns.
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.8.0"})
	for _, col := range []string{"schema_dsl", "schema_format", "model_json"} {
		_, err := db.ExecContext(ctx, "ALTER TABLE melange_migrations DROP COLUMN "+col)
		require.NoError(t, err, "dropping column %s", col)
	}

	types, err := parser.ParseSchemaString(schemaV1)
	require.NoError(t, err)

	var buf bytes.Buffer
	err = m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		DryRun:        &buf,
		Version:       "v0.9.0",
		SchemaContent: schemaV1,
		SchemaFormat:  "single",
	})
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, buf.String())
	require.NoError(t, err, "dry-run script should upgrade the old table then insert")

	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model, "model should be readable after applying upgrade script")
	assert.Equal(t, schemaV1, model.DSL)
}

// TestMigration_MigrateFromStringRecordsModel verifies the high-level
// MigrateFromString entrypoint records a self-describing migration, so
// GetDeployedModel works for library users following the recommended APIs.
func TestMigration_MigrateFromStringRecordsModel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	require.NoError(t, migrator.MigrateFromString(ctx, db, schemaV1))

	m := migrator.NewMigrator(db, "")
	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model, "MigrateFromString should record the deployed model")
	assert.Equal(t, schemaV1, model.DSL)
	assert.Equal(t, "single", model.Format)
}

// TestMigration_BackfillsModelOnRerun verifies that rerunning migrate with an
// unchanged schema backfills the deployed model when an older record lacks it,
// rather than fast-path skipping and leaving GetDeployedModel nil.
func TestMigration_BackfillsModelOnRerun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version:      "v0.9.0",
		SchemaFormat: "single",
	})

	// Simulate a record written before model storage existed.
	_, err := db.ExecContext(ctx,
		"UPDATE melange_migrations SET schema_dsl = '', schema_format = '', model_json = '{}'::jsonb")
	require.NoError(t, err)

	model, err := m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.Nil(t, model, "precondition: model appears unrecorded")

	// Rerunning the same schema must backfill instead of fast-path skipping.
	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{
		Version:      "v0.9.0",
		SchemaFormat: "single",
	})

	model, err = m.GetDeployedModel(ctx)
	require.NoError(t, err)
	require.NotNil(t, model, "rerun should backfill the deployed model")
	assert.Equal(t, schemaV1, model.DSL)
}

// TestMigration_GetLastMigrationToleratesMissingMelangeVersion verifies that a
// database migrated before the melange_version column existed can still be read
// — status now depends on GetLastMigration, so a missing optional column must
// not fail the whole command.
func TestMigration_GetLastMigrationToleratesMissingMelangeVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.9.0"})

	// Simulate a legacy table that predates the melange_version column.
	_, err := db.ExecContext(ctx, "ALTER TABLE melange_migrations DROP COLUMN melange_version")
	require.NoError(t, err)

	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err, "read should tolerate a missing melange_version column")
	require.NotNil(t, rec)
	assert.Empty(t, rec.MelangeVersion)
	assert.NotEmpty(t, rec.FunctionNames)
}

// migrateWithGuard applies a schema with a --if-deployed-checksum precondition.
func migrateWithGuard(t *testing.T, ctx context.Context, m *migrator.Migrator, schemaContent, version, expectedChecksum string) error {
	t.Helper()
	types, err := parser.ParseSchemaString(schemaContent)
	require.NoError(t, err)
	return m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		Version:            version,
		SchemaContent:      schemaContent,
		SchemaFormat:       "single",
		IfDeployedChecksum: &expectedChecksum,
	})
}

// TestMigration_DriftGuardMatches verifies a compare-and-swap migrate proceeds
// when the deployed checksum matches the expected one.
func TestMigration_DriftGuardMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.9.0"})

	// Deployed is V1; guard against V1's checksum, apply V2.
	err := migrateWithGuard(t, ctx, m, schemaV2, "v0.9.1", migrator.ComputeSchemaChecksum(schemaV1))
	require.NoError(t, err)
	assert.True(t, functionExists(t, ctx, db, "check_document_editor"), "V2 should have been applied")
}

// TestMigration_DriftGuardMismatchAborts verifies a mismatched precondition
// aborts without applying anything, leaving the deployed model untouched.
func TestMigration_DriftGuardMismatchAborts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.9.0"})

	err := migrateWithGuard(t, ctx, m, schemaV2, "v0.9.1", "not-the-deployed-checksum")
	require.Error(t, err)
	var driftErr *migrator.DeployedModelChangedError
	require.True(t, errors.As(err, &driftErr), "expected DeployedModelChangedError, got %v", err)

	// Nothing applied: V2's editor function is absent and the record is still V1.
	assert.False(t, functionExists(t, ctx, db, "check_document_editor"), "V2 must not have been applied")
	rec, err := m.GetLastMigration(ctx)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, migrator.ComputeSchemaChecksum(schemaV1), rec.SchemaChecksum, "deployed model should still be V1")
}

// TestMigration_DriftGuardEmptyMatchesFreshDatabase verifies that an empty
// expected checksum matches a database with no migration, and a non-empty one
// aborts against a fresh database.
func TestMigration_DriftGuardEmptyMatchesFreshDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()

	// Empty expected checksum matches a never-migrated database → applies.
	db := testutil.EmptyDB(t)
	m := migrator.NewMigrator(db, "")
	require.NoError(t, migrateWithGuard(t, ctx, m, schemaV1, "v0.9.0", ""))
	assert.True(t, functionExists(t, ctx, db, "check_document_viewer"))

	// A non-empty expected checksum against a fresh database aborts.
	db2 := testutil.EmptyDB(t)
	m2 := migrator.NewMigrator(db2, "")
	err := migrateWithGuard(t, ctx, m2, schemaV1, "v0.9.0", "some-expected-checksum")
	var driftErr *migrator.DeployedModelChangedError
	require.True(t, errors.As(err, &driftErr), "expected DeployedModelChangedError, got %v", err)
	assert.False(t, functionExists(t, ctx, db2, "check_document_viewer"), "nothing should have been applied")
}

// TestMigration_GuardedUnchangedReportsSkipped verifies that a guarded migrate
// of an unchanged schema still reports skipped=true (the guard is checked before
// the fast-path skip), and that a mismatched guard aborts.
func TestMigration_GuardedUnchangedReportsSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "schema.fga")
	require.NoError(t, os.WriteFile(path, []byte(schemaV1), 0o644))

	_, err := migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{Version: "v0.9.0"})
	require.NoError(t, err)

	sum := migrator.ComputeSchemaChecksum(schemaV1)
	skipped, err := migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{
		Version:            "v0.9.0",
		IfDeployedChecksum: &sum,
	})
	require.NoError(t, err)
	assert.True(t, skipped, "unchanged guarded migrate should report skipped")

	wrong := "not-the-checksum"
	_, err = migrator.MigrateWithOptions(ctx, db, path, migrator.MigrateOptions{
		Version:            "v0.9.0",
		IfDeployedChecksum: &wrong,
	})
	var driftErr *migrator.DeployedModelChangedError
	require.True(t, errors.As(err, &driftErr), "mismatched guard should abort, got %v", err)
}

// TestMigration_DriftGuardEnforcedInDryRun verifies that --if-deployed-checksum
// is honored in dry-run: a mismatch aborts before printing any SQL, and a match
// still produces the preview.
func TestMigration_DriftGuardEnforcedInDryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")

	migrateSchema(t, ctx, m, schemaV1, migrator.InternalMigrateOptions{Version: "v0.9.0"})
	types, err := parser.ParseSchemaString(schemaV2)
	require.NoError(t, err)

	// Mismatch → aborts, nothing printed.
	wrong := "not-the-deployed-checksum"
	var buf bytes.Buffer
	err = m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		DryRun: &buf, Version: "v0.9.1", SchemaContent: schemaV2, SchemaFormat: "single",
		IfDeployedChecksum: &wrong,
	})
	var driftErr *migrator.DeployedModelChangedError
	require.True(t, errors.As(err, &driftErr), "dry-run should enforce the guard, got %v", err)
	assert.Empty(t, buf.String(), "no SQL should be printed on a drift-guard failure")

	// Match → proceeds and prints the preview.
	sum := migrator.ComputeSchemaChecksum(schemaV1)
	var buf2 bytes.Buffer
	err = m.MigrateWithTypesAndOptions(ctx, types, migrator.InternalMigrateOptions{
		DryRun: &buf2, Version: "v0.9.1", SchemaContent: schemaV2, SchemaFormat: "single",
		IfDeployedChecksum: &sum,
	})
	require.NoError(t, err)
	assert.Contains(t, buf2.String(), "Melange Migration (dry-run)", "matching guard should still preview")
}
