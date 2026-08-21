package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// These tests drive the assembled cobra commands in-process, covering the wiring
// between flags, configuration loading, and environment resolution. They stay
// on paths that never reach a database: the CLI's own validation, the
// diagnostic commands, and the errors raised before connecting.

// runCLI executes the root command in a scratch directory containing config, and
// returns everything written to stdout. The CLI writes to os.Stdout directly
// (cobra's output stream only carries usage and errors), so stdout is captured
// around the call.
func runCLI(t *testing.T, config string, args ...string) (string, error) {
	t.Helper()
	return runCLIWithFiles(t, config, nil, args...)
}

// runCLIWithFiles is runCLI with extra files seeded into the scratch directory,
// for commands that read the local schema before doing anything else.
func runCLIWithFiles(t *testing.T, config string, files map[string]string, args ...string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	if config != "" {
		writeFile(t, dir+"/melange.yaml", config)
	}
	for name, content := range files {
		if sub := filepath.Dir(name); sub != "." {
			if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
				t.Fatalf("creating %s: %v", sub, err)
			}
		}
		writeFile(t, filepath.Join(dir, name), content)
	}
	t.Chdir(dir)

	// Flag values are bound to package globals, so a value set by one run would
	// otherwise leak into the next. Reset the whole command tree, then the
	// globals PersistentPreRunE populates, so each run starts from a clean slate
	// regardless of test order.
	resetFlags(rootCmd)
	cfgFile, envFlag, quiet = "", "", false
	activeEnv, configPath, envResolveErr = "", "", nil
	cfg, baseCfg = nil, nil
	noUpdateCheck = true
	configShowSource, configShowReveal = false, false
	t.Cleanup(func() { noUpdateCheck = false })

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	rootCmd.SetArgs(args)
	rootCmd.SetOut(&bytes.Buffer{})
	rootCmd.SetErr(&bytes.Buffer{})
	runErr := rootCmd.Execute()

	_ = w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String(), runErr
}

// resetFlags restores every flag in the command tree to its declared default.
func resetFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	}
	cmd.Flags().VisitAll(reset)
	cmd.PersistentFlags().VisitAll(reset)
	for _, sub := range cmd.Commands() {
		resetFlags(sub)
	}
}

const configWithEnvironments = `
schema: melange/schema.fga
database:
  url: postgres://admin:basesecret@localhost:5432/app
default_environment: local
environments:
  local:
    database:
      url: postgres://localhost:5432/app
  production:
    database:
      url: postgres://app:prodsecret@prod:5432/app
`

func TestCLI_ConfigShow_MasksSecrets(t *testing.T) {
	out, err := runCLI(t, configWithEnvironments, "config", "show")
	if err != nil {
		t.Fatalf("config show: %v", err)
	}

	for _, secret := range []string{"basesecret", "prodsecret"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q printed in cleartext:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "****") {
		t.Errorf("expected masked passwords in:\n%s", out)
	}
}

func TestCLI_ConfigShow_RevealSecretsOptsIn(t *testing.T) {
	out, err := runCLI(t, configWithEnvironments, "config", "show", "--reveal-secrets")
	if err != nil {
		t.Fatalf("config show --reveal-secrets: %v", err)
	}

	if !strings.Contains(out, "prodsecret") {
		t.Errorf("--reveal-secrets should print secrets, got:\n%s", out)
	}
}

func TestCLI_ConfigShow_Source(t *testing.T) {
	out, err := runCLI(t, configWithEnvironments, "config", "show", "--source", "--env", "production")
	if err != nil {
		t.Fatalf("config show --source: %v", err)
	}

	if !strings.Contains(out, "Config file: ") || !strings.Contains(out, "melange.yaml") {
		t.Errorf("--source should report the config file, got:\n%s", out)
	}
	if !strings.Contains(out, "Environment: production") {
		t.Errorf("--source should report the active environment, got:\n%s", out)
	}
}

func TestCLI_EnvList(t *testing.T) {
	out, err := runCLI(t, configWithEnvironments, "env", "list")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}

	for _, want := range []string{"Environments:", "local", "production", "Default: local"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "prodsecret") {
		t.Errorf("env list must not print credentials:\n%s", out)
	}
}

