package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/clientgen"
	"github.com/pthm/melange/pkg/parser"
)

var (
	genClientRuntime string
	genClientSchema  string
	genClientOutput  string
	genClientPackage string
	genClientFilter  string
	genClientIDType  string
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

Supported runtimes: ` + strings.Join(clientgen.ListRuntimes(), ", "),
	Example: `  # Generate Go code to a directory
  melange generate client --runtime go --schema schemas/schema.fga --output internal/authz/

  # Generate with custom package name
  melange generate client --runtime go --schema schemas/schema.fga --output . --package myauthz

  # Generate only permission relations (can_*)
  melange generate client --runtime go --schema schemas/schema.fga --output . --include-relation 'can_*'

  # Output to stdout
  melange generate client --runtime go --schema schemas/schema.fga`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve values: flags > config > defaults
		runtime := resolveString(genClientRuntime, cfg.Generate.Client.Runtime)
		schema := resolveString(genClientSchema, cfg.Schema)
		output := resolveString(genClientOutput, cfg.Generate.Client.Output)
		pkg := resolveString(genClientPackage, cfg.Generate.Client.Package, "authz")
		filter := resolveString(genClientFilter, cfg.Generate.Client.Filter)
		idType := resolveString(genClientIDType, cfg.Generate.Client.IDType, "string")
		// Relation patterns: flags > env > file > inherit sql.relations.
		client := cfg.Generate.Client
		if err := applyRelationFlags(cmd.Flags(), &client.Relations); err != nil {
			return err
		}
		exposure := cfg.SQL.Exposure()
		selector := client.RelationPatterns(cfg.SQL.Relations)

		// Validate required fields
		if runtime == "" {
			return cli.ConfigError("--runtime is required", nil)
		}
		if schema == "" {
			return cli.ConfigError("--schema is required", nil)
		}

		// Validate runtime
		if !clientgen.Registered(runtime) {
			return cli.ConfigError(
				fmt.Sprintf("unknown runtime %q", runtime),
				fmt.Errorf("supported runtimes: %s", strings.Join(clientgen.ListRuntimes(), ", ")),
			)
		}

		// Parse schema
		if _, err := os.Stat(schema); err != nil {
			return cli.SchemaParseError(fmt.Sprintf("schema not found: %s", schema), nil)
		}

		types, err := parser.ParseSchema(schema)
		if err != nil {
			return cli.SchemaParseError("parsing schema", err)
		}

		// Generate code
		genCfg := &clientgen.Config{
			Package:        pkg,
			Exposure:       &exposure,
			Relations:      &selector,
			RelationFilter: filter,
			IDType:         idType,
			Version:        version.Version,
			SourcePath:     schema,
		}
		files, err := clientgen.Generate(runtime, types, genCfg)
		if err != nil {
			return cli.GeneralError("generation failed", err)
		}

		// Output
		if output == "" {
			if len(files) > 1 {
				return cli.ConfigError("--output is required for multi-file generation", nil)
			}
			for _, content := range files {
				if _, err := os.Stdout.Write(content); err != nil {
					return cli.GeneralError("writing to stdout", err)
				}
			}
		} else {
			if err := os.MkdirAll(output, 0o755); err != nil {
				return cli.GeneralError("creating output directory", err)
			}
			for filename, content := range files {
				outPath := filepath.Join(output, filename)
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

func init() {
	f := generateClientCmd.Flags()
	f.StringVar(&genClientRuntime, "runtime", "", "target runtime: "+strings.Join(clientgen.ListRuntimes(), ", "))
	f.StringVar(&genClientSchema, "schema", "", "path to schema.fga or fga.mod file")
	f.StringVar(&genClientOutput, "output", "", "output directory or file path (default: stdout)")
	f.StringVar(&genClientPackage, "package", "", "package/module name (default: authz)")
	f.StringSlice("include-relation", nil, "generate constants only for relations matching this pattern, relation or type#relation (repeatable; replaces generate.client.relations.include, and sql.relations is no longer inherited)")
	f.StringSlice("exclude-relation", nil, "generate no constants for relations matching this pattern (repeatable; replaces generate.client.relations.exclude; with no client include it narrows the inherited sql.relations)")
	f.StringVar(&genClientFilter, "filter", "", "relation name prefix applied after the relation patterns (deprecated: use --include-relation 'can_*')")
	f.StringVar(&genClientIDType, "id-type", "", "ID type for constructors (default: string)")
}
