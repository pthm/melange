package command

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
)

var envCmd = &cobra.Command{
	Use:   "env",
	Short: "Environment profile utilities",
	Long: `Inspect the named environment profiles defined in configuration.

Environments are connection profiles selected with the global --env flag
(or the MELANGE_ENV variable, or the default_environment config key). Each
overlays the base database configuration, so a command like

  melange status --env production

targets that environment's database.`,
}

var envListCmd = &cobra.Command{
	Use:   "list",
	Short: "List defined environment profiles",
	Long:  `List the environment profiles defined in configuration and their connection targets.`,
	Example: `  # List environments
  melange env list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEnvList()
	},
}

func init() {
	envCmd.AddCommand(envListCmd)
}

func runEnvList() error {
	// baseCfg holds the configuration before any --env overlay, so each profile
	// is summarized against the same base.
	names := make([]string, 0, len(baseCfg.Environments))
	for name := range baseCfg.Environments {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		fmt.Println("No environments defined.")
		fmt.Println("\nAdd them under 'environments:' in your config, e.g.:")
		fmt.Println("  environments:")
		fmt.Println("    production:")
		fmt.Println("      database:")
		fmt.Println("        url: ${PROD_DATABASE_URL}")
		return nil
	}

	fmt.Println("Environments:")
	for _, name := range names {
		summary, err := baseCfg.EnvironmentSummary(name)
		if err != nil {
			return cli.ConfigError("summarizing environment", err)
		}
		marker := "  "
		if name == activeEnv {
			marker = "* "
		}
		fmt.Printf("%s%-16s %s\n", marker, name, summary)
	}

	if baseCfg.DefaultEnvironment != "" {
		fmt.Printf("\nDefault: %s\n", baseCfg.DefaultEnvironment)
	}
	if activeEnv != "" {
		fmt.Printf("Active:  %s (marked with *)\n", activeEnv)
	}
	return nil
}
