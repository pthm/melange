package command

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	doctorSkipClient      bool
	doctorStrictClient    bool
	doctorFormat          string
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run health checks",
	Long: `Run health checks on authorization infrastructure.

The functions doctor expects follow the SQL API exposure the database was
migrated with. The local exposure (the sql section of the configuration and
the exposure flags) is compared with it, and patterns that match no relation
are reported.

When generate.client.runtime is configured, the Generated Client checks
compare the client with what melange.yaml generates (when the output
directory exists here) and every name it uses with the database. They read
generate.client.* and the sql section from melange.yaml only; the exposure
flags above do not apply to them. A name the database does not answer for
is a warning, since the working tree may be ahead of the database;
--strict-client makes it a failure.`,
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
		var client *doctor.ClientCheck
		if !resolveBool(doctorSkipClient, cfg.Doctor.SkipClient) {
			client = doctorClientCheck(cmd, resolveBool(doctorStrictClient, cfg.Doctor.StrictClient))
		}

		exposure, err := resolveExposure(cmd, cfg.SQL)
		if err != nil {
			return err
		}

		dsn, err := resolveDSN(doctorDB)
		if err != nil {
			return err
		}

		return runDoctor(dsn, databaseSchema, schemaPath, verboseFlag, skipPerf, doctorFormat, exposure, client)
	},
}

func init() {
	f := doctorCmd.Flags()
	f.StringVar(&doctorDB, "db", "", "database URL")
	f.StringVar(&doctorDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&doctorSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.BoolVar(&doctorVerbose, "verbose", false, "show detailed output")
	f.BoolVar(&doctorSkipPerformance, "skip-performance", false, "skip performance checks")
	f.BoolVar(&doctorSkipClient, "skip-client", false, "skip the generated client checks")
	f.BoolVar(&doctorStrictClient, "strict-client", false, "fail, rather than warn, when the generated client names something the database does not answer for")
	f.StringVar(&doctorFormat, "format", "text", "output format: text (default) or json")
	addExposureFlags(f)
}

// doctorClientCheck resolves the configured client for doctor, from
// melange.yaml alone, or returns nil when none is configured or its schema
// does not parse (the Schema File checks report that).
func doctorClientCheck(cmd *cobra.Command, strict bool) *doctor.ClientCheck {
	if cfg.Generate.Client.Runtime == "" {
		return nil
	}
	p, err := resolveClientPlan(cmd, false)
	var exit *cli.ExitError
	if errors.As(err, &exit) && exit.Code == cli.ExitSchemaParse {
		return nil
	}
	cc := &doctor.ClientCheck{Runtime: cfg.Generate.Client.Runtime, Output: cfg.Generate.Client.Output, Strict: strict}
	if err != nil {
		cc.GenerateError = err.Error()
		return cc
	}
	cc.Local = p.local
	if cc.Names, err = p.names(); err != nil {
		cc.GenerateError = err.Error()
		return cc
	}
	switch info, statErr := os.Stat(p.output); {
	case p.output == "":
		cc.FilesSkipped = "generate.client.output is not set"
	case errors.Is(statErr, fs.ErrNotExist):
		cc.FilesSkipped = p.output + " does not exist here (no sources, as in a production image)"
	case statErr != nil:
		cc.FilesError = statErr.Error()
	case !info.IsDir():
		cc.FilesError = p.output + " is not a directory"
	default:
		if cc.Drift, err = p.checkFiles(); err != nil {
			cc.FilesError = err.Error()
		}
	}
	return cc
}

func runDoctor(dsn, databaseSchema, schemaPath string, verboseFlag, skipPerformance bool, format string, exposure compiler.Exposure, client *doctor.ClientCheck) error {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return cli.DBConnectError("connecting to database", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	if !quiet && format == "text" {
		fmt.Println("melange doctor - Health Check")
	}

	opts := doctor.Options{SkipPerformance: skipPerformance, Exposure: &exposure, Client: client}
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
