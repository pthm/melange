package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/update"
)

var (
	// Global state set during PersistentPreRunE
	cfg        *cli.Config
	configPath string

	// Persistent flags
	cfgFile       string
	verbose       int
	quiet         bool
	noUpdateCheck bool

	// Update check result channel
	updateResult chan *update.Info
)

var rootCmd = &cobra.Command{
	Use:   "melange",
	Short: "PostgreSQL Fine-Grained Authorization",
	Long: `melange - PostgreSQL Fine-Grained Authorization

Melange is an authorization compiler that generates specialized SQL functions
from OpenFGA schemas, enabling single-query permission checks in PostgreSQL.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip config loading for help/completion/version/license commands
		if cmd.Name() == "help" || cmd.Name() == "completion" || cmd.Name() == "version" || cmd.Name() == "license" {
			return nil
		}

		// Start background update check (unless disabled)
		if !noUpdateCheck && !isCI() {
			updateResult = make(chan *update.Info, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				info, _ := update.CheckWithCache(ctx)
				updateResult <- info
			}()
		}

		var err error
		cfg, configPath, err = cli.LoadConfig(cfgFile)
		if err != nil {
			return cli.ConfigError("loading configuration", err)
		}

		return nil
	},
	SilenceUsage:  true, // Don't show usage on errors
	SilenceErrors: true, // We handle errors ourselves
}

// Command group IDs
const (
	groupSchema  = "schema"
	groupClient  = "client"
	groupUtility = "utility"
)

func init() {
	// Persistent flags (available to all commands)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: auto-discover melange.yaml)")
	rootCmd.PersistentFlags().CountVarP(&verbose, "verbose", "v", "increase verbosity (can be repeated)")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "suppress non-error output")
	rootCmd.PersistentFlags().BoolVar(&noUpdateCheck, "no-update-check", false, "disable update check")

	// Define command groups
	rootCmd.AddGroup(
		&cobra.Group{ID: groupSchema, Title: "Schema:"},
		&cobra.Group{ID: groupClient, Title: "Client:"},
		&cobra.Group{ID: groupUtility, Title: "Utility:"},
	)

	// Schema commands
	validateCmd.GroupID = groupSchema
	migrateCmd.GroupID = groupSchema
	statusCmd.GroupID = groupSchema
	doctorCmd.GroupID = groupSchema
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(doctorCmd)

	// Client commands
	generateCmd.GroupID = groupClient
	rootCmd.AddCommand(generateCmd)

	// Utility commands
	configCmd.GroupID = groupUtility
	versionCmd.GroupID = groupUtility
	licenseCmd.GroupID = groupUtility
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(licenseCmd)
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		cli.ExitWithError(err)
	}
}

// resolveString returns the first non-empty string from the provided values.
// Used to implement precedence: flag > config > default.
func resolveString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolveBool returns true if any of the provided values is true.
// Used for boolean flags where any true value should win.
func resolveBool(values ...bool) bool {
	for _, v := range values {
		if v {
			return true
		}
	}
	return false
}

// isCI detects if running in a CI environment
func isCI() bool {
	return os.Getenv("CI") != ""
}

// ShowUpdateNoticeIfAvailable checks for pending update results and displays a notice
// This should be called after command execution (from main.go) since PersistentPostRun
// doesn't run when commands return errors.
func ShowUpdateNoticeIfAvailable() {
	if updateResult == nil {
		return
	}

	// Wait briefly for results (1s should be fast enough for cached results,
	// and reasonable even for network fetch)
	select {
	case info := <-updateResult:
		if info != nil && info.UpdateAvailable {
			showUpdateNotice(info)
		}
	case <-time.After(1 * time.Second):
		// Check not finished in time, skip notice
	}
}

func showUpdateNotice(info *update.Info) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "* A new version of melange is available: v%s (current: %s)\n",
		info.LatestVersion, info.CurrentVersion)
	fmt.Fprintln(os.Stderr, "  brew upgrade melange  or  go install github.com/pthm/melange/cmd/melange@latest")
}
