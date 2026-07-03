package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
)

var (
	historyDB       string
	historyDBSchema string
	historyFormat   string
	historyLimit    int
)

var historyCmd = &cobra.Command{
	Use:   "history",
	Short: "Show the migration history recorded in the database",
	Long: `List recent entries from the melange_migrations table — when each migration
ran, the melange version, the schema checksum, and how many functions it
installed. An audit trail of how a database's authorization model has evolved.`,
	Example: `  # Recent migrations
  melange history --db postgres://localhost/mydb

  # The last 5, as JSON, against a named environment
  melange history --env production --limit 5 --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseSchema := resolveString(historyDBSchema, cfg.Database.Schema, "public")
		dsn, err := resolveDSN(historyDB)
		if err != nil {
			return err
		}
		return runHistory(dsn, databaseSchema, historyFormat, historyLimit)
	},
}

func init() {
	f := historyCmd.Flags()
	f.StringVar(&historyDB, "db", "", "database URL")
	f.StringVar(&historyDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&historyFormat, "format", "text", "output format: text (default) or json")
	f.IntVar(&historyLimit, "limit", 20, "maximum number of entries to show")
}

// historyEntry is the JSON/text shape of one migration record. Fields are always
// present (empty string when a legacy record lacks them) so the JSON schema is
// stable and matches what the text renderer shows.
type historyEntry struct {
	ID             int    `json:"id"`
	MigratedAt     string `json:"migrated_at"`
	MelangeVersion string `json:"melange_version"`
	Checksum       string `json:"schema_checksum"`
	Format         string `json:"schema_format"`
	FunctionCount  int    `json:"function_count"`
}

func runHistory(dsn, databaseSchema, format string, limit int) error {
	if format != "text" && format != "json" {
		return cli.ConfigError(fmt.Sprintf("invalid --format %q (want text or json)", format), nil)
	}
	if limit < 1 {
		return cli.ConfigError("--limit must be at least 1", nil)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)

	records, err := m.GetMigrationHistory(ctx, limit)
	if err != nil {
		return cli.GeneralError("reading migration history", err)
	}

	entries := make([]historyEntry, 0, len(records))
	for _, r := range records {
		e := historyEntry{
			ID:             r.ID,
			MelangeVersion: r.MelangeVersion,
			Checksum:       r.SchemaChecksum,
			Format:         r.SchemaFormat,
			FunctionCount:  len(r.FunctionNames),
		}
		if !r.MigratedAt.IsZero() {
			e.MigratedAt = r.MigratedAt.Format(time.RFC3339)
		}
		entries = append(entries, e)
	}

	if format == "json" {
		out, merr := json.MarshalIndent(entries, "", "  ")
		if merr != nil {
			return cli.GeneralError("encoding history", merr)
		}
		fmt.Println(string(out))
		return nil
	}

	if len(entries) == 0 {
		fmt.Println("No migrations recorded in this database.")
		return nil
	}
	fmt.Println("Migration history (most recent first):")
	for _, e := range entries {
		when := e.MigratedAt
		if when == "" {
			when = "(unknown time)"
		}
		version := e.MelangeVersion
		if version == "" {
			version = "unknown"
		}
		line := fmt.Sprintf("  %s · melange %s · checksum %s", when, version, shortChecksum(e.Checksum))
		if e.Format != "" {
			line += " · " + e.Format
		}
		line += fmt.Sprintf(" · %d functions", e.FunctionCount)
		fmt.Println(line)
	}
	return nil
}
