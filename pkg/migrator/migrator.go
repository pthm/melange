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
	"time"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/schema"
)

// TypeDefinition re-exports [schema.TypeDefinition] for callers of this package.
type TypeDefinition = schema.TypeDefinition

// GeneratedSQL re-exports [sqlgen.GeneratedSQL].
type GeneratedSQL = sqlgen.GeneratedSQL

// ListGeneratedSQL re-exports [sqlgen.ListGeneratedSQL].
type ListGeneratedSQL = sqlgen.ListGeneratedSQL

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

	collectDispatcherFunctions = sqlgen.CollectDispatcherFunctions
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

	// IfDeployedChecksum, when non-nil, makes migration a compare-and-swap: it
	// proceeds only if the currently-deployed schema checksum equals the pointed-to
	// value, otherwise it aborts with *DeployedModelChangedError without applying
	// anything. An empty string matches a database with no migration recorded. It
	// is enforced in dry-run too, so a drift-gated preview aborts rather than
	// printing SQL against a drifted database.
	//
	// The checksum is verified up front and again inside the apply transaction, so
	// a migration committed while this one runs aborts it. It is not a hard lock:
	// two migrations that verify at the exact same instant could both proceed, but
	// concurrent migrations against one database are unsupported regardless.
	IfDeployedChecksum *string
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

	// SchemaFormat records how the schema was authored: "single" for a .fga
	// file or "modular" for an fga.mod manifest. Stored alongside the DSL so
	// `melange schema pull` can annotate its output.
	SchemaFormat string

	// IfDeployedChecksum is the compare-and-swap precondition; see MigrateOptions.
	IfDeployedChecksum *string
}

// DeployedModelChangedError is returned by migrate when --if-deployed-checksum
// does not match the currently-deployed schema checksum: the database drifted
// from the expected state, so nothing was applied.
type DeployedModelChangedError struct {
	Expected string // checksum the caller expected to be deployed
	Actual   string // checksum actually deployed ("" when no migration is recorded)
}

func (e *DeployedModelChangedError) Error() string {
	actual := e.Actual
	if actual == "" {
		actual = "none (no migration recorded)"
	}
	return fmt.Sprintf("deployed model changed: expected checksum %s but database has %s; nothing was applied",
		e.Expected, actual)
}

// driftGuardError returns a *DeployedModelChangedError when expected is non-nil
// and does not match the deployed checksum (rec's checksum, or "" when rec is
// nil). It returns nil when the guard is unset or satisfied.
func driftGuardError(expected *string, rec *MigrationRecord) error {
	if expected == nil {
		return nil
	}
	deployed := ""
	if rec != nil {
		deployed = rec.SchemaChecksum
	}
	if deployed != *expected {
		return &DeployedModelChangedError{Expected: *expected, Actual: deployed}
	}
	return nil
}

