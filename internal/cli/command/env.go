package command

import (
	"fmt"
	"io"
	"os"
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
	return printEnvironments(os.Stdout, baseCfg, activeEnv)
}

// printEnvironments writes one line per configured profile, marking the active
// one. cfg is the configuration before any --env overlay, so every profile is
// summarized against the same base.
func printEnvironments(w io.Writer, cfg *cli.Config, active string) error {
	names := make([]string, 0, len(cfg.Environments))
	for name := range cfg.Environments {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		_, _ = fmt.Fprintln(w, "No environments defined.")
		_, _ = fmt.Fprintln(w, "\nAdd them under 'environments:' in your config, e.g.:")
		_, _ = fmt.Fprintln(w, "  environments:")
		_, _ = fmt.Fprintln(w, "    production:")
		_, _ = fmt.Fprintln(w, "      database:")
		_, _ = fmt.Fprintln(w, "        url: ${PROD_DATABASE_URL}")
		return nil
	}

	_, _ = fmt.Fprintln(w, "Environments:")
	for _, name := range names {
		summary, err := cfg.EnvironmentSummary(name)
		if err != nil {
			return cli.ConfigError("summarizing environment", err)
		}
		marker := "  "
		if name == active {
			marker = "* "
		}
		_, _ = fmt.Fprintf(w, "%s%-16s %s\n", marker, name, summary)
	}

	if cfg.DefaultEnvironment != "" {
		_, _ = fmt.Fprintf(w, "\nDefault: %s\n", cfg.DefaultEnvironment)
	}
	if active != "" {
		_, _ = fmt.Fprintf(w, "Active:  %s (marked with *)\n", active)
	}
	return nil
}
