package command

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var (
	diffSchema         string
	diffDB             string
	diffDBSchema       string
	diffGitRef         string
	diffPreviousSchema string
	diffFormat         string
	diffExitCode       bool
)

var diffCmd = &cobra.Command{
	Use:   "diff",
	Short: "Show what changed between a deployed/previous model and your local schema",
	Long: `Diff compares your local schema (the "new" side) against a previous model
(the "old" side) and classifies each change as additive (safe — widens access
or adds structure) or breaking (narrows access or removes structure).

The comparison source is one of:
  - (default) the model deployed in a database (--db / --env, or config)
  - --git-ref <ref>: your schema as of a git commit, branch, or tag
  - --previous-schema <path>: a previous .fga file

Use --exit-code in CI to fail the build when a change is breaking.`,
	Example: `  # What would migrating apply to production?
  melange diff --env production

  # Compare against the schema on main
  melange diff --git-ref main

  # Compare two files
  melange diff --previous-schema old.fga

  # CI gate: non-zero exit on breaking changes
  melange diff --env production --exit-code`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		schemaPath := resolveString(diffSchema, cfg.Schema)
		databaseSchema := resolveString(diffDBSchema, cfg.Database.Schema, "public")

		explicitDBSource := diffDB != "" || resolveString(envFlag, os.Getenv("MELANGE_ENV")) != ""
		if err := validateDiffFlags(schemaPath, diffFormat, diffGitRef, diffPreviousSchema, explicitDBSource); err != nil {
			return err
		}

		newTypes, err := parser.ParseSchema(schemaPath)
		if err != nil {
			return cli.SchemaParseError("parsing local schema", err)
		}

		oldTypes, source, err := diffPreviousModel(schemaPath, databaseSchema)
		if err != nil {
			return err
		}

		d := schema.Diff(oldTypes, newTypes)
		renderDiff(os.Stdout, d, source, schemaPath)

		if diffExitCode && d.HasBreaking() {
			// git-diff-style signal for CI gates; errors already exited above.
			os.Exit(1)
		}
		return nil
	},
}

func init() {
	f := diffCmd.Flags()
	f.StringVar(&diffSchema, "schema", "", "path to the local .fga or fga.mod file (the new side)")
	f.StringVar(&diffDB, "db", "", "database URL to compare against (the default source)")
	f.StringVar(&diffDBSchema, "db-schema", "", "database schema (default: config database.schema, else public)")
	f.StringVar(&diffGitRef, "git-ref", "", "compare against the schema at this git ref")
	f.StringVar(&diffPreviousSchema, "previous-schema", "", "compare against a previous .fga file (modular not supported)")
	f.StringVar(&diffFormat, "format", "tree", "output format: tree (default) or json")
	f.BoolVar(&diffExitCode, "exit-code", false, "exit 1 if any change is breaking")
}

// validateDiffFlags rejects flag combinations that have no single answer, before
// any database connection is attempted.
//
// explicitDBSource means the user actively chose a database (--db, or --env /
// MELANGE_ENV): only then does a database source conflict with --git-ref or
// --previous-schema. A passive default_environment does not conflict, so
// `melange diff --git-ref main` still works in a repo that has one configured.
func validateDiffFlags(schemaPath, format, gitRef, previousSchema string, explicitDBSource bool) error {
	if schemaPath == "" {
		return cli.ConfigError("no schema path — set --schema or `schema` in config", nil)
	}
	if format != "tree" && format != "json" {
		return cli.ConfigError(fmt.Sprintf("invalid --format %q (want tree or json)", format), nil)
	}
	if boolCount(gitRef != "", previousSchema != "") > 1 {
		return cli.ConfigError("--git-ref and --previous-schema are mutually exclusive", nil)
	}
	if explicitDBSource && (gitRef != "" || previousSchema != "") {
		return cli.ConfigError("a database source (--db/--env) cannot be combined with --git-ref or --previous-schema", nil)
	}
	return nil
}

// diffPreviousModel resolves the "old" side of the diff and a label for it.
func diffPreviousModel(schemaPath, databaseSchema string) ([]schema.TypeDefinition, string, error) {
	switch {
	case diffGitRef != "":
		relPath, err := gitRelativePath(schemaPath)
		if err != nil {
			return nil, "", err
		}
		types, err := parsePreviousSchema(diffGitRef, relPath, true)
		return types, "git:" + diffGitRef, err
	case diffPreviousSchema != "":
		if parser.IsModularSchema(diffPreviousSchema) {
			return nil, "", cli.ConfigError("--previous-schema does not support modular schemas (fga.mod); use --db or --git-ref instead", nil)
		}
		types, err := parsePreviousSchema(diffPreviousSchema, "", false)
		return types, "file:" + diffPreviousSchema, err
	default:
		dsn, err := resolveDSN(diffDB)
		if err != nil {
			return nil, "", err
		}
		types, err := deployedModelTypes(dsn, databaseSchema)
		return types, "deployed", err
	}
}

// deployedModelTypes reads the parsed model recorded in a database. If only the
// DSL was recorded (model_json absent), it parses the DSL so the deployed side
// is never silently empty — which would misreport every local type as additive.
func deployedModelTypes(dsn, databaseSchema string) ([]schema.TypeDefinition, error) {
	model, err := readDeployedModel(dsn, databaseSchema)
	if err != nil {
		return nil, err
	}
	if len(model.Types) == 0 && model.DSL != "" {
		types, perr := parser.ParseSchemaString(model.DSL)
		if perr != nil {
			return nil, cli.SchemaParseError("parsing deployed schema", perr)
		}
		return types, nil
	}
	return model.Types, nil
}

func renderDiff(w io.Writer, d schema.SchemaDiff, source, schemaPath string) {
	if diffFormat == "json" {
		out, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			// SchemaDiff is plain data; marshaling cannot fail.
			out = []byte("{}")
		}
		_, _ = fmt.Fprintln(w, string(out))
		return
	}

	_, _ = fmt.Fprintf(w, "Comparing %s → %s\n\n", source, schemaPath)
	if d.Empty() {
		_, _ = fmt.Fprintln(w, "No changes — schemas are equivalent.")
		return
	}
	for _, c := range d.Changes {
		label := "additive"
		if c.Class == schema.ClassBreaking {
			label = "BREAKING"
		}
		_, _ = fmt.Fprintf(w, "  %-9s %s\n", label, c.Summary)
	}
	additive, breaking := d.Counts()
	_, _ = fmt.Fprintf(w, "\n%d breaking, %d additive\n", breaking, additive)
}
