package command

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var (
	validateSchema string
	validateFormat string
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate a schema",
	Long: `Validate a schema without database access: syntax, references to undefined
types and relations, unsupported features, and relation cycles.`,
	Example: `  # Validate a single-file schema
  melange validate --schema schemas/schema.fga

  # Validate a modular schema (fga.mod manifest)
  melange validate --schema schemas/fga.mod

  # Validate using config file settings
  melange validate

  # Machine-readable result, for scripts and coding agents
  melange validate --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if validateFormat != "text" && validateFormat != "json" {
			return cli.GeneralError(fmt.Sprintf("invalid --format %q (want text or json)", validateFormat), nil)
		}

		// Resolve schema path: flag > config > default
		schemaPath := resolveString(validateSchema, cfg.Schema)

		types, verr := validateSchemaFile(schemaPath)

		if validateFormat == "json" {
			if err := printValidateJSON(schemaPath, types, verr); err != nil {
				return err
			}
			return verr
		}
		if verr != nil {
			return verr
		}

		if !quiet {
			fmt.Printf("Schema is valid. Found %d types:\n", len(types))
			for _, t := range types {
				fmt.Printf("  - %s (%d relations)\n", t.Name, len(t.Relations))
			}
		}

		return nil
	},
}

// validateSchemaFile runs every check that needs no database, the same ones
// migrate and generate apply before compiling.
func validateSchemaFile(schemaPath string) ([]schema.TypeDefinition, error) {
	if _, err := os.Stat(schemaPath); err != nil {
		return nil, cli.SchemaParseError(fmt.Sprintf("schema not found: %s", schemaPath), nil)
	}

	types, err := parser.ParseSchema(schemaPath)
	if err != nil {
		return nil, cli.SchemaParseError("parsing schema", err)
	}

	if err := schema.DetectCycles(types); err != nil {
		return nil, cli.SchemaParseError("invalid schema", err)
	}

	return types, nil
}

// validateReport is the --format json output. Types is always present (empty
// when invalid) so consumers can read it without a presence check.
type validateReport struct {
	Valid  bool               `json:"valid"`
	Schema string             `json:"schema"`
	Types  []validateTypeInfo `json:"types"`
	Error  string             `json:"error,omitempty"`
}

type validateTypeInfo struct {
	Name      string   `json:"name"`
	Relations []string `json:"relations"`
}

func printValidateJSON(schemaPath string, types []schema.TypeDefinition, verr error) error {
	report := validateReport{Valid: verr == nil, Schema: schemaPath, Types: []validateTypeInfo{}}
	if verr != nil {
		report.Error = verr.Error()
	}
	for _, t := range types {
		info := validateTypeInfo{Name: t.Name, Relations: make([]string, 0, len(t.Relations))}
		for _, r := range t.Relations {
			info.Relations = append(info.Relations, r.Name)
		}
		report.Types = append(report.Types, info)
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return cli.GeneralError("encoding validation result", err)
	}
	fmt.Println(string(out))
	return nil
}

func init() {
	validateCmd.Flags().StringVar(&validateSchema, "schema", "", "path to schema.fga or fga.mod file")
	validateCmd.Flags().StringVar(&validateFormat, "format", "text", "output format: text (default) or json")
}
