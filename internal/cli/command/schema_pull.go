package command

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
)

var (
	schemaPullDB       string
	schemaPullDBSchema string
	schemaPullOutput   string
	schemaPullNoHeader bool
)

var schemaPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Reconstruct the .fga schema from a migrated database",
	Long: `Pull writes the OpenFGA DSL recorded by the most recent migration to a file
or stdout. Use it to recover a schema whose source file was lost, or to see
exactly what model a database is running.

For a single-file schema the output is valid DSL — it carries a provenance
header as '#' comments and re-parses as-is; pass --no-header for the bare
schema. A modular (fga.mod) schema is emitted as the stored manifest + module
bundle for reference/recovery; that combined form does not re-parse as a single
.fga, and splitting it back into module files is not supported.

Requires a database migrated by a melange version that records the deployed
model; older databases did not record it, and pull reports that they cannot be
recovered.`,
	Example: `  # Print the deployed schema
  melange schema pull --db postgres://localhost/mydb

  # Recover it to a file, targeting a named environment
  melange schema pull --env production -o recovered.fga`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseSchema := resolveString(schemaPullDBSchema, cfg.Database.Schema, "public")
		dsn, err := resolveDSN(schemaPullDB)
		if err != nil {
			return err
		}
		return runSchemaPull(dsn, databaseSchema, schemaPullOutput, schemaPullNoHeader)
	},
}

func init() {
	f := schemaPullCmd.Flags()
	f.StringVar(&schemaPullDB, "db", "", "database URL")
	f.StringVar(&schemaPullDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVarP(&schemaPullOutput, "output", "o", "", "write to this file (default: stdout)")
	f.BoolVar(&schemaPullNoHeader, "no-header", false, "omit the provenance header comment")
}

func runSchemaPull(dsn, databaseSchema, output string, noHeader bool) error {
	model, err := readDeployedModel(dsn, databaseSchema)
	if err != nil {
		return err
	}

	content := pullContent(model, noHeader)

	if output == "" || output == "-" {
		fmt.Print(content)
		return nil
	}
	if err := os.WriteFile(output, []byte(content), 0o644); err != nil { //nolint:gosec // schema is not sensitive
		return cli.GeneralError("writing schema file", err)
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "Wrote deployed schema to %s\n", output)
	}
	return nil
}

// pullContent assembles what `schema pull` emits: the recorded DSL, prefixed
// with the provenance header unless it was suppressed.
func pullContent(model *migrator.DeployedModel, noHeader bool) string {
	if noHeader {
		return model.DSL
	}
	return pullHeader(model) + model.DSL
}

// pullHeader renders the provenance of a pulled schema as '#' comment lines.
// It never includes the database URL, which may contain credentials.
func pullHeader(model *migrator.DeployedModel) string {
	var b strings.Builder
	b.WriteString("# Pulled from a melange-migrated database by `melange schema pull`\n")
	if !model.MigratedAt.IsZero() {
		fmt.Fprintf(&b, "# Deployed: %s", model.MigratedAt.Format(time.RFC3339))
		if model.MelangeVersion != "" {
			fmt.Fprintf(&b, " by melange %s", model.MelangeVersion)
		}
		b.WriteString("\n")
	}
	if model.SchemaChecksum != "" {
		fmt.Fprintf(&b, "# Schema checksum: %s\n", model.SchemaChecksum)
	}
	if model.Format == migrator.FormatModular {
		b.WriteString("# Original format: modular (fga.mod). Below is the stored manifest + module\n")
		b.WriteString("# bundle (--- separated) — NOT a single parseable .fga; for reference/recovery.\n")
	}
	b.WriteString("\n")
	return b.String()
}
