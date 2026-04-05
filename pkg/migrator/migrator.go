package migrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/lib/sqlgen"
	"github.com/pthm/melange/lib/sqlgen/sqldsl"
	"github.com/pthm/melange/lib/version"
	"github.com/pthm/melange/pkg/schema"
)

// Type aliases for cleaner code.
type (
	TypeDefinition   = schema.TypeDefinition
	GeneratedSQL     = sqlgen.GeneratedSQL
	ListGeneratedSQL = sqlgen.ListGeneratedSQL
)

// NamedFunction pairs a function name with its generated SQL body.
type NamedFunction = sqlgen.NamedFunction

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
	collectNamedFunctions  = sqlgen.CollectNamedFunctions
)

// CodegenVersion returns the melange version used to identify which codegen
// produced the SQL. Combined with function checksums, this allows skip detection
// and change tracking across migrations.
func CodegenVersion() string {
	return version.Short()
}

// MigrateOptions controls migration behavior (public API).
type MigrateOptions struct {
	// DryRun outputs SQL to the provided writer without applying changes to the database.
	// If nil, migration proceeds normally. Use for previewing migrations or generating migration scripts.
	DryRun io.Writer

	// Force re-runs migration even if schema/codegen unchanged. Use when manually fixing corrupted state or testing.
	Force bool

	// Version is the melange CLI/library version (e.g., "v0.4.3").
	// Recorded in melange_migrations for traceability.
	Version string

	// DatabaseSchema is the Postgres schema where the objects will be created.
	DatabaseSchema string
}

// InternalMigrateOptions extends MigrateOptions with internal fields.
type InternalMigrateOptions struct {
	DryRun io.Writer
	Force  bool

	// Version is the melange CLI/library version (e.g., "v0.4.3").
	// Recorded in melange_migrations for traceability.
	Version string

	// SchemaContent is the raw schema text used for checksum calculation to detect schema changes.
	// If empty, skip-if-unchanged optimization is disabled.
	SchemaContent string
}

