package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/clientgen"
)

var (
	genClientRuntime  string
	genClientSchema   string
	genClientOutput   string
	genClientPackage  string
	genClientFilter   string
	genClientIDType   string
	genClientCheck    bool
	genClientDB       string
	genClientDBSchema string
	genClientFormat   string
	genClientStrict   bool
)

var generateClientCmd = &cobra.Command{
	Use:   "client",
	Short: "Generate type-safe client code",
	Long: `Generate type-safe client code from an authorization schema.

The client follows the SQL API exposure in melange.yaml: relation constants
for the relations sql.relations exposes, or for generate.client.relations
when set, and the exact names of the accessible functions migrate installs
for them (accessible_<type>_<relation>, and accessible_<type>_<relation>_by_<link>
when sql.functions.parent_key is on), truncated names included. The function
families come from sql.functions only: migrate's --no-<family> flags are not
seen here, so keep that choice in melange.yaml.

--check writes nothing. It regenerates the client in memory and compares it
with the files in the output directory, ignoring the header's melange
version and source lines, line endings and trailing whitespace at the end;
any other difference, reformatting included, is drift. With --db, --env or
MELANGE_ENV it also checks every name the client uses against the database's
last migration: accessible functions that are not installed or only
internal, and relations it does not expose. Run it after migrate; before,
use melange diff and status. It exits 1 on drift or an error, and on a
warning with --strict. Doctor runs the same check from melange.yaml alone,
so keep the relation selection there rather than in flags.

Supported runtimes: ` + strings.Join(clientgen.ListRuntimes(), ", "),
	Example: `  # Generate Go code to a directory
  melange generate client --runtime go --schema schemas/schema.fga --output internal/authz/

  # Generate with custom package name
  melange generate client --runtime go --schema schemas/schema.fga --output . --package myauthz

  # Generate only permission relations (can_*)
  melange generate client --runtime go --schema schemas/schema.fga --output . --include-relation 'can_*'

  # Output to stdout
  melange generate client --runtime go --schema schemas/schema.fga

  # CI: fail when the committed client is stale
  melange generate client --check

  # After migrate: check the client's names against production
  melange generate client --check --env production`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if genClientFormat != "text" && genClientFormat != "json" {
			return cli.ConfigError(fmt.Sprintf("invalid --format %q (want text or json)", genClientFormat), nil)
		}
		if !genClientCheck {
			for _, name := range []string{"db", "db-schema", "format", "strict"} {
				if cmd.Flags().Changed(name) {
					return cli.ConfigError("--"+name+" applies to --check", nil)
				}
			}
		}
		p, err := resolveClientPlan(cmd, true)
		if err != nil {
			return err
		}
		if genClientCheck {
			dbSchema := resolveString(genClientDBSchema, cfg.Database.Schema, "public")
			return runClientCheck(p, genClientDB, dbSchema, genClientFormat, genClientStrict)
		}
		warnClientFlags(cmd)

		files, err := clientgen.Generate(p.runtime, p.types, p.genCfg)
		if err != nil {
			return cli.GeneralError("generation failed", err)
		}

		// Output
		if p.output == "" {
			if len(files) > 1 {
				return cli.ConfigError("--output is required for multi-file generation", nil)
			}
			for _, content := range files {
				if _, err := os.Stdout.Write(content); err != nil {
					return cli.GeneralError("writing to stdout", err)
				}
			}
		} else {
			if err := os.MkdirAll(p.output, 0o755); err != nil {
				return cli.GeneralError("creating output directory", err)
			}
			for filename, content := range files {
				outPath := filepath.Join(p.output, filename)
				if err := os.WriteFile(outPath, content, 0o644); err != nil {
					return cli.GeneralError(fmt.Sprintf("writing %s", outPath), err)
				}
				if !quiet {
					fmt.Printf("Generated %s\n", outPath)
				}
			}
		}

		return nil
	},
}

// warnClientFlags says, when the client is configured, that relation
// selection flags are seen by this run only: doctor and --check without
// them read melange.yaml.
func warnClientFlags(cmd *cobra.Command) {
	if quiet || cfg.Generate.Client.Runtime == "" {
		return
	}
	for _, name := range []string{"include-relation", "exclude-relation", "filter"} {
		if cmd.Flags().Changed(name) {
			fmt.Fprintln(os.Stderr, "hint: doctor and --check read generate.client.relations from melange.yaml; put these patterns there")
			return
		}
	}
}

func init() {
	f := generateClientCmd.Flags()
	f.StringVar(&genClientRuntime, "runtime", "", "target runtime: "+strings.Join(clientgen.ListRuntimes(), ", "))
	f.StringVar(&genClientSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.StringVar(&genClientOutput, "output", "", "output directory (default: stdout, for a single-file runtime)")
	f.StringVar(&genClientPackage, "package", "", "package/module name (default: authz)")
	f.StringSlice("include-relation", nil, "generate constants only for relations matching this pattern, relation or type#relation (repeatable; replaces generate.client.relations.include, and sql.relations is no longer inherited)")
	f.StringSlice("exclude-relation", nil, "generate no constants for relations matching this pattern (repeatable; replaces generate.client.relations.exclude; with no client include it narrows the inherited sql.relations)")
	f.StringVar(&genClientFilter, "filter", "", "relation name prefix applied after the relation patterns (deprecated: use --include-relation 'can_*')")
	f.StringVar(&genClientIDType, "id-type", "", "ID type for constructors (default: string)")
	f.BoolVar(&genClientCheck, "check", false, "write nothing: compare the output directory with a fresh generation, and with --db or --env the client's names with the database")
	f.StringVar(&genClientDB, "db", "", "database URL to check the client's names against (with --check)")
	f.StringVar(&genClientDBSchema, "db-schema", "", "database schema (with --check; default: config database.schema, else public)")
	f.StringVar(&genClientFormat, "format", "text", "output format of --check: text (default) or json")
	f.BoolVar(&genClientStrict, "strict", false, "with --check, exit 1 on warnings too")
}
