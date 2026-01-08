package schema

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// Migrator handles loading authorization schemas into PostgreSQL.
// The migrator is idempotent - safe to run on every application startup.
//
// The migration process:
//  1. Creates/replaces check_permission and list_accessible_* functions
//  2. Loads generated SQL entrypoints into the database
//
// # Usage with Tooling Module
//
// For most use cases, use the tooling module's convenience functions which
// handle parsing and migration in one step:
//
//	import "github.com/pthm/melange/tooling"
//	err := tooling.Migrate(ctx, db, "schemas")
//
// Use the core Migrator directly when you have pre-parsed TypeDefinitions
// or need fine-grained control (DDL-only, status checks, etc.):
//
//	types, _ := tooling.ParseSchema("schemas/schema.fga")
//	migrator := schema.NewMigrator(db, "schemas")
//	err := migrator.MigrateWithTypes(ctx, types)
//
// This separation keeps the core melange package free of OpenFGA dependencies.
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

// applyDDLTx applies DDL to a specific Execer (typically a transaction).
// This is the transactional version of ApplyDDL.
func (m *Migrator) applyDDLTx(ctx context.Context, db Execer) error {
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
