package command

import "github.com/spf13/cobra"

var schemaCmd = &cobra.Command{
	Use:   "schema",
	Short: "Inspect the model deployed in a database",
	Long: `Work with the authorization model recorded in a migrated database.

Since v0.9 every migration stores the schema it applied, so a live database
can describe itself. Use 'schema pull' to reconstruct the .fga from it.`,
}

func init() {
	schemaCmd.AddCommand(schemaPullCmd)
}
