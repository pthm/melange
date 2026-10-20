package command

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/cli/style"
	"github.com/pthm/melange/internal/update"
)

var (
	// Global state set during PersistentPreRunE
	cfg           *cli.Config
	baseCfg       *cli.Config // config before environment overlay (for 'env list')
	configPath    string
	activeEnv     string // environment applied to cfg (empty = base config)
	envResolveErr error  // deferred environment-resolution failure, surfaced at connect time

	// Persistent flags
	cfgFile       string
	envFlag       string
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
		if cmd.Name() == "help" || cmd.Name() == "completion" || cmd.Name() == "version" || cmd.Name() == "license" || cmd.Name() == "init" {
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

		// Resolve the active environment and overlay it onto cfg. Downstream
		// resolution (resolveDSN, cfg.DSN) operates on the resolved config.
		baseCfg = cfg
		res, err := resolveEnvironment(cfg, envFlag, os.Getenv("MELANGE_ENV"))
		if err != nil {
			return err
		}
		cfg, activeEnv, envResolveErr = res.Config, res.Active, res.Deferred
		if res.Warning != "" {
			fmt.Fprintf(os.Stderr, "warning: %s\n", res.Warning)
		}

		// Surface the target so a command running against a non-base environment
		// (especially one selected via default_environment) is never silent.
		if activeEnv != "" && !quiet {
			fmt.Fprintf(os.Stderr, "→ environment: %s\n", activeEnv)
		}

		return nil
	},
	SilenceUsage:  true, // Don't show usage on errors
	SilenceErrors: true, // We handle errors ourselves
}

// envResolution is the outcome of selecting an environment profile and applying
// it to the loaded configuration.
type envResolution struct {
	Config   *cli.Config // configuration with the environment overlaid (best effort)
	Active   string      // selected environment; empty means the base config
	Deferred error       // resolution failure to surface when a command connects
	Warning  string      // non-fatal note for the user
}

