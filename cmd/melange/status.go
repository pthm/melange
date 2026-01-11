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
	statusDB     string
	statusSchema string
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current schema status",
	Long:  `Show current schema and migration status.`,
	Example: `  # Check status
  melange status --db postgres://localhost/mydb`,
	RunE: func(cmd *cobra.Command, args []string) error {
		schemaPath := resolveString(statusSchema, cfg.Schema)

		dsn, err := resolveDSN(statusDB)
		if err != nil {
			return err
		}

		return runStatus(dsn, schemaPath)
	},
}

func init() {
	f := statusCmd.Flags()
	f.StringVar(&statusDB, "db", "", "database URL")
	f.StringVar(&statusSchema, "schema", "", "path to schema.fga file")
}

func runStatus(dsn, schemaPath string) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, schemaPath)

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
		fmt.Printf("\nNo schema found at %s\n", schemaPath)
	} else if !s.TuplesExists {
		fmt.Println("\nTuples view not found.")
		fmt.Println("Create melange_tuples before running checks.")
	}

	return nil
}
