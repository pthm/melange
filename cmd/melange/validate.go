package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/parser"
)

var validateSchema string

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate schema syntax",
	Long:  `Validate schema syntax using the OpenFGA parser.`,
	Example: `  # Validate a specific schema file
  melange validate --schema schemas/schema.fga

  # Validate using config file settings
  melange validate`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Resolve schema path: flag > config > default
		schemaPath := resolveString(validateSchema, cfg.Schema)

		if _, err := os.Stat(schemaPath); err != nil {
			return cli.SchemaParseError(fmt.Sprintf("schema not found: %s", schemaPath), nil)
		}

		types, err := parser.ParseSchema(schemaPath)
		if err != nil {
			return cli.SchemaParseError("parsing schema", err)
		}

		if !quiet {
			fmt.Printf("Schema is valid. Found %d types:\n", len(types))
			for _, t := range types {
				fmt.Printf("  - %s (%d relations)\n", t.Name, len(t.Relations))
			}
			fmt.Println()
			fmt.Println("For full validation, install OpenFGA CLI:")
			fmt.Println("  go install github.com/openfga/cli/cmd/fga@latest")
			fmt.Printf("  fga model validate --file %s\n", schemaPath)
		}

		return nil
	},
}

func init() {
	validateCmd.Flags().StringVar(&validateSchema, "schema", "", "path to schema.fga file")
}
