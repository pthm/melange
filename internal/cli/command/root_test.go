package command

import (
	"strings"
	"testing"

	"github.com/pthm/melange/internal/cli"
)

func envConfig() *cli.Config {
	return &cli.Config{
		Database: cli.DatabaseConfig{URL: "postgres://localhost/base"},
		Environments: map[string]cli.EnvironmentConfig{
			"staging":    {Database: cli.DatabaseConfig{URL: "postgres://staging/app"}},
			"production": {Database: cli.DatabaseConfig{URL: "postgres://prod/app"}},
		},
	}
}

// --env beats MELANGE_ENV beats default_environment beats the base config.
func TestResolveEnvironment_Precedence(t *testing.T) {
	cfg := envConfig()
	cfg.DefaultEnvironment = "staging"

	cases := []struct {
		name        string
		flag, osEnv string
		wantActive  string
		wantURL     string
	}{
		{"flag wins", "production", "staging", "production", "postgres://prod/app"},
		{"env var when no flag", "", "production", "production", "postgres://prod/app"},
		{"default when neither", "", "", "staging", "postgres://staging/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := resolveEnvironment(cfg, tc.flag, tc.osEnv)
			if err != nil {
				t.Fatalf("resolveEnvironment: %v", err)
			}
			if res.Active != tc.wantActive {
				t.Errorf("active = %q, want %q", res.Active, tc.wantActive)
			}
			if res.Config.Database.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", res.Config.Database.URL, tc.wantURL)
			}
			if res.Deferred != nil || res.Warning != "" {
				t.Errorf("unexpected deferred=%v warning=%q", res.Deferred, res.Warning)
			}
		})
	}
}

// With no environments configured at all, behavior is identical to before the
// feature existed.
func TestResolveEnvironment_NoEnvironmentsUsesBase(t *testing.T) {
	base := &cli.Config{Database: cli.DatabaseConfig{URL: "postgres://localhost/base"}}

	res, err := resolveEnvironment(base, "", "")
	if err != nil {
		t.Fatalf("resolveEnvironment: %v", err)
	}
	if res.Active != "" || res.Config.Database.URL != "postgres://localhost/base" {
		t.Errorf("got active=%q url=%q, want base config", res.Active, res.Config.Database.URL)
	}
}

// Typo protection: `--env prod` must not quietly run against the base (often
// local) database because the profile is spelled `production`.
func TestResolveEnvironment_UndefinedExplicitEnvIsFatal(t *testing.T) {
	for _, tc := range []struct{ name, flag, osEnv string }{
		{"flag", "prod", ""},
		{"env var", "", "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveEnvironment(envConfig(), tc.flag, tc.osEnv)
			if err == nil {
				t.Fatal("an undefined explicit environment must be an error")
			}
			if !strings.Contains(err.Error(), "prod") {
				t.Errorf("error should name the environment, got %v", err)
			}
		})
	}
}

// A default_environment left pointing at a deleted profile is the one lenient
// case: warn and fall back rather than blocking every command.
func TestResolveEnvironment_StaleDefaultWarnsAndFallsBack(t *testing.T) {
	cfg := envConfig()
	cfg.DefaultEnvironment = "removed"

	res, err := resolveEnvironment(cfg, "", "")
	if err != nil {
		t.Fatalf("a stale default must not be fatal, got %v", err)
	}
	if res.Active != "" {
		t.Errorf("active = %q, want the base config", res.Active)
	}
	if !strings.Contains(res.Warning, "removed") {
		t.Errorf("warning = %q, want it to name the missing environment", res.Warning)
	}
	if res.Config.Database.URL != "postgres://localhost/base" {
		t.Errorf("url = %q, want the base URL", res.Config.Database.URL)
	}
}

// An unset ${VAR} secret is deferred to connect time so diagnostic commands
// still run, but it must not be lost — resolveDSN reports it later.
func TestResolveEnvironment_UnsetSecretIsDeferred(t *testing.T) {
	cfg := &cli.Config{
		Database: cli.DatabaseConfig{URL: "postgres://localhost/base"},
		Environments: map[string]cli.EnvironmentConfig{
			"production": {Database: cli.DatabaseConfig{URL: "${MELANGE_TEST_UNSET_SECRET}"}},
		},
		DefaultEnvironment: "production",
	}

	res, err := resolveEnvironment(cfg, "", "")
	if err != nil {
		t.Fatalf("resolution must not fail up front, got %v", err)
	}
	if res.Deferred == nil {
		t.Error("an unset secret must be reported at connect time, not dropped")
	}
	if res.Active != "production" {
		t.Errorf("active = %q; a defined default must not silently fall back to base", res.Active)
	}
}

func TestResolveString(t *testing.T) {
	if got := resolveString("", "", "fallback"); got != "fallback" {
		t.Errorf("resolveString = %q, want the last non-empty value", got)
	}
	if got := resolveString("first", "second"); got != "first" {
		t.Errorf("resolveString = %q, want the first non-empty value", got)
	}
	if got := resolveString(); got != "" {
		t.Errorf("resolveString() = %q, want empty", got)
	}
}

func TestBoolCount(t *testing.T) {
	if got := boolCount(true, false, true); got != 2 {
		t.Errorf("boolCount = %d, want 2", got)
	}
	if got := boolCount(false, false); got != 0 {
		t.Errorf("boolCount = %d, want 0", got)
	}
}
