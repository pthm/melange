package migrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/schema"
)

// Type aliases for cleaner code.
type (
	TypeDefinition   = schema.TypeDefinition
	GeneratedSQL     = sqlgen.GeneratedSQL
	ListGeneratedSQL = sqlgen.ListGeneratedSQL
)

// Function aliases from schema and sqlgen packages.
var (
	DetectCycles           = schema.DetectCycles
	ComputeRelationClosure = schema.ComputeRelationClosure
	AnalyzeRelations       = sqlgen.AnalyzeRelations
	ComputeCanGenerate     = sqlgen.ComputeCanGenerate
	buildInlineSQLData     = sqlgen.BuildInlineSQLData
	GenerateSQL            = sqlgen.GenerateSQL
	GenerateListSQL        = sqlgen.GenerateListSQL
	CollectFunctionNames   = sqlgen.CollectFunctionNames
)

// CodegenVersion is incremented when SQL generation templates or logic change.
// This ensures migrations re-run even if schema checksum matches.
// Bump this when:
//   - SQL templates in tooling/schema/templates/ change
//   - Codegen logic in schema/codegen.go or schema/codegen_list.go changes
//   - New function patterns are added
const CodegenVersion = "1"

// MigrateOptions controls migration behavior (public API).
type MigrateOptions struct {
	// DryRun outputs SQL to the provided writer without applying changes to the database.
	// If nil, migration proceeds normally. Use for previewing migrations or generating migration scripts.
	DryRun io.Writer

	// Force re-runs migration even if schema/codegen unchanged. Use when manually fixing corrupted state or testing.
	Force bool
}

// InternalMigrateOptions extends MigrateOptions with internal fields.
type InternalMigrateOptions struct {
	DryRun io.Writer
	Force  bool

	// SchemaContent is the raw schema text used for checksum calculation to detect schema changes.
	// If empty, skip-if-unchanged optimization is disabled.
	SchemaContent string
}

// MigrationRecord represents a row in the melange_migrations table.
type MigrationRecord struct {
	SchemaChecksum string
	CodegenVersion string
	FunctionNames  []string
}

// Migrator handles loading authorization schemas into PostgreSQL.
// The migrator is idempotent - safe to run on every application startup.
//
// The migration process:
//  1. Creates/replaces check_permission and list_accessible_* functions
//  2. Loads generated SQL entrypoints into the database
//
// # Usage
//
// Use the convenience functions in pkg/migrator for most use cases:
//
//	import "github.com/pthm/melange/pkg/migrator"
//	err := migrator.Migrate(ctx, db, "schemas")
//
// For embedded schemas (no file I/O):
//
//	err := migrator.MigrateFromString(ctx, db, schemaContent)
//
// Use the Migrator directly when you have pre-parsed TypeDefinitions
// or need fine-grained control (DDL-only, status checks, etc.):
//
//	types, _ := parser.ParseSchema("schemas/schema.fga")
//	m := migrator.NewMigrator(db, "schemas")
//	err := m.MigrateWithTypes(ctx, types)
type Migrator struct {
	db         Execer
	schemasDir string
}

// NewMigrator creates a new schema migrator.
// The schemasDir should contain a schema.fga file in OpenFGA DSL format.
// The Execer is typically *sql.DB but can be *sql.Tx for testing.
func NewMigrator(db Execer, schemasDir string) *Migrator {
	return &Migrator{db: db, schemasDir: schemasDir}
}

// SchemaPath returns the path to the schema.fga file.
// Conventionally named schema.fga by OpenFGA tooling.
func (m *Migrator) SchemaPath() string {
	return filepath.Join(m.schemasDir, "schema.fga")
}

// HasSchema returns true if the schema file exists.
// Use this to conditionally run migration or skip if not configured.
func (m *Migrator) HasSchema() bool {
	_, err := os.Stat(m.SchemaPath())
	return err == nil
}

// ApplyDDL applies any base schema required by Melange.
// With fully generated SQL entrypoints, no base DDL is required.
func (m *Migrator) ApplyDDL(ctx context.Context) error {
	return nil
}

