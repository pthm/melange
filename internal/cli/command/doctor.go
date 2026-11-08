package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/pkg/compiler"
)

var (
	doctorDB              string
	doctorDBSchema        string
	doctorSchema          string
	doctorVerbose         bool
	doctorSkipPerformance bool
	doctorFormat          string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run health checks",
	Long: `Run health checks on authorization infrastructure.

The functions doctor expects follow the SQL API exposure the database was
migrated with. The local exposure (the sql section of the configuration and
the exposure flags) is compared with it, and patterns that match no relation
are reported.`,
	Example: `  # Run health checks
  melange doctor --db postgres://localhost/mydb

  # Use a different database schema
  melange doctor --db postgres://localhost/mydb --db-schema myschema

  # Run with verbose output
  melange doctor --db postgres://localhost/mydb --verbose

  # Machine-readable report, for scripts and coding agents
  melange doctor --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if doctorFormat != "text" && doctorFormat != "json" {
			return cli.GeneralError(fmt.Sprintf("invalid --format %q (want text or json)", doctorFormat), nil)
		}

		databaseSchema := resolveString(doctorDBSchema, cfg.Database.Schema, "public")
		schemaPath := resolveString(doctorSchema, cfg.Schema)
		verboseFlag := resolveBool(doctorVerbose, cfg.Doctor.Verbose)
		skipPerf := resolveBool(doctorSkipPerformance, cfg.Doctor.SkipPerformance)

		exposure, err := resolveExposure(cmd, cfg.SQL)
		if err != nil {
			return err
		}

		dsn, err := resolveDSN(doctorDB)
		if err != nil {
			return err
		}

		return runDoctor(dsn, databaseSchema, schemaPath, verboseFlag, skipPerf, doctorFormat, exposure)
	},
}

func init() {
	f := doctorCmd.Flags()
	f.StringVar(&doctorDB, "db", "", "database URL")
	f.StringVar(&doctorDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&doctorSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.BoolVar(&doctorVerbose, "verbose", false, "show detailed output")
	f.BoolVar(&doctorSkipPerformance, "skip-performance", false, "skip performance checks")
	f.StringVar(&doctorFormat, "format", "text", "output format: text (default) or json")
	addExposureFlags(f)
}

func runDoctor(dsn, databaseSchema, schemaPath string, verboseFlag, skipPerformance bool, format string, exposure compiler.Exposure) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	if !quiet && format == "text" {
		fmt.Println("melange doctor - Health Check")
	}

	opts := doctor.Options{SkipPerformance: skipPerformance, Exposure: &exposure}
	if info := awaitUpdateInfo(advisoryWait); info != nil {
		opts.Advisories = info.Advisories
	}
	d := doctor.New(db, schemaPath, opts)
	d.SetDatabaseSchema(databaseSchema)
	report, err := d.Run(ctx)
	if err != nil {
		return cli.GeneralError("running doctor", err)
	}

	if format == "json" {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return cli.GeneralError("encoding report", err)
		}
		fmt.Println(string(out))
	} else {
		report.Print(os.Stdout, verboseFlag)
	}

	if report.HasErrors() {
		return cli.GeneralError("health checks failed", nil)
	}

	return nil
}