// MigrationRecord represents a row in the melange_migrations table.
type MigrationRecord struct {
	// ID and MigratedAt identify the row and when it was written. Populated by
	// reads (getLastMigration); ignored on writes (the DB assigns them).
	ID         int
	MigratedAt time.Time

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

	// SchemaDSL, SchemaFormat, and ModelJSON make the database self-describing.
	// They are populated only when the corresponding columns exist and were
	// written by a version that records the model. On older records SchemaDSL is
	// "" (treat as "no model recorded") and ModelJSON is nil.
	SchemaDSL    string
	SchemaFormat string
	ModelJSON    []byte
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
	return &Migrator{db: db, schemaPath: schemaPath, databaseSchema: "public"}
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

	// Apply per-relation explain functions before the explain dispatcher
	// (dispatcher CASE expressions name the per-relation functions).
	for i, fn := range gen.ExplainFunctions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying explain function %d: %w", i, err)
		}
	}
	if gen.ExplainDispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.ExplainDispatcher); err != nil {
			return fmt.Errorf("applying explain dispatcher: %w", err)
		}
	}

	// Apply per-relation expand functions before the expand dispatcher
	// (dispatcher CASE expressions name the per-relation functions).
	for i, fn := range gen.ExpandFunctions {
		if _, err := db.ExecContext(ctx, fn); err != nil {
			return fmt.Errorf("applying expand function %d: %w", i, err)
		}
	}
	if gen.ExpandDispatcher != "" {
		if _, err := db.ExecContext(ctx, gen.ExpandDispatcher); err != nil {
			return fmt.Errorf("applying expand dispatcher: %w", err)
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
//
// Optional columns (function_checksums, schema_dsl, schema_format, model_json)
// were added after the original DDL, so the SELECT is built from the columns
// that actually exist. This keeps reads compatible with databases migrated by
// any earlier version without a branch per column combination.
func (m *Migrator) getLastMigration(ctx context.Context, db Execer) (*MigrationRecord, error) {
	// First check if the migrations table exists
	tableExists, err := m.migrationTableExists(ctx, db)
	if err != nil {
		return nil, err
	}
	if !tableExists {
		return nil, nil // No migrations table yet
	}

	cols, err := m.migrationColumns(ctx, db)
	if err != nil {
		return nil, err
	}

	// id, migrated_at, and the original three columns are always present.
	// melange_version, function_checksums, and the model columns were each added
	// later, so probe for them; a database migrated by an old version that lacks
	// one must still read cleanly (status now depends on this).
	var (
		rec            MigrationRecord
		melangeVersion sql.NullString
		checksumsJSON  sql.NullString
		schemaDSL      sql.NullString
		schemaFormat   sql.NullString
		modelJSON      sql.NullString
	)
	selects := []string{"id", "migrated_at", "schema_checksum", "codegen_version", "function_names"}
	targets := []any{&rec.ID, &rec.MigratedAt, &rec.SchemaChecksum, &rec.CodegenVersion, pq.Array(&rec.FunctionNames)}
	if cols["melange_version"] {
		selects = append(selects, "melange_version")
		targets = append(targets, &melangeVersion)
	}
	if cols["function_checksums"] {
		selects = append(selects, "function_checksums::TEXT")
		targets = append(targets, &checksumsJSON)
	}
	if cols["schema_dsl"] {
		selects = append(selects, "schema_dsl")
		targets = append(targets, &schemaDSL)
	}
	if cols["schema_format"] {
		selects = append(selects, "schema_format")
		targets = append(targets, &schemaFormat)
	}
	if cols["model_json"] {
		selects = append(selects, "model_json::TEXT")
		targets = append(targets, &modelJSON)
	}

	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY id DESC LIMIT 1",
		strings.Join(selects, ", "), m.prefixIdent("melange_migrations"))
	err = db.QueryRowContext(ctx, query).Scan(targets...)
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
	rec.MelangeVersion = melangeVersion.String
	rec.SchemaDSL = schemaDSL.String
	rec.SchemaFormat = schemaFormat.String
	if modelJSON.Valid && modelJSON.String != "" && modelJSON.String != "{}" {
		rec.ModelJSON = []byte(modelJSON.String)
	}
	return &rec, nil
}

// migrationTableExists reports whether the melange_migrations table exists in
// the configured schema. Used to distinguish an un-migrated database from one
// whose columns are merely hidden by privileges (which would make an
// information_schema probe misleadingly return nothing).
func (m *Migrator) migrationTableExists(ctx context.Context, db Execer) (bool, error) {
	var exists bool
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
	)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking melange_migrations table: %w", err)
	}
	return exists, nil
}

// driftGuardInTx re-evaluates the drift guard against the latest record visible
// to db (a transaction). Read-committed visibility means a migration committed
// since the up-front check is seen here, so a concurrent change that lands while
// this migration applies is caught before the record is written.
func (m *Migrator) driftGuardInTx(ctx context.Context, db Execer, expected *string) error {
	if expected == nil {
		return nil
	}
	rec, err := m.getLastMigration(ctx, db)
	if err != nil {
		return err
	}
	return driftGuardError(expected, rec)
}

