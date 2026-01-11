package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var configShowSource bool

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration utilities",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show effective configuration",
	Long:  `Show the effective configuration after merging defaults, config file, and environment variables.`,
	Example: `  # Show effective configuration
  melange config show

  # Show configuration with source file path
  melange config show --source`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if configShowSource {
			if configPath != "" {
				fmt.Printf("Config file: %s\n\n", configPath)
			} else {
				fmt.Println("Config file: (none, using defaults)")
				fmt.Println()
			}
		}

		out, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		fmt.Print(string(out))
		return nil
	},
}

func init() {
	configShowCmd.Flags().BoolVar(&configShowSource, "source", false, "show config file source")
	configCmd.AddCommand(configShowCmd)
}
