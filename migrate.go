package melange

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	melangesql "github.com/pthm/melange/sql"
)

// Migrator handles loading authorization schemas into PostgreSQL.
// The migrator is idempotent - safe to run on every application startup.
//
// The migration process:
//  1. Creates melange_model table (if not exists)
//  2. Creates/replaces check_permission and list_accessible_* functions
//  3. Loads pre-parsed authorization rules into the database
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
//	migrator := melange.NewMigrator(db, "schemas")
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

// ApplyDDL applies the melange_model table, closure table, and functions.
// This is idempotent (CREATE TABLE IF NOT EXISTS, CREATE OR REPLACE FUNCTION,
// CREATE INDEX IF NOT EXISTS).
//
// The DDL creates:
//   - melange_model table with performance indexes (stores parsed FGA schema)
//   - melange_relation_closure table (precomputed transitive closure)
//   - check_permission function (evaluates permissions)
//   - list_accessible_objects function (reverse lookup)
//   - has_tuple function (direct tuple checks)
//
// This can be called independently of schema migration to update function
// implementations without reloading the authorization model.
func (m *Migrator) ApplyDDL(ctx context.Context) error {
	// Apply model table and indexes
	if _, err := m.db.ExecContext(ctx, melangesql.ModelSQL); err != nil {
		return fmt.Errorf("applying model.sql: %w", err)
	}

	// Apply closure table and indexes
	if _, err := m.db.ExecContext(ctx, melangesql.ClosureSQL); err != nil {
		return fmt.Errorf("applying closure.sql: %w", err)
	}

	// Apply functions in dependency order.
	for _, file := range melangesql.FunctionsSQLFiles {
		if _, err := m.db.ExecContext(ctx, file.Contents); err != nil {
			return fmt.Errorf("applying %s: %w", file.Path, err)
		}
	}

	return nil
}

// MigrateWithTypes performs database migration using pre-parsed type definitions.
// This is the core migration method used by the tooling package's Migrate function.
//
// The method:
//  1. Validates the schema (checks for cycles)
//  2. Applies DDL (creates tables and functions)
//  3. Converts type definitions to authorization models
//  4. Computes relation closure for efficient implied-by resolution
//  5. Truncates and repopulates melange_model and melange_relation_closure
//
// This is idempotent - safe to run multiple times with the same types.
//
// Uses a transaction if the db supports it (*sql.DB). This ensures
// the schema is updated atomically or not at all.
func (m *Migrator) MigrateWithTypes(ctx context.Context, types []TypeDefinition) error {
	// Validate schema before applying to database
	if err := DetectCycles(types); err != nil {
		return err
	}

	// Apply DDL first
	if err := m.ApplyDDL(ctx); err != nil {
		return err
	}

	models := ToAuthzModels(types)
	closureRows := ComputeRelationClosure(types)
	usersetRules := ToUsersetRules(types, closureRows)

	// Use a transaction if the db supports it
	if txer, ok := m.db.(interface {
		BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	}); ok {
		tx, err := txer.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("starting transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		if err := m.applyTypes(ctx, tx, types); err != nil {
			return err
		}

		if err := m.applyModels(ctx, tx, models); err != nil {
			return err
		}

		if err := m.applyClosure(ctx, tx, closureRows); err != nil {
			return err
		}

		if err := m.applyUsersetRules(ctx, tx, usersetRules); err != nil {
			return err
		}

		return tx.Commit()
	}

	// Fall back to non-transactional (for *sql.Conn)
	if err := m.applyTypes(ctx, m.db, types); err != nil {
		return err
	}
	if err := m.applyModels(ctx, m.db, models); err != nil {
		return err
	}
	if err := m.applyClosure(ctx, m.db, closureRows); err != nil {
		return err
	}
	return m.applyUsersetRules(ctx, m.db, usersetRules)
}

// applyTypes truncates and repopulates the melange_types table.
// This table stores all defined types for validation purposes.
func (m *Migrator) applyTypes(ctx context.Context, db Execer, types []TypeDefinition) error {
	// TRUNCATE is transactional in PostgreSQL
	_, err := db.ExecContext(ctx, "TRUNCATE melange_types")
	if err != nil {
		return fmt.Errorf("truncating melange_types: %w", err)
	}

	if len(types) == 0 {
		return nil
	}

	// Build bulk insert
	values := make([]string, 0, len(types))
	args := make([]any, 0, len(types))
	argIdx := 1

	for _, t := range types {
		values = append(values, fmt.Sprintf("($%d)", argIdx))
		args = append(args, t.Name)
		argIdx++
	}

	query := fmt.Sprintf(
		"INSERT INTO melange_types (object_type) VALUES %s",
		strings.Join(values, ", "),
	)

	_, err = db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("inserting types: %w", err)
	}

	return nil
}