// resolveEnvironment picks the environment profile to run against and overlays
// it, following the documented precedence: --env, then MELANGE_ENV, then
// default_environment, then the base config.
//
// It distinguishes three failure modes deliberately. An explicitly named
// environment that does not exist is a hard error, so `--env prod` cannot
// silently run against a local database after a typo. A *stale*
// default_environment — one naming a profile that no longer exists — only warns
// and falls back to base. Anything else (almost always an unset ${VAR} secret)
// is deferred rather than fatal, so diagnostic commands such as `env list`,
// `config show`, and `validate` still run; commands that connect surface it
// through resolveDSN.
func resolveEnvironment(base *cli.Config, flagEnv, osEnv string) (envResolution, error) {
	explicit := resolveString(flagEnv, osEnv)
	active := resolveString(explicit, base.DefaultEnvironment)

	if explicit != "" && !base.HasEnvironment(explicit) {
		return envResolution{}, cli.ConfigError("resolving environment",
			fmt.Errorf("environment %q is not defined in configuration", explicit))
	}

	out := envResolution{Active: active}
	resolved, err := base.ForEnvironment(active)
	if err != nil && explicit == "" && active != "" && !base.HasEnvironment(active) {
		out.Warning = fmt.Sprintf("default_environment %q is not defined; using base config", active)
		out.Active = ""
		resolved, err = base.ForEnvironment("")
	}
	if err != nil {
		// Keep the best-effort config: a non-connection overlay (schema, say)
		// still applies, and an explicit --db can still proceed.
		out.Deferred = err
	}
	out.Config = resolved
	return out, nil
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
	rootCmd.PersistentFlags().StringVar(&envFlag, "env", "", "environment profile to target (see 'environments' in config)")
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
	schemaCmd.GroupID = groupSchema
	diffCmd.GroupID = groupSchema
	historyCmd.GroupID = groupSchema
	doctorCmd.GroupID = groupSchema
	explainCmd.GroupID = groupSchema
	expandCmd.GroupID = groupSchema
	studioCmd.GroupID = groupSchema
	rootCmd.AddCommand(validateCmd)
	rootCmd.AddCommand(migrateCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(schemaCmd)
	rootCmd.AddCommand(diffCmd)
	rootCmd.AddCommand(historyCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(expandCmd)
	rootCmd.AddCommand(studioCmd)

	// Client commands
	generateCmd.GroupID = groupClient
	rootCmd.AddCommand(generateCmd)

	// Utility commands
	initCmd.GroupID = groupUtility
	configCmd.GroupID = groupUtility
	envCmd.GroupID = groupUtility
	versionCmd.GroupID = groupUtility
	licenseCmd.GroupID = groupUtility
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(envCmd)
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

// boolCount returns the number of true values.
func boolCount(values ...bool) int {
	n := 0
	for _, v := range values {
		if v {
			n++
		}
	}
	return n
}

// isCI reports whether the process is running under a CI system by checking
// the standard CI environment variable set by most CI providers.
func isCI() bool {
	return os.Getenv("CI") != ""
}

// ShowUpdateNoticeIfAvailable displays a version upgrade prompt if the background
// update check (started in PersistentPreRunE) found a newer release, preceded by
// any published security advisory that covers the running version. It must be
// called after the command completes because PersistentPostRun is skipped when
// commands return errors.
func ShowUpdateNoticeIfAvailable() {
	// Wait briefly for results (1s should be fast enough for cached results,
	// and reasonable even for network fetch)
	info := awaitUpdateInfo(1 * time.Second)
	if info == nil {
		return
	}
	if affected := update.Affecting(info.Advisories, info.CurrentVersion); len(affected) > 0 {
		showAdvisoryNotice(info.CurrentVersion, affected)
	}
	if info.UpdateAvailable {
		showUpdateNotice(info)
	}
}

// advisoryWait bounds how long doctor and status wait for the background
// update check before skipping the advisory check. It started before the
// command connected to the database, so a cached result is already there.
const advisoryWait = 3 * time.Second

// awaitUpdateInfo returns the background update check result, waiting up to
// timeout. The result is put back on the channel so later callers (the exit
// notice) still see it. It returns nil when the check is disabled, failed, or
// has not finished in time.
func awaitUpdateInfo(timeout time.Duration) *update.Info {
	if updateResult == nil {
		return nil
	}
	select {
	case info := <-updateResult:
		updateResult <- info
		return info
	case <-time.After(timeout):
		return nil
	}
}

// Notice styles render on stderr, so color follows stderr's terminal and
// NO_COLOR rather than stdout's: redirected output stays plain text.
var (
	noticeDanger = style.New(style.Stderr).Bold(true).Foreground(style.Fail)
	noticeAccent = style.New(style.Stderr).Bold(true).Foreground(style.Accent)
	noticeBold   = style.New(style.Stderr).Bold(true)
	noticeDim    = style.New(style.Stderr).Foreground(style.Muted)
)

// showAdvisoryNotice warns that the running CLI is covered by security
// advisories. Most fixes are in generated SQL, so upgrading alone is not
// enough: the database must be migrated again.
func showAdvisoryNotice(current string, affected []update.Advisory) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, noticeDanger.Render(fmt.Sprintf("! melange %s is affected by %d security advisor%s:",
		current, len(affected), pluralY(len(affected)))))
	for _, a := range affected {
		fmt.Fprintf(os.Stderr, "  %s %s %s\n    %s\n",
			noticeBold.Render(a.ID),
			noticeDim.Render(fmt.Sprintf("(%s, %s)", a.Severity, a.FixedIn())),
			a.Summary,
			noticeDim.Render(a.URL))
	}
	fmt.Fprintln(os.Stderr, "  Upgrade to a fixed release, then run "+noticeBold.Render("melange migrate")+" to regenerate the database functions.")
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// showUpdateNotice prints an upgrade prompt to stderr. It is called only when
// a newer release is confirmed available, so the caller should always check
// info.UpdateAvailable before invoking this.
func showUpdateNotice(info *update.Info) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s %s\n",
		noticeAccent.Render("* A new version of melange is available: v"+info.LatestVersion),
		noticeDim.Render("(current: "+info.CurrentVersion+")"))
	fmt.Fprintln(os.Stderr, noticeDim.Render("  brew upgrade melange  or  go install github.com/pthm/melange@latest"))
}