// applyGeneratedSQL applies generated specialized functions and dispatcher.
func (m *Migrator) applyGeneratedSQL(ctx context.Context, db Execer, gen GeneratedSQL) error {
	// Apply specialized check functions first (dispatcher depends on them)
	for i, fn := range gen.Functions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying generated function %d: %w", i, err)
		}
	}
	for i, fn := range gen.NoWildcardFunctions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying generated no-wildcard function %d: %w", i, err)
		}
	}

	// Apply dispatcher (replaces default check_permission)
	if gen.Dispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.Dispatcher); err != nil {
			return fmt.Errorf("applying dispatcher: %w", err)
		}
	}

	// Apply no-wildcard dispatcher
	if gen.DispatcherNoWildcard != "" {
		if _, err := db.ExecContext(ctx, gen.DispatcherNoWildcard); err != nil {
			return fmt.Errorf("applying no-wildcard dispatcher: %w", err)
		}
	}

	return nil
}

// applyGeneratedListSQL applies generated specialized list functions and dispatchers.
func (m *Migrator) applyGeneratedListSQL(ctx context.Context, db Execer, gen ListGeneratedSQL) error {
	// Apply specialized list_objects functions
	for i, fn := range gen.ListObjectsFunctions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying list_objects function %d: %w", i, err)
		}
	}

	// Apply specialized list_subjects functions
	for i, fn := range gen.ListSubjectsFunctions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying list_subjects function %d: %w", i, err)
		}
	}

	// Apply list_objects dispatcher
	if gen.ListObjectsDispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.ListObjectsDispatcher); err != nil {
			return fmt.Errorf("applying list_objects dispatcher: %w", err)
		}
	}

	// Apply list_subjects dispatcher
	if gen.ListSubjectsDispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.ListSubjectsDispatcher); err != nil {
			return fmt.Errorf("applying list_subjects dispatcher: %w", err)
		}
	}

	return nil
}

// MigrateWithTypes performs database migration using pre-parsed type definitions.
// This is the core migration method used by the tooling package's Migrate function.
//
// The method:
//  1. Validates the schema (checks for cycles)
//  2. Computes derived data (closure)
//  3. Analyzes relations and generates specialized SQL functions
//  4. Applies everything atomically in a transaction:
//     - Generated specialized functions and dispatcher
//
// This is idempotent - safe to run multiple times with the same types.
//
// Uses a transaction if the db supports it (*sql.DB). This ensures
// the schema is updated atomically or not at all.
func (m *Migrator) MigrateWithTypes(ctx context.Context, types []TypeDefinition) error {
	// 1. Validate schema before any computation
	if err := DetectCycles(types); err != nil {
		return err
	}

	// 2. Compute derived data (pure computation, no DB)
	closureRows := ComputeRelationClosure(types)

	// 3. Analyze relations and generate SQL
	analyses := AnalyzeRelations(types, closureRows)
	analyses = ComputeCanGenerate(analyses) // Walk dependency graph to set CanGenerate
	inline := buildInlineSQLData(closureRows, analyses)
	generatedSQL, err := GenerateSQL(analyses, inline)
	if err != nil {
		return fmt.Errorf("generating check SQL: %w", err)
	}

	// 4. Generate list functions
	listSQL, err := GenerateListSQL(analyses, inline)
	if err != nil {
		return fmt.Errorf("generating list SQL: %w", err)
	}

	// 5. Apply everything atomically
	if txer, ok := m.db.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := txer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("starting transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// Apply generated specialized check functions
		if err := m.applyGeneratedSQL(ctx, tx, generatedSQL); err != nil {
			return err
		}

		// Apply generated specialized list functions
		if err := m.applyGeneratedListSQL(ctx, tx, listSQL); err != nil {
			return err
		}

		return tx.Commit()
	}

	// Fall back to non-transactional (for *sql.Conn)
	if err := m.applyGeneratedSQL(ctx, m.db, generatedSQL); err != nil {
		return err
	}
	if err := m.applyGeneratedListSQL(ctx, m.db, listSQL); err != nil {
		return err
	}
	return nil
}

