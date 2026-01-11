package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
)

var (
	statusDB         string
	statusSchemasDir string
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current schema status",
	Long:  `Show current schema and migration status.`,
	Example: `  # Check status
  melange status --db postgres://localhost/mydb`,
	RunE: func(cmd *cobra.Command, args []string) error {
		schemasDir := resolveString(statusSchemasDir, cfg.Status.SchemasDir, cfg.SchemasDir)

		dsn, err := resolveDSN(statusDB)
		if err != nil {
			return err
		}

		return runStatus(dsn, schemasDir)
	},
}

func init() {
	f := statusCmd.Flags()
	f.StringVar(&statusDB, "db", "", "database URL")
	f.StringVar(&statusSchemasDir, "schemas-dir", "", "directory containing schema.fga")
}

func runStatus(dsn, schemasDir string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, schemasDir)

	s, err := m.GetStatus(ctx)
	if err != nil {
		return cli.GeneralError("getting status", err)
	}

	if s.SchemaExists {
		fmt.Println("Schema file:  present")
	} else {
		fmt.Println("Schema file:  missing")
	}
	if s.TuplesExists {
		fmt.Println("Tuples view:  present")
	} else {
		fmt.Println("Tuples view:  missing")
	}

	if !s.SchemaExists {
		fmt.Println("\nNo schema found. Create schemas/schema.fga to start.")
	} else if !s.TuplesExists {
		fmt.Println("\nTuples view not found.")
		fmt.Println("Create melange_tuples before running checks.")
	}

	return nil
}
