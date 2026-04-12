package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/lib/cli"
	"github.com/pthm/melange/lib/version"
	"github.com/pthm/melange/pkg/migrator"
)

var (
	migrateDB       string
	migrateDBSchema string
	migrateSchema   string
	migrateDryRun   bool
	migrateForce    bool
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Apply schema to database",
	Long:  `Apply authorization schema to PostgreSQL database.`,
	Example: `  # Apply schema to database
  melange migrate --db postgres://localhost/mydb

  # Use a different database schema
  melange migrate --db postgres://localhost/mydb --db-schema myschema

  # Preview migration without applying
  melange migrate --db postgres://localhost/mydb --dry-run

  # Force re-apply even if schema unchanged
  melange migrate --db postgres://localhost/mydb --force`,
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
		databaseSchema := resolveString(migrateDBSchema, cfg.Database.Schema)
		schemaPath := resolveString(migrateSchema, cfg.Schema)
		dryRun := resolveBool(migrateDryRun, cfg.Migrate.DryRun)
		force := resolveBool(migrateForce, cfg.Migrate.Force)

		// Get DSN
		dsn, err := resolveDSN(migrateDB)
		if err != nil {
			return err
		}

		return runMigrate(dsn, schemaPath, dryRun, force, databaseSchema)
	},
}

func init() {
	f := migrateCmd.Flags()
	f.StringVar(&migrateDB, "db", "", "database URL")
	f.StringVar(&migrateDBSchema, "db-schema", "public", "database schema")
	f.StringVar(&migrateSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.BoolVar(&migrateDryRun, "dry-run", false, "output migration SQL without applying")
	f.BoolVar(&migrateForce, "force", false, "force migration even if schema unchanged")
}

// resolveDSN gets the database DSN from flag or config.
func resolveDSN(flagDSN string) (string, error) {
	if flagDSN != "" {
		return flagDSN, nil
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

func runMigrate(dsn, schemaPath string, dryRun, force bool, databaseSchema string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	opts := migrator.MigrateOptions{
		Force:          force,
		Version:        version.Version,
		DatabaseSchema: databaseSchema,
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
		// Classify error
		errStr := err.Error()
		if strings.Contains(errStr, "parsing schema") {
			return cli.SchemaParseError("schema error", err)
		}
		return cli.GeneralError("migration failed", err)
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