// Status represents the current migration state.
// Use GetStatus to check if the authorization system is properly configured.
type Status struct {
	// SchemaExists indicates if the schema.fga file exists on disk.
	SchemaExists bool

	// TuplesExists indicates if the melange_tuples relation exists (view, table, or materialized view).
	// This must be created by the user to map their domain tables.
	TuplesExists bool
}

// GetStatus returns the current migration status.
// Useful for health checks or migration diagnostics.
func (m *Migrator) GetStatus(ctx context.Context) (*Status, error) {
	status := &Status{
		SchemaExists: m.HasSchema(),
	}

	// Check if melange_tuples relation exists (view, table, or materialized view)
	var tuplesExists bool
	err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'melange_tuples'
			AND n.nspname = current_schema()
			AND c.relkind IN ('r', 'v', 'm')
		)
	`).Scan(&tuplesExists)
	if err != nil {
		return nil, fmt.Errorf("checking melange_tuples: %w", err)
	}
	status.TuplesExists = tuplesExists

	return status, nil
}

// ComputeSchemaChecksum returns a SHA256 hash of the schema content.
// Used to detect schema changes for skip-if-unchanged optimization.
func ComputeSchemaChecksum(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// GetLastMigration returns the most recent migration record, or nil if none exists.
// This can be used to check if migration is needed before calling MigrateWithTypesAndOptions.
func (m *Migrator) GetLastMigration(ctx context.Context) (*MigrationRecord, error) {
	return m.getLastMigration(ctx, m.db)
}

// getLastMigration returns the most recent migration record, or nil if none exists.
func (m *Migrator) getLastMigration(ctx context.Context, db Execer) (*MigrationRecord, error) {
	// First check if the migrations table exists
	var tableExists bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'melange_migrations'
			AND n.nspname = current_schema()
		)
	`).Scan(&tableExists)
	if err != nil {
		return nil, fmt.Errorf("checking melange_migrations table: %w", err)
	}
	if !tableExists {
		return nil, nil // No migrations table yet
	}

	var rec MigrationRecord
	err = db.QueryRowContext(ctx, `
		SELECT schema_checksum, codegen_version, function_names
		FROM melange_migrations
		ORDER BY id DESC
		LIMIT 1
	`).Scan(&rec.SchemaChecksum, &rec.CodegenVersion, pq.Array(&rec.FunctionNames))
	if err == sql.ErrNoRows {
		return nil, nil // No previous migration
	}
	if err != nil {
		return nil, fmt.Errorf("querying last migration: %w", err)
	}
	return &rec, nil
}

// shouldSkipMigration returns true if the schema and codegen version are unchanged.
func shouldSkipMigration(lastMigration *MigrationRecord, schemaChecksum string) bool {
	if lastMigration == nil {
		return false
	}
	return lastMigration.SchemaChecksum == schemaChecksum &&
		lastMigration.CodegenVersion == CodegenVersion
}

// getCurrentFunctions returns all melange-generated function names from pg_proc.
func (m *Migrator) getCurrentFunctions(ctx context.Context, db Execer) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = current_schema()
		AND (
			p.proname LIKE 'check_%'
			OR p.proname LIKE 'list_%'
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("querying pg_proc: %w", err)
	}
	defer func() { _ = rows.Close() }()

	functions := make([]string, 0, 32)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning function name: %w", err)
		}
		functions = append(functions, name)
	}
	return functions, rows.Err()
}