// migrationColumns returns the set of column names present in the
// melange_migrations table, used to build a compatible SELECT.
func (m *Migrator) migrationColumns(ctx context.Context, db Execer) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`
			SELECT column_name FROM information_schema.columns
			WHERE table_name = 'melange_migrations'
			AND table_schema = %s
		`,
		m.postgresSchema(),
	))
	if err != nil {
		return nil, fmt.Errorf("listing melange_migrations columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cols := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning column name: %w", err)
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// DeployedModel is the authorization model recorded by the most recent
// migration — the source for `melange schema pull`, drift detection, and diffs.
type DeployedModel struct {
	DSL            string                  // OpenFGA DSL that produced the deployment
	Format         string                  // "single" or "modular"
	Types          []schema.TypeDefinition // parsed model (nil if only DSL was recorded)
	SchemaChecksum string
	MelangeVersion string
	MigratedAt     time.Time
}

// GetDeployedModel returns the model recorded by the most recent migration, or
// nil if none has been recorded — either because the database has never been
// migrated or because it was migrated before model storage existed (SchemaDSL
// empty). Callers surface a "re-run migrate to record the model" hint for nil.
func (m *Migrator) GetDeployedModel(ctx context.Context) (*DeployedModel, error) {
	rec, err := m.GetLastMigration(ctx)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.SchemaDSL == "" {
		return nil, nil
	}
	types, err := schema.UnmarshalModel(rec.ModelJSON)
	if err != nil {
		return nil, fmt.Errorf("decoding deployed model: %w", err)
	}
	return &DeployedModel{
		DSL:            rec.SchemaDSL,
		Format:         rec.SchemaFormat,
		Types:          types,
		SchemaChecksum: rec.SchemaChecksum,
		MelangeVersion: rec.MelangeVersion,
		MigratedAt:     rec.MigratedAt,
	}, nil
}

// GetMigrationHistory returns up to limit migration records, most recent first.
// It reads a lightweight subset (no DSL or model JSON) suitable for an audit
// listing, and tolerates databases migrated by older versions that lack the
// melange_version or schema_format columns. Returns nil when no migrations table
// exists.
func (m *Migrator) GetMigrationHistory(ctx context.Context, limit int) ([]MigrationRecord, error) {
	if limit < 1 {
		return nil, fmt.Errorf("limit must be at least 1, got %d", limit)
	}
	exists, err := m.migrationTableExists(ctx, m.db)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	cols, err := m.migrationColumns(ctx, m.db)
	if err != nil {
		return nil, err
	}

	selects := []string{"id", "migrated_at", "schema_checksum", "codegen_version", "function_names"}
	hasVersion := cols["melange_version"]
	hasFormat := cols["schema_format"]
	if hasVersion {
		selects = append(selects, "melange_version")
	}
	if hasFormat {
		selects = append(selects, "schema_format")
	}

	query := fmt.Sprintf("SELECT %s FROM %s ORDER BY id DESC LIMIT $1",
		strings.Join(selects, ", "), m.prefixIdent("melange_migrations"))
	rows, err := m.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("querying migration history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var history []MigrationRecord
	for rows.Next() {
		var (
			rec                MigrationRecord
			melangeVer, format sql.NullString
		)
		targets := []any{&rec.ID, &rec.MigratedAt, &rec.SchemaChecksum, &rec.CodegenVersion, pq.Array(&rec.FunctionNames)}
		if hasVersion {
			targets = append(targets, &melangeVer)
		}
		if hasFormat {
			targets = append(targets, &format)
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, fmt.Errorf("scanning migration row: %w", err)
		}
		rec.MelangeVersion = melangeVer.String
		rec.SchemaFormat = format.String
		history = append(history, rec)
	}
	return history, rows.Err()
}

// migrationRecordMatches reports whether the last migration was recorded with
// the same schema checksum and codegen version as the current run.
//
// A record written before model storage existed (SchemaDSL empty) never
// matches: falling through lets phase 2 backfill schema_dsl/model_json without
// re-applying the generated functions, so a plain rerun after upgrading makes
// GetDeployedModel / schema pull work without --force.
func migrationRecordMatches(lastMigration *MigrationRecord, schemaChecksum string) bool {
	if lastMigration == nil {
		return false
	}
	return lastMigration.SchemaChecksum == schemaChecksum &&
		lastMigration.CodegenVersion == CodegenVersion() &&
		lastMigration.SchemaDSL != ""
}

// shouldSkipMigration returns true if the schema and codegen version are unchanged.
// This is the fast-path (phase 1) skip: no SQL generation needed at all.
//
// Unstamped "dev" builds never skip here: "dev" is a constant, so a rebuilt
// binary with changed codegen would look identical to the last migration.
// Falling through lets the phase 2 checksum comparison (which includes
// dispatchers) decide. Released binaries and go-install/library builds report a
// real version (lib/version resolves it from ldflags or embedded build info),
// so they still get the fast-path skip.
func shouldSkipMigration(lastMigration *MigrationRecord, schemaChecksum string) bool {
	if CodegenVersion() == "dev" {
		return false
	}
	return migrationRecordMatches(lastMigration, schemaChecksum)
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
	for _, stmt := range migrationsTableDDL(m.databaseSchema) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("applying migrations DDL: %w", err)
		}
	}
	return nil
}