// The default_environment is applied without a flag, and marked as active.
func TestCLI_EnvList_MarksDefaultEnvironmentActive(t *testing.T) {
	out, err := runCLI(t, configWithEnvironments, "env", "list")
	if err != nil {
		t.Fatalf("env list: %v", err)
	}

	if !strings.Contains(out, "* local") {
		t.Errorf("the environment selected by default_environment should be marked active:\n%s", out)
	}
}

// A typo in --env must stop the command rather than run it against the base
// (often local) database.
func TestCLI_UndefinedEnvironmentIsRejected(t *testing.T) {
	_, err := runCLI(t, configWithEnvironments, "config", "show", "--env", "prod")
	if err == nil {
		t.Fatal("expected an error for an undefined environment")
	}
	if !strings.Contains(err.Error(), "prod") {
		t.Errorf("error should name the environment, got: %v", err)
	}
}

func TestCLI_ValidationErrorsBeforeConnecting(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"diff format", []string{"diff", "--format", "yaml"}, "invalid --format"},
		{"diff sources", []string{"diff", "--git-ref", "main", "--previous-schema", "old.fga"}, "mutually exclusive"},
		{"diff db with git ref", []string{"diff", "--db", "postgres://localhost/app", "--git-ref", "main"}, "cannot be combined"},
		{"history format", []string{"history", "--format", "yaml"}, "invalid --format"},
		{"history limit", []string{"history", "--limit", "0"}, "--limit"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, configWithEnvironments, tc.args...)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// Without a database URL anywhere, commands that need one fail with a
// configuration error rather than attempting a connection.
func TestCLI_MissingDatabaseURL(t *testing.T) {
	_, err := runCLI(t, "schema: melange/schema.fga\n", "history")
	if err == nil {
		t.Fatal("expected an error when no database is configured")
	}
}

// unreachableDB refuses immediately, so these cases stay fast and deterministic.
const unreachableDB = "postgres://127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"

// A database that cannot be reached must surface as a clear error from every
// command that needs one — not a panic, and not a silently empty result that
// would read as "nothing is deployed".
func TestCLI_UnreachableDatabaseReportsAnError(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"status", []string{"status", "--db", unreachableDB}},
		{"status json", []string{"status", "--db", unreachableDB, "--format", "json"}},
		{"history", []string{"history", "--db", unreachableDB}},
		{"schema pull", []string{"schema", "pull", "--db", unreachableDB}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runCLI(t, "schema: melange/schema.fga\n", tc.args...)
			if err == nil {
				t.Fatalf("expected an error; output was:\n%s", out)
			}
			if !strings.Contains(err.Error(), "connect") {
				t.Errorf("error should describe the connection failure, got: %v", err)
			}
		})
	}
}

// diff parses the local schema before it connects, so it needs one on disk to
// reach the database at all.
func TestCLI_DiffUnreachableDatabase(t *testing.T) {
	_, err := runCLIWithFiles(t, "schema: melange/schema.fga\n",
		map[string]string{"melange/schema.fga": "model\n  schema 1.1\ntype user\n"},
		"diff", "--db", unreachableDB)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("error should describe the connection failure, got: %v", err)
	}
}

// A missing local schema is reported as such, rather than as a database problem.
func TestCLI_DiffMissingLocalSchema(t *testing.T) {
	_, err := runCLI(t, "schema: melange/schema.fga\n", "diff", "--db", unreachableDB)
	if err == nil {
		t.Fatal("expected an error for a missing schema file")
	}
	if !strings.Contains(err.Error(), "melange/schema.fga") {
		t.Errorf("error should name the missing schema, got: %v", err)
	}
}

// status keeps its reachability signal even when the schema file is missing:
// the file and view checks still report, so the command stays useful.
func TestCLI_StatusFormatValidatedBeforeConnecting(t *testing.T) {
	_, err := runCLI(t, "schema: melange/schema.fga\n", "status", "--db", unreachableDB, "--format", "yaml")
	if err == nil {
		t.Fatal("expected an invalid-format error")
	}
	if !strings.Contains(err.Error(), "invalid --format") {
		t.Errorf("format should be validated before connecting, got: %v", err)
	}
}