// dropOrphanedFunctions drops functions that exist but are not in the expected list.
func (m *Migrator) dropOrphanedFunctions(ctx context.Context, db Execer, currentFunctions, expectedFunctions []string) error {
	expected := make(map[string]bool)
	for _, fn := range expectedFunctions {
		expected[fn] = true
	}

	for _, fn := range currentFunctions {
		if !expected[fn] {
			// Use CASCADE to handle any edge case dependencies
			_, err := db.ExecContext(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s CASCADE", fn))
			if err != nil {
				return fmt.Errorf("dropping orphaned function %s: %w", fn, err)
			}
		}
	}
	return nil
}

// applyMigrationsDDL creates the melange_migrations table if it doesn't exist.
func (m *Migrator) applyMigrationsDDL(ctx context.Context, db Execer) error {
	if _, err := db.ExecContext(ctx, migrationsDDL); err != nil {
		return fmt.Errorf("applying migrations DDL: %w", err)
	}
	return nil
}

// insertMigrationRecord records the migration in melange_migrations.
func (m *Migrator) insertMigrationRecord(ctx context.Context, db Execer, schemaChecksum string, functionNames []string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO melange_migrations (schema_checksum, codegen_version, function_names)
		VALUES ($1, $2, $3)
	`, schemaChecksum, CodegenVersion, pq.Array(functionNames))
	if err != nil {
		return fmt.Errorf("inserting migration record: %w", err)
	}
	return nil
}

// MigrateWithTypesAndOptions performs database migration with options.
// This is the full-featured migration method that supports dry-run, skip-if-unchanged,
// and orphan cleanup.
//
// See MigrateWithTypes for basic usage without options.
func (m *Migrator) MigrateWithTypesAndOptions(ctx context.Context, types []TypeDefinition, opts InternalMigrateOptions) error {
	// 1. Validate schema before any computation
	if err := DetectCycles(types); err != nil {
		return err
	}

	// 2. Compute schema checksum if content provided
	var schemaChecksum string
	if opts.SchemaContent != "" {
		schemaChecksum = ComputeSchemaChecksum(opts.SchemaContent)
	}

	// 3. Check if we can skip migration (unless force or dry-run)
	if !opts.Force && opts.DryRun == nil && schemaChecksum != "" {
		lastMigration, err := m.getLastMigration(ctx, m.db)
		if err != nil {
			return fmt.Errorf("checking last migration: %w", err)
		}
		if shouldSkipMigration(lastMigration, schemaChecksum) {
			return nil // Schema unchanged, skip migration
		}
	}

	// 4. Compute derived data (pure computation, no DB)
	closureRows := ComputeRelationClosure(types)

	// 5. Analyze relations and generate SQL
	analyses := AnalyzeRelations(types, closureRows)
	analyses = ComputeCanGenerate(analyses)
	inline := buildInlineSQLData(closureRows, analyses)
	generatedSQL, err := GenerateSQL(analyses, inline)
	if err != nil {
		return fmt.Errorf("generating check SQL: %w", err)
	}

	// 6. Generate list functions
	listSQL, err := GenerateListSQL(analyses, inline)
	if err != nil {
		return fmt.Errorf("generating list SQL: %w", err)
	}

	// 7. Collect expected function names for tracking and orphan detection
	expectedFunctions := CollectFunctionNames(analyses)

	// 8. Handle dry-run mode
	if opts.DryRun != nil {
		m.outputDryRun(opts.DryRun, schemaChecksum, generatedSQL, listSQL, expectedFunctions)
		return nil
	}

	// 9. Apply everything atomically
	if txer, ok := m.db.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := txer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("starting transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// Apply migrations DDL (creates tracking table)
		if err := m.applyMigrationsDDL(ctx, tx); err != nil {
			return err
		}

		// Get current functions before applying new ones (for orphan detection)
		currentFunctions, err := m.getCurrentFunctions(ctx, tx)
		if err != nil {
			return fmt.Errorf("getting current functions: %w", err)
		}

		// Apply generated specialized check functions
		if err := m.applyGeneratedSQL(ctx, tx, generatedSQL); err != nil {
			return err
		}

		// Apply generated specialized list functions
		if err := m.applyGeneratedListSQL(ctx, tx, listSQL); err != nil {
			return err
		}

		// Drop orphaned functions
		if err := m.dropOrphanedFunctions(ctx, tx, currentFunctions, expectedFunctions); err != nil {
			return err
		}

		// Record migration
		if schemaChecksum != "" {
			if err := m.insertMigrationRecord(ctx, tx, schemaChecksum, expectedFunctions); err != nil {
				return err
			}
		}

		return tx.Commit()
	}

	// Fall back to non-transactional (for *sql.Conn)
	if err := m.applyMigrationsDDL(ctx, m.db); err != nil {
		return err
	}
	currentFunctions, err := m.getCurrentFunctions(ctx, m.db)
	if err != nil {
		return fmt.Errorf("getting current functions: %w", err)
	}
	if err := m.applyGeneratedSQL(ctx, m.db, generatedSQL); err != nil {
		return err
	}
	if err := m.applyGeneratedListSQL(ctx, m.db, listSQL); err != nil {
		return err
	}
	if err := m.dropOrphanedFunctions(ctx, m.db, currentFunctions, expectedFunctions); err != nil {
		return err
	}
	if schemaChecksum != "" {
		if err := m.insertMigrationRecord(ctx, m.db, schemaChecksum, expectedFunctions); err != nil {
			return err
		}
	}
	return nil
}

// outputDryRun writes the migration SQL to the provided writer.
func (m *Migrator) outputDryRun(w io.Writer, schemaChecksum string, generatedSQL GeneratedSQL, listSQL ListGeneratedSQL, expectedFunctions []string) {
	// Header
	_, _ = fmt.Fprintf(w, "-- Melange Migration (dry-run)\n")
	_, _ = fmt.Fprintf(w, "-- Schema checksum: %s\n", schemaChecksum)
	_, _ = fmt.Fprintf(w, "-- Codegen version: %s\n", CodegenVersion)
	_, _ = fmt.Fprintf(w, "\n")

	// Migrations DDL
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- DDL: Migration Tracking Table\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	_, _ = fmt.Fprintf(w, "%s\n\n", migrationsDDL)

	// Check functions
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- Check Functions (%d functions)\n", len(generatedSQL.Functions))
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	for _, fn := range generatedSQL.Functions {
		_, _ = fmt.Fprintf(w, "%s\n\n", fn)
	}

	// No-wildcard check functions
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- No-Wildcard Check Functions (%d functions)\n", len(generatedSQL.NoWildcardFunctions))
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	for _, fn := range generatedSQL.NoWildcardFunctions {
		_, _ = fmt.Fprintf(w, "%s\n\n", fn)
	}

	// Check dispatchers
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- Check Dispatchers\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	if generatedSQL.Dispatcher != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", generatedSQL.Dispatcher)
	}
	if generatedSQL.DispatcherNoWildcard != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", generatedSQL.DispatcherNoWildcard)
	}

	// List objects functions
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- List Objects Functions (%d functions)\n", len(listSQL.ListObjectsFunctions))
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	for _, fn := range listSQL.ListObjectsFunctions {
		_, _ = fmt.Fprintf(w, "%s\n\n", fn)
	}

	// List subjects functions
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- List Subjects Functions (%d functions)\n", len(listSQL.ListSubjectsFunctions))
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	for _, fn := range listSQL.ListSubjectsFunctions {
		_, _ = fmt.Fprintf(w, "%s\n\n", fn)
	}

	// List dispatchers
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- List Dispatchers\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	if listSQL.ListObjectsDispatcher != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", listSQL.ListObjectsDispatcher)
	}
	if listSQL.ListSubjectsDispatcher != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", listSQL.ListSubjectsDispatcher)
	}

	// Migration record
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- Migration Record\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")

	// Sort function names for deterministic output
	sortedFunctions := make([]string, len(expectedFunctions))
	copy(sortedFunctions, expectedFunctions)
	sort.Strings(sortedFunctions)

	// Format as SQL array literal
	quotedFunctions := make([]string, len(sortedFunctions))
	for i, fn := range sortedFunctions {
		quotedFunctions[i] = fmt.Sprintf("'%s'", fn)
	}
	_, _ = fmt.Fprintf(w, "INSERT INTO melange_migrations (schema_checksum, codegen_version, function_names)\n")
	_, _ = fmt.Fprintf(w, "VALUES ('%s', '%s', ARRAY[%s]);\n", schemaChecksum, CodegenVersion, strings.Join(quotedFunctions, ", "))
}