// migrationWrite carries the fields persisted to a new melange_migrations row.
type migrationWrite struct {
	MelangeVersion    string
	SchemaChecksum    string
	FunctionNames     []string
	FunctionChecksums map[string]string
	SchemaDSL         string
	SchemaFormat      string
	ModelJSON         []byte
}

// modelJSONOrEmpty returns the model JSON, substituting an empty JSON object for
// the NOT NULL model_json column when no model was captured (e.g. embedded-string
// migrations without content).
func (w migrationWrite) modelJSONOrEmpty() []byte {
	if len(w.ModelJSON) == 0 {
		return []byte("{}")
	}
	return w.ModelJSON
}

// recordMigrationOnly inserts a migration record without re-applying functions.
// Used when phase 2 skip determines the generated SQL is identical to what's
// already installed — only the melange version or schema checksum changed. The
// drift guard is re-verified in-transaction before the insert, like the full
// apply path, so a concurrent change is not overwritten.
func (m *Migrator) recordMigrationOnly(ctx context.Context, w migrationWrite, ifDeployed *string) error {
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
		if err := m.driftGuardInTx(ctx, tx, ifDeployed); err != nil {
			return err
		}
		if err := m.insertMigrationRecord(ctx, tx, w); err != nil {
			return err
		}
		return tx.Commit()
	}

	if err := m.applyMigrationsDDL(ctx, m.db); err != nil {
		return err
	}
	if err := m.driftGuardInTx(ctx, m.db, ifDeployed); err != nil {
		return err
	}
	return m.insertMigrationRecord(ctx, m.db, w)
}

// insertMigrationRecord records the migration in melange_migrations.
func (m *Migrator) insertMigrationRecord(ctx context.Context, db Execer, w migrationWrite) error {
	checksumsJSON, err := json.Marshal(w.FunctionChecksums)
	if err != nil {
		return fmt.Errorf("marshaling function checksums: %w", err)
	}
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		`
			INSERT INTO %s (melange_version, schema_checksum, codegen_version, function_names, function_checksums, schema_dsl, schema_format, model_json)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`,
		m.prefixIdent("melange_migrations"),
	), w.MelangeVersion, w.SchemaChecksum, CodegenVersion(), pq.Array(w.FunctionNames), string(checksumsJSON),
		w.SchemaDSL, w.SchemaFormat, string(w.modelJSONOrEmpty()))
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
	_, err := m.migrateWithTypesAndOptions(ctx, types, opts)
	return err
}

