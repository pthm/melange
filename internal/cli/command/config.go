package command

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var (
	configShowSource bool
	configShowReveal bool
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration utilities",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show effective configuration",
	Long: `Show the effective configuration after merging defaults, config file,
environment variables, and any selected environment profile.

Passwords (including those resolved from ${VAR} references) are masked by
default; pass --reveal-secrets to print them in cleartext.`,
	Example: `  # Show effective configuration
  melange config show

  # Show configuration with source file path
  melange config show --source

  # Show the resolved production profile, secrets unmasked
  melange config show --env production --reveal-secrets`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if configShowSource {
			if configPath != "" {
				fmt.Printf("Config file: %s\n", configPath)
			} else {
				fmt.Println("Config file: (none, using defaults)")
			}
			if activeEnv != "" {
				fmt.Printf("Environment: %s\n", activeEnv)
			} else {
				fmt.Println("Environment: (base, none selected)")
			}
			fmt.Println()
		}

		toShow := cfg
		if !configShowReveal {
			toShow = cfg.Redacted()
		}
		out, err := yaml.Marshal(toShow)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	},
}

func init() {
	configShowCmd.Flags().BoolVar(&configShowSource, "source", false, "show config file source")
	configShowCmd.Flags().BoolVar(&configShowReveal, "reveal-secrets", false, "print passwords in cleartext instead of masking them")
	configCmd.AddCommand(configShowCmd)
}