// MigrationRecord represents a row in the melange_migrations table.
type MigrationRecord struct {
	MelangeVersion string
	SchemaChecksum string
	CodegenVersion string
	FunctionNames  []string
	// FunctionChecksums maps function_name → SHA256(sql_body) for each function
	// installed by this migration. Populated only when the database schema includes
	// the function_checksums column (added in v0.7.3). Nil on records written by
	// older versions; callers should treat nil as "no checksum data available" and
	// fall back to full-mode generation.
	FunctionChecksums map[string]string
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
//	err := migrator.Migrate(ctx, db, "schemas/schema.fga")
//
// For embedded schemas (no file I/O):
//
//	err := migrator.MigrateFromString(ctx, db, schemaContent)
//
// Use the Migrator directly when you have pre-parsed TypeDefinitions
// or need fine-grained control (DDL-only, status checks, etc.):
//
//	types, _ := parser.ParseSchema("schemas/schema.fga")
//	m := migrator.NewMigrator(db, "schemas/schema.fga")
//	err := m.MigrateWithTypes(ctx, types)
type Migrator struct {
	db             Execer
	schemaPath     string
	databaseSchema string
}

// NewMigrator creates a new schema migrator.
// The schemaPath should point to an OpenFGA DSL schema file (e.g., "schemas/schema.fga").
// The Execer is typically *sql.DB but can be *sql.Tx for testing.
func NewMigrator(db Execer, schemaPath string) *Migrator {
	return &Migrator{db: db, schemaPath: schemaPath}
}

// SchemaPath returns the path to the schema file.
func (m *Migrator) SchemaPath() string {
	return m.schemaPath
}

// SetDatabaseSchema sets the PostgreSQL schema for melange objects.
func (m *Migrator) SetDatabaseSchema(databaseSchema string) {
	m.databaseSchema = databaseSchema
}

// DatabaseSchema returns the database schema.
func (m *Migrator) DatabaseSchema() string {
	return m.databaseSchema
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

	// Apply bulk dispatcher
	if gen.BulkDispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.BulkDispatcher); err != nil {
			return fmt.Errorf("applying bulk dispatcher: %w", err)
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
	generatedSQL, err := GenerateSQL(analyses, inline, m.databaseSchema)
	if err != nil {
		return fmt.Errorf("generating check SQL: %w", err)
	}

	// 4. Generate list functions
	listSQL, err := GenerateListSQL(analyses, inline, m.databaseSchema)
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
	err := m.db.QueryRowContext(ctx, fmt.Sprintf(
		`
			SELECT EXISTS (
				SELECT 1 FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = 'melange_tuples'
				AND n.nspname = %s
				AND c.relkind IN ('r', 'v', 'm')
			)
		`,
		m.postgresSchema(),
	)).Scan(&tuplesExists)
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

// ComputeFunctionChecksums computes SHA256 hashes for each named function's SQL body.
// The returned map is stored in the migration record and used by `generate migration --db`
// to determine which functions have changed and need to be included in the migration.
func ComputeFunctionChecksums(namedFunctions []NamedFunction) map[string]string {
	checksums := make(map[string]string, len(namedFunctions))
	for _, nf := range namedFunctions {
		h := sha256.Sum256([]byte(nf.SQL))
		checksums[nf.Name] = hex.EncodeToString(h[:])
	}
	return checksums
}

// GetLastMigration returns the most recent migration record, or nil if none
// exists. It queries against the migrator's own database connection, making it
// suitable for external callers such as the generate migration command.
//
// Internal migration code uses the private getLastMigration with an explicit
// Execer to participate in an in-progress transaction.
func (m *Migrator) GetLastMigration(ctx context.Context) (*MigrationRecord, error) {
	return m.getLastMigration(ctx, m.db)
}

// getLastMigration returns the most recent migration record, or nil if none exists.
func (m *Migrator) getLastMigration(ctx context.Context, db Execer) (*MigrationRecord, error) {
	// First check if the migrations table exists
	var tableExists bool
	err := db.QueryRowContext(ctx, fmt.Sprintf(
		`
			SELECT EXISTS (
				SELECT 1 FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = 'melange_migrations'
				AND n.nspname = %s
			)
		`,
		m.postgresSchema(),
	)).Scan(&tableExists)
	if err != nil {
		return nil, fmt.Errorf("checking melange_migrations table: %w", err)
	}
	if !tableExists {
		return nil, nil // No migrations table yet
	}

	// Check if function_checksums column exists (may be absent on older installations)
	var hasChecksumsCol bool
	err = db.QueryRowContext(ctx, fmt.Sprintf(
		`
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'melange_migrations'
				AND column_name = 'function_checksums'
				AND table_schema = %s
			)
		`,
		m.postgresSchema(),
	)).Scan(&hasChecksumsCol)
	if err != nil {
		return nil, fmt.Errorf("checking function_checksums column: %w", err)
	}

	var rec MigrationRecord
	if hasChecksumsCol {
		var checksumsJSON sql.NullString
		err = db.QueryRowContext(ctx, fmt.Sprintf(
			`
				SELECT melange_version, schema_checksum, codegen_version, function_names, function_checksums::TEXT
				FROM %s
				ORDER BY id DESC
				LIMIT 1
			`,
			m.prefixIdent("melange_migrations"),
		)).Scan(&rec.MelangeVersion, &rec.SchemaChecksum, &rec.CodegenVersion, pq.Array(&rec.FunctionNames), &checksumsJSON)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("querying last migration: %w", err)
		}
		if checksumsJSON.Valid && checksumsJSON.String != "" {
			rec.FunctionChecksums = make(map[string]string)
			if err := json.Unmarshal([]byte(checksumsJSON.String), &rec.FunctionChecksums); err != nil {
				return nil, fmt.Errorf("unmarshaling function checksums: %w", err)
			}
		}
	} else {
		err = db.QueryRowContext(ctx, fmt.Sprintf(
			`
				SELECT melange_version, schema_checksum, codegen_version, function_names
				FROM %s
				ORDER BY id DESC
				LIMIT 1
			`,
			m.prefixIdent("melange_migrations"),
		)).Scan(&rec.MelangeVersion, &rec.SchemaChecksum, &rec.CodegenVersion, pq.Array(&rec.FunctionNames))
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("querying last migration: %w", err)
		}
	}
	return &rec, nil
}

// shouldSkipMigration returns true if the schema and codegen version are unchanged.
// This is the fast-path (phase 1) skip: no SQL generation needed at all.
func shouldSkipMigration(lastMigration *MigrationRecord, schemaChecksum string) bool {
	if lastMigration == nil {
		return false
	}
	return lastMigration.SchemaChecksum == schemaChecksum &&
		lastMigration.CodegenVersion == CodegenVersion()
}

// shouldSkipApply returns true if the generated SQL is identical to what was
// last applied. This is the phase 2 skip: SQL was generated (because the schema
// or melange version changed) but the output is byte-for-byte identical, so
// there is nothing to apply. Returns true only when there are no orphaned
// functions and every function checksum matches.
func shouldSkipApply(lastMigration *MigrationRecord, currentChecksums map[string]string, expectedFunctions []string) bool {
	if lastMigration == nil || lastMigration.FunctionChecksums == nil {
		return false
	}

	// Check for orphaned functions (present in previous but not in current)
	currentSet := make(map[string]bool, len(expectedFunctions))
	for _, fn := range expectedFunctions {
		currentSet[fn] = true
	}
	for _, fn := range lastMigration.FunctionNames {
		if !currentSet[fn] {
			return false // Orphan exists, must apply
		}
	}

	// Check that every current function has an unchanged checksum
	for name, checksum := range currentChecksums {
		prevChecksum, existed := lastMigration.FunctionChecksums[name]
		if !existed || prevChecksum != checksum {
			return false // New or changed function, must apply
		}
	}

	return true
}