// migrateWithTypesAndOptions implements MigrateWithTypesAndOptions and
// additionally reports whether the migration was a no-op (both skip phases
// count; recording a new migration state does not).
func (m *Migrator) migrateWithTypesAndOptions(ctx context.Context, types []TypeDefinition, opts InternalMigrateOptions) (skipped bool, err error) {
	// 1. Validate schema before any computation
	if err := DetectCycles(types); err != nil {
		return false, err
	}

	// 2. Compute schema checksum if content provided
	var schemaChecksum string
	if opts.SchemaContent != "" {
		schemaChecksum = ComputeSchemaChecksum(opts.SchemaContent)
	}

	// 3. Fetch last migration record (needed for the drift guard and both skip
	// phases). The guard needs it even under --force and --dry-run.
	var lastMigration *MigrationRecord
	if opts.IfDeployedChecksum != nil ||
		(!opts.Force && opts.DryRun == nil && schemaChecksum != "") {
		lastMigration, err = m.getLastMigration(ctx, m.db)
		if err != nil {
			return false, fmt.Errorf("checking last migration: %w", err)
		}
	}

	// Drift guard: --if-deployed-checksum makes migrate a compare-and-swap. Abort
	// early if the database isn't at the expected checksum — including in dry-run,
	// so a drift-gated preview fails rather than printing SQL against a drifted
	// database. The apply path re-checks inside its transaction (see below) to
	// catch drift committed while this migration runs.
	if err := driftGuardError(opts.IfDeployedChecksum, lastMigration); err != nil {
		return false, err
	}

	// Phase 1 skip: schema + codegen version unchanged → skip entirely
	if !opts.Force && opts.DryRun == nil && schemaChecksum != "" {
		if shouldSkipMigration(lastMigration, schemaChecksum) {
			return true, nil
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
		return false, fmt.Errorf("generating check SQL: %w", err)
	}

	// 6. Generate list functions
	listSQL, err := GenerateListSQL(analyses, inline, m.databaseSchema)
	if err != nil {
		return false, fmt.Errorf("generating list SQL: %w", err)
	}

	// 7. Collect expected function names and checksums for tracking.
	// Dispatchers are checksummed alongside specialized functions so that a
	// codegen change altering only dispatcher SQL still defeats the phase 2
	// skip below.
	expectedFunctions := CollectFunctionNames(analyses)
	namedFunctions := collectNamedFunctions(generatedSQL, listSQL, analyses)
	namedFunctions = append(namedFunctions, collectDispatcherFunctions(generatedSQL, listSQL)...)
	functionChecksums := ComputeFunctionChecksums(namedFunctions)

	// Serialize the parsed model so the migration record is self-describing.
	modelJSON, err := schema.MarshalModel(types)
	if err != nil {
		return false, fmt.Errorf("marshaling model: %w", err)
	}
	write := migrationWrite{
		MelangeVersion:    opts.Version,
		SchemaChecksum:    schemaChecksum,
		FunctionNames:     expectedFunctions,
		FunctionChecksums: functionChecksums,
		SchemaDSL:         opts.SchemaContent,
		SchemaFormat:      opts.SchemaFormat,
		ModelJSON:         modelJSON,
	}

	// 8. Handle dry-run mode
	if opts.DryRun != nil {
		m.outputDryRun(opts.DryRun, write, generatedSQL, listSQL)
		return false, nil
	}

	// 9. Phase 2 skip: generated SQL is identical to what's already applied.
	// The schema or melange version changed (phase 1 didn't skip), but the
	// generated functions are byte-for-byte identical. Record the new version
	// but skip re-applying the functions.
	if !opts.Force && schemaChecksum != "" && shouldSkipApply(lastMigration, functionChecksums, expectedFunctions) {
		// Nothing changed at all (dev-build re-run that bypassed phase 1):
		// pure no-op, don't insert a migration record per run.
		if migrationRecordMatches(lastMigration, schemaChecksum) {
			return true, nil
		}
		return false, m.recordMigrationOnly(ctx, write, opts.IfDeployedChecksum)
	}

	// 10. Apply everything atomically
	if txer, ok := m.db.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := txer.BeginTx(ctx, nil)
		if err != nil {
			return false, fmt.Errorf("starting transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		// Apply migrations DDL (creates tracking table)
		if err := m.applyMigrationsDDL(ctx, tx); err != nil {
			return false, err
		}

		// Get current functions before applying new ones (for orphan detection)
		currentFunctions, err := m.getCurrentFunctions(ctx, tx)
		if err != nil {
			return false, fmt.Errorf("getting current functions: %w", err)
		}

		// Apply generated specialized check functions
		if err := m.applyGeneratedSQL(ctx, tx, generatedSQL); err != nil {
			return false, err
		}

		// Apply generated specialized list functions
		if err := m.applyGeneratedListSQL(ctx, tx, listSQL); err != nil {
			return false, err
		}

		// Drop orphaned functions
		if err := m.dropOrphanedFunctions(ctx, tx, currentFunctions, expectedFunctions); err != nil {
			return false, err
		}

		// Re-verify the drift guard inside the transaction before recording, so a
		// migration committed while we were applying aborts this one (rollback).
		if err := m.driftGuardInTx(ctx, tx, opts.IfDeployedChecksum); err != nil {
			return false, err
		}

		// Record migration
		if schemaChecksum != "" {
			if err := m.insertMigrationRecord(ctx, tx, write); err != nil {
				return false, err
			}
		}

		return false, tx.Commit()
	}

	// Fall back to non-transactional (for *sql.Conn). Without a transaction there
	// is no rollback, so the drift guard is re-checked BEFORE any function is
	// applied — a failure then leaves nothing applied, matching the error.
	if err := m.applyMigrationsDDL(ctx, m.db); err != nil {
		return false, err
	}
	if err := m.driftGuardInTx(ctx, m.db, opts.IfDeployedChecksum); err != nil {
		return false, err
	}
	currentFunctions, err := m.getCurrentFunctions(ctx, m.db)
	if err != nil {
		return false, fmt.Errorf("getting current functions: %w", err)
	}
	if err := m.applyGeneratedSQL(ctx, m.db, generatedSQL); err != nil {
		return false, err
	}
	if err := m.applyGeneratedListSQL(ctx, m.db, listSQL); err != nil {
		return false, err
	}
	if err := m.dropOrphanedFunctions(ctx, m.db, currentFunctions, expectedFunctions); err != nil {
		return false, err
	}
	if schemaChecksum != "" {
		if err := m.insertMigrationRecord(ctx, m.db, write); err != nil {
			return false, err
		}
	}
	return false, nil
}

// outputDryRun writes the migration SQL to the provided writer.
func (m *Migrator) outputDryRun(w io.Writer, write migrationWrite, generatedSQL GeneratedSQL, listSQL ListGeneratedSQL) {
	melangeVersion := write.MelangeVersion
	schemaChecksum := write.SchemaChecksum
	expectedFunctions := write.FunctionNames

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

	// Migrations DDL (including column migrations for legacy tables)
	_, _ = fmt.Fprintf(w, "-- ============================================================\n")
	_, _ = fmt.Fprintf(w, "-- DDL: Migration Tracking Table\n")
	_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
	for _, stmt := range migrationsTableDDL(m.databaseSchema) {
		_, _ = fmt.Fprintf(w, "%s\n\n", stmt)
	}

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

	// Explain functions + dispatcher (Stage 1: slice 1 — direct-grant only,
	// with cycle detection and a no-entry sentinel for unknown pairs).
	if len(generatedSQL.ExplainFunctions) > 0 || generatedSQL.ExplainDispatcher != "" {
		_, _ = fmt.Fprintf(w, "-- ============================================================\n")
		_, _ = fmt.Fprintf(w, "-- Explain Functions (%d functions)\n", len(generatedSQL.ExplainFunctions))
		_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
		for _, fn := range generatedSQL.ExplainFunctions {
			_, _ = fmt.Fprintf(w, "%s\n\n", fn)
		}
		if generatedSQL.ExplainDispatcher != "" {
			_, _ = fmt.Fprintf(w, "%s\n\n", generatedSQL.ExplainDispatcher)
		}
	}

	// Expand functions + dispatcher (Stage 2: slice 2.1 — direct + computed
	// only; TTU, intersection, exclusion, usersets, wildcards route to the
	// empty-leaf sentinel until later slices land).
	if len(generatedSQL.ExpandFunctions) > 0 || generatedSQL.ExpandDispatcher != "" {
		_, _ = fmt.Fprintf(w, "-- ============================================================\n")
		_, _ = fmt.Fprintf(w, "-- Expand Functions (%d functions)\n", len(generatedSQL.ExpandFunctions))
		_, _ = fmt.Fprintf(w, "-- ============================================================\n\n")
		for _, fn := range generatedSQL.ExpandFunctions {
			_, _ = fmt.Fprintf(w, "%s\n\n", fn)
		}
		if generatedSQL.ExpandDispatcher != "" {
			_, _ = fmt.Fprintf(w, "%s\n\n", generatedSQL.ExpandDispatcher)
		}
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
		quotedFunctions[i] = sqldsl.Lit(fn).SQL()
	}

	// Emit every persisted column — including function_checksums and the
	// deployed-model columns — so a separately-applied dry-run script records a
	// migration identical to a normal migrate. Without function_checksums a later
	// real migrate could not take the phase-2 "skip-apply" fast path. sqldsl.Lit
	// escapes single quotes, so multi-line DSL and JSON are safe; the JSON text is
	// cast to the JSONB columns implicitly.
	checksumsJSON, err := json.Marshal(write.FunctionChecksums)
	if err != nil {
		// FunctionChecksums is a plain map[string]string; marshaling cannot fail.
		checksumsJSON = []byte("{}")
	}
	_, _ = fmt.Fprintf(w, "INSERT INTO %s (melange_version, schema_checksum, codegen_version, function_names, function_checksums, schema_dsl, schema_format, model_json)\n", m.prefixIdent("melange_migrations"))
	_, _ = fmt.Fprintf(w, "VALUES (%s, %s, %s, ARRAY[%s], %s, %s, %s, %s);\n",
		sqldsl.Lit(melangeVersion).SQL(),
		sqldsl.Lit(schemaChecksum).SQL(),
		sqldsl.Lit(CodegenVersion()).SQL(),
		strings.Join(quotedFunctions, ", "),
		sqldsl.Lit(string(checksumsJSON)).SQL(),
		sqldsl.Lit(write.SchemaDSL).SQL(),
		sqldsl.Lit(write.SchemaFormat).SQL(),
		sqldsl.Lit(string(write.modelJSONOrEmpty())).SQL(),
	)
}

func (m *Migrator) prefixIdent(identifier string) string {
	return sqldsl.PrefixIdent(identifier, m.databaseSchema)
}

func (m *Migrator) postgresSchema() string {
	return sqldsl.PostgresSchemaExpr(m.databaseSchema)
}
