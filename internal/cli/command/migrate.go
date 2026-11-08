package command

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/migrator"
)

var (
	migrateDB                 string
	migrateDBSchema           string
	migrateSchema             string
	migrateDryRun             bool
	migrateForce              bool
	migrateIfDeployedChecksum string
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply schema to database",
	Long: `Apply authorization schema to PostgreSQL database.

The generated SQL API follows the sql section of the configuration, with the
exposure flags on top. Each run applies exactly that exposure: a run without
an sql section or exposure flags restores the default (every relation, every
function family), so keep a lasting restriction in the configuration file.

Functions the new SQL no longer generates are dropped. When views, policies,
rules or functions depend on one, migrate refuses and lists them; --force
drops them together with their dependents (CASCADE).`,
	Example: `  # Apply schema to database
  melange migrate --db postgres://localhost/mydb

  # Use a different database schema
  melange migrate --db postgres://localhost/mydb --db-schema myschema

  # Preview migration without applying
  melange migrate --db postgres://localhost/mydb --dry-run

  # Re-apply functions changed outside Melange; also drops the views and
  # policies that call removed functions (CASCADE) instead of refusing
  melange migrate --db postgres://localhost/mydb --force

  # Expose only the can_* relations, without the expand and explain functions
  melange migrate --include-relation 'can_*' --no-expand --no-explain

  # Drift-safe apply: abort unless the deployed checksum matches (CI gate)
  melange migrate --env production --if-deployed-checksum "$CURRENT"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Warn if generate.migration.output is configured
		if cfg.Generate.Migration.Output != "" && !quiet {
			fmt.Fprintln(os.Stderr, "WARNING: generate.migration.output is configured in your melange config.")
			fmt.Fprintln(os.Stderr, "         This suggests you may be using 'melange generate migration' to produce")
			fmt.Fprintln(os.Stderr, "         SQL files for an external migration framework. Running 'melange migrate'")
			fmt.Fprintln(os.Stderr, "         alongside generated migrations can cause state tracking conflicts.")
			fmt.Fprintln(os.Stderr)
		}

		// Resolve values
		databaseSchema := resolveString(migrateDBSchema, cfg.Database.Schema, "public")
		schemaPath := resolveString(migrateSchema, cfg.Schema)
		dryRun := resolveBool(migrateDryRun, cfg.Migrate.DryRun)
		force := resolveBool(migrateForce, cfg.Migrate.Force)

		exposure, err := resolveExposure(cmd, cfg.SQL)
		if err != nil {
			return err
		}

		// Get DSN
		dsn, err := resolveDSN(migrateDB)
		if err != nil {
			return err
		}

		// A pointer distinguishes "not set" from an explicit empty value (which
		// matches a database with no migration recorded).
		var ifDeployedChecksum *string
		if cmd.Flags().Changed("if-deployed-checksum") {
			ifDeployedChecksum = &migrateIfDeployedChecksum
		}

		warnUnmatchedPatterns(schemaPath, exposure)
		return runMigrate(dsn, schemaPath, dryRun, force, databaseSchema, ifDeployedChecksum, exposure)
	},
}

func init() {
	f := migrateCmd.Flags()
	f.StringVar(&migrateDB, "db", "", "database URL")
	f.StringVar(&migrateDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&migrateSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.BoolVar(&migrateDryRun, "dry-run", false, "output migration SQL without applying")
	f.BoolVar(&migrateForce, "force", false, "re-apply even if unchanged, and drop removed functions with CASCADE, together with the views, policies and functions that depend on them")
	f.StringVar(&migrateIfDeployedChecksum, "if-deployed-checksum", "", "apply only if the deployed schema checksum matches this value (from `melange status --format json`); the exposure is not compared")
	addExposureFlags(f)
}

// resolveDSN gets the database DSN from flag or config.
func resolveDSN(flagDSN string) (string, error) {
	if flagDSN != "" {
		return flagDSN, nil
	}

	// A deferred environment-resolution failure (e.g. an unset ${VAR} secret)
	// becomes an error only now, when a command actually needs to connect. An
	// explicit --db above bypasses it.
	if envResolveErr != nil {
		return "", cli.ConfigError("resolving environment", envResolveErr)
	}

	dsn, err := cfg.DSN()
	if err != nil {
		return "", cli.ConfigError("database configuration", err)
	}
	if dsn == "" {
		return "", cli.ConfigError("database URL is required (use --db or set in config)", nil)
	}
	return dsn, nil
}

func runMigrate(dsn, schemaPath string, dryRun, force bool, databaseSchema string, ifDeployedChecksum *string, exposure migrator.Exposure) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	opts := migrator.MigrateOptions{
		Force:              force,
		Version:            version.Version,
		DatabaseSchema:     databaseSchema,
		IfDeployedChecksum: ifDeployedChecksum,
		Exposure:           &exposure,
	}

	if dryRun {
		opts.DryRun = os.Stdout
		if !quiet {
			fmt.Fprintln(os.Stderr, "-- Dry-run mode: SQL will be output but not applied")
			fmt.Fprintln(os.Stderr, "")
		}
	} else if !quiet {
		fmt.Println("Applying authz infrastructure...")
	}

	skipped, err := migrator.MigrateWithOptions(ctx, db, schemaPath, opts)
	if err != nil {
		return migrateExitError(err)
	}

	if dryRun {
		return nil
	}

	if !quiet {
		if skipped {
			fmt.Println("Schema unchanged, migration skipped.")
			fmt.Println("Use --force to re-apply.")
		} else {
			fmt.Println("Authz schema applied successfully.")
		}
	}

	// Check for melange_tuples warning
	m := migrator.NewMigrator(db, schemaPath)
	m.SetDatabaseSchema(databaseSchema)

	status, err := m.GetStatus(ctx)
	if err == nil && !status.TuplesExists && !quiet {
		fmt.Println()
		fmt.Println("WARNING: melange_tuples view/table does not exist.")
		fmt.Println("         Permission checks will fail until you create it.")
	}

	return nil
}

// migrateExitError maps a migration error to the CLI's exit code, keeping the
// typed migrator errors in the chain for errors.As.
func migrateExitError(err error) error {
	// Drift guard failed: the database is not at the expected checksum.
	var driftErr *migrator.DeployedModelChangedError
	if errors.As(err, &driftErr) {
		return cli.GeneralError("migration aborted", driftErr)
	}
	var aheadErr *migrator.DatabaseAheadError
	if errors.As(err, &aheadErr) {
		return &cli.ExitError{Code: cli.ExitDatabaseAhead, Message: "migration refused", Err: aheadErr}
	}
	// The error lists the dependents; say how to drop them anyway.
	var depErr *migrator.DependentObjectsError
	if errors.As(err, &depErr) {
		return cli.GeneralError("migration refused (rerun with --force to drop the dependents)", depErr)
	}
	if strings.Contains(err.Error(), "parsing schema") {
		return cli.SchemaParseError("schema error", err)
	}
	return cli.GeneralError("migration failed", err)
}