// getCurrentFunctions returns all melange-generated function names from pg_proc.
func (m *Migrator) getCurrentFunctions(ctx context.Context, db Execer) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = %s
		AND (
			p.proname LIKE 'check_%%'
			OR p.proname LIKE 'list_%%'
		)
	`, m.postgresSchema()))
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
			query := fmt.Sprintf("DROP FUNCTION IF EXISTS %s CASCADE", m.prefixIdent(fn))

			_, err := db.ExecContext(ctx, query)
			if err != nil {
				return fmt.Errorf("dropping orphaned function %s: %w", fn, err)
			}
		}
	}
	return nil
}

// applyMigrationsDDL creates the melange_migrations table if it doesn't exist.
// Also applies any necessary column migrations for existing tables.
func (m *Migrator) applyMigrationsDDL(ctx context.Context, db Execer) error {
	if _, err := db.ExecContext(ctx, migrationsDDL(m.databaseSchema)); err != nil {
		return fmt.Errorf("applying migrations DDL: %w", err)
	}
	// Add melange_version column if it doesn't exist (for existing tables)
	if _, err := db.ExecContext(ctx, addMelangeVersionColumn(m.databaseSchema)); err != nil {
		return fmt.Errorf("adding melange_version column: %w", err)
	}
	// Add function_checksums column if it doesn't exist (for existing tables)
	if _, err := db.ExecContext(ctx, addFunctionChecksumsColumn(m.databaseSchema)); err != nil {
		return fmt.Errorf("adding function_checksums column: %w", err)
	}
	return nil
}

// recordMigrationOnly inserts a migration record without re-applying functions.
// Used when phase 2 skip determines the generated SQL is identical to what's
// already installed — only the melange version or schema checksum changed.
func (m *Migrator) recordMigrationOnly(ctx context.Context, melangeVersion, schemaChecksum string, functionNames []string, functionChecksums map[string]string) error {
	if txer, ok := m.db.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := txer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("starting transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		if err := m.applyMigrationsDDL(ctx, tx); err != nil {
			return err
		}
		if err := m.insertMigrationRecord(ctx, tx, melangeVersion, schemaChecksum, functionNames, functionChecksums); err != nil {
			return err
		}
		return tx.Commit()
	}

	if err := m.applyMigrationsDDL(ctx, m.db); err != nil {
		return err
	}
	return m.insertMigrationRecord(ctx, m.db, melangeVersion, schemaChecksum, functionNames, functionChecksums)
}

// insertMigrationRecord records the migration in melange_migrations.
func (m *Migrator) insertMigrationRecord(ctx context.Context, db Execer, melangeVersion, schemaChecksum string, functionNames []string, functionChecksums map[string]string) error {
	checksumsJSON, err := json.Marshal(functionChecksums)
	if err != nil {
		return fmt.Errorf("marshaling function checksums: %w", err)
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`
			INSERT INTO %s (melange_version, schema_checksum, codegen_version, function_names, function_checksums)
			VALUES ($1, $2, $3, $4, $5)
		`,
		m.prefixIdent("melange_migrations"),
	), melangeVersion, schemaChecksum, CodegenVersion(), pq.Array(functionNames), string(checksumsJSON))
	if err != nil {
		return fmt.Errorf("inserting migration record: %w", err)
	}
	return nil
}

// MigrateWithTypesAndOptions performs database migration with options.
// This is the full-featured migration method that supports dry-run, two-phase
// skip detection, and orphan cleanup.
//
// Skip detection has two phases:
//   - Phase 1: If both the schema checksum and melange version match the last
//     migration, skip entirely without generating SQL.
//   - Phase 2: If phase 1 didn't skip (schema or version changed), generate
//     the SQL and compare function checksums against the last migration. If
//     every function is identical and no orphans exist, skip applying and
//     only record the new migration state.
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

	// 3. Fetch last migration record (needed for both skip phases)
	var lastMigration *MigrationRecord
	if !opts.Force && opts.DryRun == nil && schemaChecksum != "" {
		var err error
		lastMigration, err = m.getLastMigration(ctx, m.db)
		if err != nil {
			return fmt.Errorf("checking last migration: %w", err)
		}
		// Phase 1 skip: schema + melange version unchanged → skip entirely
		if shouldSkipMigration(lastMigration, schemaChecksum) {
			return nil
		}
	}

	// 4. Compute derived data (pure computation, no DB)
	closureRows := ComputeRelationClosure(types)

	// 5. Analyze relations and generate SQL
	analyses := AnalyzeRelations(types, closureRows)
	analyses = ComputeCanGenerate(analyses)
	inline := buildInlineSQLData(closureRows, analyses)
	generatedSQL, err := GenerateSQL(analyses, inline, m.databaseSchema)
	if err != nil {
		return fmt.Errorf("generating check SQL: %w", err)
	}

	// 6. Generate list functions
	listSQL, err := GenerateListSQL(analyses, inline, m.databaseSchema)
	if err != nil {
		return fmt.Errorf("generating list SQL: %w", err)
	}

	// 7. Collect expected function names and checksums for tracking
	expectedFunctions := CollectFunctionNames(analyses)
	namedFunctions := collectNamedFunctions(generatedSQL, listSQL, analyses)
	functionChecksums := ComputeFunctionChecksums(namedFunctions)

	// 8. Handle dry-run mode
	if opts.DryRun != nil {
		m.outputDryRun(opts.DryRun, opts.Version, schemaChecksum, generatedSQL, listSQL, expectedFunctions)
		return nil
	}

	// 9. Phase 2 skip: generated SQL is identical to what's already applied.
	// The schema or melange version changed (phase 1 didn't skip), but the
	// generated functions are byte-for-byte identical. Record the new version
	// but skip re-applying the functions.
	if !opts.Force && schemaChecksum != "" && shouldSkipApply(lastMigration, functionChecksums, expectedFunctions) {
		return m.recordMigrationOnly(ctx, opts.Version, schemaChecksum, expectedFunctions, functionChecksums)
	}

	// 10. Apply everything atomically
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
			if err := m.insertMigrationRecord(ctx, tx, opts.Version, schemaChecksum, expectedFunctions, functionChecksums); err != nil {
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
		if err := m.insertMigrationRecord(ctx, m.db, opts.Version, schemaChecksum, expectedFunctions, functionChecksums); err != nil {
			return err
		}
	}
	return nil
}

// outputDryRun writes the migration SQL to the provided writer.
func (m *Migrator) outputDryRun(w io.Writer, melangeVersion, schemaChecksum string, generatedSQL GeneratedSQL, listSQL ListGeneratedSQL, expectedFunctions []string) {
	// Header
	_, _ = fmt.Fprintf(w, "-- Melange Migration (dry-run)\n")
	if melangeVersion != "" {
		_, _ = fmt.Fprintf(w, "-- Melange version: %s\n", melangeVersion)
	}
	_, _ = fmt.Fprintf(w, "-- Schema checksum: %s\n", schemaChecksum)
	_, _ = fmt.Fprintf(w, "-- Codegen version: %s\n", CodegenVersion())
	_, _ = fmt.Fprintf(w, "\n")

	// Database schema
	if m.databaseSchema != "" {
		_, _ = fmt.Fprintf(w, "-- ============================================================\n")
		_, _ = fmt.Fprintf(w, "-- Database schema: %s\n", m.databaseSchema)
		_, _ = fmt.Fprintf(w, "-- NOTE: You must create this schema before running the migration:\n")
		_, _ = fmt.Fprintf(w, "--   CREATE SCHEMA IF NOT EXISTS %s;\n", sqldsl.QuoteIdent(m.databaseSchema))
		_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	}

	// Migrations DDL
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- DDL: Migration Tracking Table\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	_, _ = fmt.Fprintf(w, "%s\n\n", migrationsDDL(m.databaseSchema))

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
	if generatedSQL.BulkDispatcher != "" {
		_, _ = fmt.Fprintf(w, "%s\n\n", generatedSQL.BulkDispatcher)
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
	_, _ = fmt.Fprintf(w, "INSERT INTO %s (melange_version, schema_checksum, codegen_version, function_names)\n", m.prefixIdent("melange_migrations"))
	_, _ = fmt.Fprintf(w, "VALUES ('%s', '%s', '%s', ARRAY[%s]);\n", melangeVersion, schemaChecksum, CodegenVersion(), strings.Join(quotedFunctions, ", "))
}

func (m *Migrator) prefixIdent(identifier string) string {
	return sqldsl.PrefixIdent(identifier, m.databaseSchema)
}

func (m *Migrator) postgresSchema() string {
	if m.databaseSchema == "" {
		return "current_schema()"
	}

	return pq.QuoteLiteral(m.databaseSchema)
}