// applyModels truncates and repopulates the melange_model table.
// This is the core of the migration: converting parsed FGA rules into
// database rows that check_permission can query.
//
// TRUNCATE is transactional in PostgreSQL, ensuring atomicity when called
// within a transaction. If any insert fails, the whole migration rolls back.
//
// Uses bulk INSERT for efficiency when loading large schemas.
func (m *Migrator) applyModels(ctx context.Context, db Execer, models []AuthzModel) error {
	// TRUNCATE is transactional in PostgreSQL
	_, err := db.ExecContext(ctx, "TRUNCATE melange_model RESTART IDENTITY")
	if err != nil {
		return fmt.Errorf("truncating melange_model: %w", err)
	}

	if len(models) == 0 {
		return nil
	}

	// Build bulk insert
	values := make([]string, 0, len(models))
	args := make([]any, 0, len(models)*16)
	argIdx := 1

	for _, model := range models {
		values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d)",
			argIdx, argIdx+1, argIdx+2, argIdx+3, argIdx+4, argIdx+5, argIdx+6, argIdx+7, argIdx+8, argIdx+9, argIdx+10, argIdx+11, argIdx+12, argIdx+13, argIdx+14, argIdx+15))
		args = append(args, model.ObjectType, model.Relation,
			model.SubjectType, model.ImpliedBy, model.ParentRelation, model.ExcludedRelation,
			model.SubjectWildcard, model.ExcludedParentRelation, model.ExcludedParentType, model.SubjectRelation,
			model.RuleGroupID, model.RuleGroupMode, model.CheckRelation, model.CheckExcludedRelation,
			model.CheckParentRelation, model.CheckParentType)
		argIdx += 16
	}

	query := fmt.Sprintf(
		"INSERT INTO melange_model (object_type, relation, subject_type, implied_by, parent_relation, excluded_relation, subject_wildcard, excluded_parent_relation, excluded_parent_type, subject_relation, rule_group_id, rule_group_mode, check_relation, check_excluded_relation, check_parent_relation, check_parent_type) VALUES %s",
		strings.Join(values, ", "),
	)

	_, err = db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("inserting schema: %w", err)
	}

	return nil
}

// applyClosure truncates and repopulates the melange_relation_closure table.
// The closure table stores precomputed transitive implied-by relationships,
// enabling efficient permission checks without recursive function calls.
//
// Uses bulk INSERT for efficiency when loading large schemas.
func (m *Migrator) applyClosure(ctx context.Context, db Execer, closureRows []ClosureRow) error {
	// TRUNCATE is transactional in PostgreSQL
	_, err := db.ExecContext(ctx, "TRUNCATE melange_relation_closure RESTART IDENTITY")
	if err != nil {
		return fmt.Errorf("truncating melange_relation_closure: %w", err)
	}

	if len(closureRows) == 0 {
		return nil
	}

	// Build bulk insert
	values := make([]string, 0, len(closureRows))
	args := make([]any, 0, len(closureRows)*4)
	argIdx := 1

	for _, row := range closureRows {
		values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d)",
			argIdx, argIdx+1, argIdx+2, argIdx+3))
		args = append(args, row.ObjectType, row.Relation, row.SatisfyingRelation, row.ViaPath)
		argIdx += 4
	}

	query := fmt.Sprintf(
		"INSERT INTO melange_relation_closure (object_type, relation, satisfying_relation, via_path) VALUES %s",
		strings.Join(values, ", "),
	)

	_, err = db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("inserting closure: %w", err)
	}

	return nil
}

// applyUsersetRules truncates and repopulates the melange_userset_rules table.
// These rows are derived from userset references plus relation closure.
func (m *Migrator) applyUsersetRules(ctx context.Context, db Execer, rules []UsersetRule) error {
	_, err := db.ExecContext(ctx, "TRUNCATE melange_userset_rules RESTART IDENTITY")
	if err != nil {
		return fmt.Errorf("truncating melange_userset_rules: %w", err)
	}

	if len(rules) == 0 {
		return nil
	}

	values := make([]string, 0, len(rules))
	args := make([]any, 0, len(rules)*6)
	argIdx := 1

	for _, rule := range rules {
		values = append(values, fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d)",
			argIdx, argIdx+1, argIdx+2, argIdx+3, argIdx+4, argIdx+5))
		args = append(args, rule.ObjectType, rule.Relation, rule.TupleRelation, rule.SubjectType, rule.SubjectRelation, rule.SubjectRelationSatisfying)
		argIdx += 6
	}

	query := fmt.Sprintf(
		"INSERT INTO melange_userset_rules (object_type, relation, tuple_relation, subject_type, subject_relation, subject_relation_satisfying) VALUES %s",
		strings.Join(values, ", "),
	)

	_, err = db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("inserting userset rules: %w", err)
	}

	return nil
}

// Status represents the current migration state.
// Use GetStatus to check if the authorization system is properly configured.
type Status struct {
	// SchemaExists indicates if the schema.fga file exists on disk.
	SchemaExists bool

	// ModelCount is the number of rows in the melange_model table.
	// Zero means the schema hasn't been loaded (run `melange migrate`).
	ModelCount int64

	// ClosureCount is the number of rows in the melange_relation_closure table.
	// This table stores precomputed transitive implied-by relationships.
	ClosureCount int64

	// IndexCount is the number of melange-related indexes found.
	// Expected to be at least 5 after a successful migration.
	IndexCount int

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

	// Check model count
	err := m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM melange_model").Scan(&status.ModelCount)
	if err != nil {
		return nil, fmt.Errorf("counting melange_model rows: %w", err)
	}

	// Check closure count
	err = m.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM melange_relation_closure").Scan(&status.ClosureCount)
	if err != nil {
		return nil, fmt.Errorf("counting melange_relation_closure rows: %w", err)
	}

	// Check index count
	err = m.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_indexes
		WHERE indexname LIKE 'idx_melange_%'
	`).Scan(&status.IndexCount)
	if err != nil {
		return nil, fmt.Errorf("counting melange indexes: %w", err)
	}

	// Check if melange_tuples relation exists (view, table, or materialized view)
	var tuplesExists bool
	err = m.db.QueryRowContext(ctx, `
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
