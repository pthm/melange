package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindConfigFile_ExplicitPath(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "custom.yaml")
	err := os.WriteFile(tmpFile, []byte("schema: test.fga"), 0o644)
	require.NoError(t, err)

	path, err := findConfigFile(tmpFile)
	require.NoError(t, err)
	assert.Equal(t, tmpFile, path)
}

func TestFindConfigFile_ExplicitPathNotFound(t *testing.T) {
	_, err := findConfigFile("/nonexistent/path/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file not found")
}

func TestFindConfigFile_AutoDiscovery(t *testing.T) {
	// Create directory structure with .git and melange.yaml
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	configPath := filepath.Join(root, "melange.yaml")
	err = os.WriteFile(configPath, []byte("schema: test.fga"), 0o644)
	require.NoError(t, err)

	// Create nested directory
	nested := filepath.Join(root, "deep", "nested")
	err = os.MkdirAll(nested, 0o755)
	require.NoError(t, err)

	// Change to nested directory
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(nested)
	require.NoError(t, err)

	path, err := findConfigFile("")
	require.NoError(t, err)

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(configPath)
	actualPath, _ := filepath.EvalSymlinks(path)
	assert.Equal(t, expectedPath, actualPath)
}

func TestFindConfigFile_PrefersMelangeYamlOverYml(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	// Create both files
	yamlPath := filepath.Join(root, "melange.yaml")
	ymlPath := filepath.Join(root, "melange.yml")
	err = os.WriteFile(yamlPath, []byte("schema: yaml.fga"), 0o644)
	require.NoError(t, err)
	err = os.WriteFile(ymlPath, []byte("schema: yml.fga"), 0o644)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	path, err := findConfigFile("")
	require.NoError(t, err)

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(yamlPath)
	actualPath, _ := filepath.EvalSymlinks(path)
	assert.Equal(t, expectedPath, actualPath) // Should prefer .yaml
}

func TestFindConfigFile_StopsAtGitRoot(t *testing.T) {
	// Config above .git should not be found
	root := t.TempDir()
	err := os.WriteFile(filepath.Join(root, "melange.yaml"), []byte("schema: above.fga"), 0o644)
	require.NoError(t, err)

	project := filepath.Join(root, "project")
	err = os.MkdirAll(project, 0o755)
	require.NoError(t, err)
	err = os.Mkdir(filepath.Join(project, ".git"), 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(project)
	require.NoError(t, err)

	path, err := findConfigFile("")
	require.NoError(t, err)
	assert.Empty(t, path) // Should not find config above .git
}

func TestFindConfigFile_NoConfigReturnsEmpty(t *testing.T) {
	// Create directory with .git but no config
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	path, err := findConfigFile("")
	require.NoError(t, err)
	assert.Empty(t, path)
}

func TestLoadConfig_Defaults(t *testing.T) {
	// Create directory with .git but no config
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	cfg, configPath, err := LoadConfig("")
	require.NoError(t, err)
	assert.Empty(t, configPath)

	// Check defaults
	assert.Equal(t, "schemas/schema.fga", cfg.Schema)
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "prefer", cfg.Database.SSLMode)
	assert.Equal(t, "authz", cfg.Generate.Client.Package)
	assert.Equal(t, "string", cfg.Generate.Client.IDType)
}

func TestLoadConfig_FromFile(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	configPath := filepath.Join(root, "melange.yaml")
	err = os.WriteFile(configPath, []byte(`
schema: custom/schema.fga
database:
  host: localhost
  name: testdb
  user: testuser
generate:
  client:
    runtime: go
    package: myauthz
`), 0o644)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	cfg, foundPath, err := LoadConfig("")
	require.NoError(t, err)

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(configPath)
	actualPath, _ := filepath.EvalSymlinks(foundPath)
	assert.Equal(t, expectedPath, actualPath)

	assert.Equal(t, "custom/schema.fga", cfg.Schema)
	assert.Equal(t, "localhost", cfg.Database.Host)
	assert.Equal(t, "testdb", cfg.Database.Name)
	assert.Equal(t, "testuser", cfg.Database.User)
	assert.Equal(t, "go", cfg.Generate.Client.Runtime)
	assert.Equal(t, "myauthz", cfg.Generate.Client.Package)

	// Check that defaults are still applied for unset values
	assert.Equal(t, 5432, cfg.Database.Port)
	assert.Equal(t, "prefer", cfg.Database.SSLMode)
	assert.Equal(t, "string", cfg.Generate.Client.IDType)
}

func TestLoadConfig_EnvOverridesFile(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	configPath := filepath.Join(root, "melange.yaml")
	err = os.WriteFile(configPath, []byte("schema: file.fga"), 0o644)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	// Set env var
	t.Setenv("MELANGE_SCHEMA", "env.fga")

	cfg, _, err := LoadConfig("")
	require.NoError(t, err)

	// Env should override file
	assert.Equal(t, "env.fga", cfg.Schema)
}

func TestLoadConfig_NestedEnvVars(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	// Set nested env vars
	t.Setenv("MELANGE_DATABASE_HOST", "envhost")
	t.Setenv("MELANGE_DATABASE_PORT", "5433")
	t.Setenv("MELANGE_GENERATE_CLIENT_RUNTIME", "typescript")

	cfg, _, err := LoadConfig("")
	require.NoError(t, err)

	assert.Equal(t, "envhost", cfg.Database.Host)
	assert.Equal(t, 5433, cfg.Database.Port)
	assert.Equal(t, "typescript", cfg.Generate.Client.Runtime)
}

func TestDSN_FromURL(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			URL: "postgres://custom:pass@host:5433/db",
		},
	}

	dsn, err := cfg.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://custom:pass@host:5433/db", dsn)
}

func TestDSN_FromDiscreteFields(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			Name:     "testdb",
			User:     "testuser",
			Password: "secret",
			SSLMode:  "require",
		},
	}

	dsn, err := cfg.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://testuser:secret@localhost:5432/testdb?sslmode=require", dsn)
}

func TestDSN_FromDiscreteFieldsNoPassword(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host:    "localhost",
			Port:    5432,
			Name:    "testdb",
			User:    "testuser",
			SSLMode: "disable",
		},
	}

	dsn, err := cfg.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://testuser@localhost:5432/testdb?sslmode=disable", dsn)
}

func TestDSN_URLTakesPrecedence(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			URL:  "postgres://url-user@url-host/url-db",
			Host: "field-host",
			Name: "field-db",
			User: "field-user",
		},
	}

	dsn, err := cfg.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://url-user@url-host/url-db", dsn)
}

func TestDSN_MissingHost(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Name: "testdb",
			User: "testuser",
		},
	}

	_, err := cfg.DSN()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.host is required")
}

func TestDSN_MissingName(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host: "localhost",
			User: "testuser",
		},
	}

	_, err := cfg.DSN()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.name is required")
}

func TestDSN_MissingUser(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host: "localhost",
			Name: "testdb",
		},
	}

	_, err := cfg.DSN()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.user is required")
}

func TestForEnvironment_EmptyReturnsBase(t *testing.T) {
	cfg := &Config{
		Schema:   "schema.fga",
		Database: DatabaseConfig{URL: "postgres://localhost/dev"},
	}

	resolved, err := cfg.ForEnvironment("")
	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/dev", resolved.Database.URL)
	assert.Equal(t, "schema.fga", resolved.Schema)
}

func TestForEnvironment_UndefinedIsError(t *testing.T) {
	cfg := &Config{Database: DatabaseConfig{URL: "postgres://localhost/dev"}}

	_, err := cfg.ForEnvironment("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `environment "production" is not defined`)
}

func TestForEnvironment_OverlayFallsThroughToBase(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			Host: "base-host", Port: 5432, Name: "base", User: "base-user", SSLMode: "prefer",
		},
		Environments: map[string]EnvironmentConfig{
			"staging": {Database: DatabaseConfig{Host: "staging-host", Name: "app"}},
		},
	}

	resolved, err := cfg.ForEnvironment("staging")
	require.NoError(t, err)
	// Overridden fields win.
	assert.Equal(t, "staging-host", resolved.Database.Host)
	assert.Equal(t, "app", resolved.Database.Name)
	// Unset fields fall through to base.
	assert.Equal(t, 5432, resolved.Database.Port)
	assert.Equal(t, "base-user", resolved.Database.User)
	assert.Equal(t, "prefer", resolved.Database.SSLMode)
}

func TestForEnvironment_DiscreteFieldsDropInheritedBaseURL(t *testing.T) {
	// Base uses a URL; the environment specifies a discrete connection. The
	// inherited URL must not shadow the discrete fields (DSN prefers URL).
	cfg := &Config{
		// Port/SSLMode mirror the defaults viper applies to the base block.
		Database: DatabaseConfig{URL: "postgres://localhost:5432/dev", Port: 5432, SSLMode: "prefer"},
		Environments: map[string]EnvironmentConfig{
			"staging": {Database: DatabaseConfig{Host: "staging-host", Name: "app", User: "melange"}},
		},
	}

	resolved, err := cfg.ForEnvironment("staging")
	require.NoError(t, err)
	assert.Empty(t, resolved.Database.URL)

	dsn, err := resolved.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://melange@staging-host:5432/app?sslmode=prefer", dsn)
}

func TestForEnvironment_DiscreteOverrideOnURLBaseNeedsFullConnection(t *testing.T) {
	// Base is a URL; an environment overrides only the host. Melange does not
	// decompose the base URL, so the dropped URL means the discrete connection
	// is incomplete and DSN() reports a clear error (predictable over clever).
	cfg := &Config{
		Database: DatabaseConfig{URL: "postgres://melange:pw@localhost:5432/app"},
		Environments: map[string]EnvironmentConfig{
			"staging": {Database: DatabaseConfig{Host: "staging.db"}},
		},
	}

	resolved, err := cfg.ForEnvironment("staging")
	require.NoError(t, err)
	assert.Empty(t, resolved.Database.URL)
	_, err = resolved.DSN()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is required when database.url is not set")
}

func TestForEnvironment_RefURLBaseReplacedByDiscreteEnv(t *testing.T) {
	// Base URL is an unset ${VAR}, but the environment supplies a complete
	// discrete connection. The dead base reference must not be required.
	cfg := &Config{
		// Port/SSLMode mirror the defaults viper applies to the base block.
		Database: DatabaseConfig{URL: "${UNSET_BASE_URL_XYZ}", Port: 5432, SSLMode: "prefer"},
		Environments: map[string]EnvironmentConfig{
			"staging": {Database: DatabaseConfig{Host: "staging.db", Name: "app", User: "melange"}},
		},
	}

	resolved, err := cfg.ForEnvironment("staging")
	require.NoError(t, err)
	dsn, err := resolved.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://melange@staging.db:5432/app?sslmode=prefer", dsn)
}

func TestForEnvironment_URLOverridesBase(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{Host: "base", Name: "base", User: "base"},
		Environments: map[string]EnvironmentConfig{
			"production": {Database: DatabaseConfig{URL: "postgres://prod/app"}},
		},
	}

	resolved, err := cfg.ForEnvironment("production")
	require.NoError(t, err)
	dsn, err := resolved.DSN()
	require.NoError(t, err)
	assert.Equal(t, "postgres://prod/app", dsn)
}

func TestForEnvironment_ExpandsVarRefs(t *testing.T) {
	t.Setenv("PROD_DATABASE_URL", "postgres://prod-user:pw@prod.db/app")
	cfg := &Config{
		Environments: map[string]EnvironmentConfig{
			"production": {Database: DatabaseConfig{URL: "${PROD_DATABASE_URL}"}},
		},
	}

	resolved, err := cfg.ForEnvironment("production")
	require.NoError(t, err)
	assert.Equal(t, "postgres://prod-user:pw@prod.db/app", resolved.Database.URL)
}

func TestForEnvironment_OverridesSchema(t *testing.T) {
	cfg := &Config{
		Schema: "base.fga",
		Environments: map[string]EnvironmentConfig{
			"legacy": {Schema: "legacy.fga"},
		},
	}

	resolved, err := cfg.ForEnvironment("legacy")
	require.NoError(t, err)
	assert.Equal(t, "legacy.fga", resolved.Schema)
}

func TestExpandEnvRefs_BracedOnly(t *testing.T) {
	t.Setenv("MY_VAR", "value")
	var missing []string
	// Braced form expands.
	assert.Equal(t, "prefix-value-suffix", expandEnvRefs("prefix-${MY_VAR}-suffix", &missing))
	// Bare $VAR and literal $ are left untouched (avoid mangling passwords).
	assert.Equal(t, "p@ss$MY_VAR", expandEnvRefs("p@ss$MY_VAR", &missing))
	assert.Equal(t, "pa$sword", expandEnvRefs("pa$sword", &missing))
	assert.Empty(t, missing)

	// Unset braced form expands to empty and is reported as missing.
	assert.Equal(t, "a--b", expandEnvRefs("a-${UNSET_VAR_XYZ}-b", &missing))
	assert.Equal(t, []string{"UNSET_VAR_XYZ"}, missing)
}

func TestForEnvironment_OverriddenBaseSecretNotRequired(t *testing.T) {
	// Base URL references an unset local secret, but production overrides the
	// URL entirely. The unset base secret must NOT be required.
	t.Setenv("PROD_DATABASE_URL", "postgres://prod/app")
	// Deliberately do NOT set LOCAL_DATABASE_URL.
	cfg := &Config{
		Database: DatabaseConfig{URL: "${LOCAL_DATABASE_URL}"},
		Environments: map[string]EnvironmentConfig{
			"production": {Database: DatabaseConfig{URL: "${PROD_DATABASE_URL}"}},
		},
	}

	resolved, err := cfg.ForEnvironment("production")
	require.NoError(t, err)
	assert.Equal(t, "postgres://prod/app", resolved.Database.URL)

	// The base config on its own, however, does require its secret.
	_, err = cfg.ForEnvironment("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOCAL_DATABASE_URL")
}

func TestForEnvironment_UnsetVarIsError(t *testing.T) {
	cfg := &Config{
		Environments: map[string]EnvironmentConfig{
			"production": {Database: DatabaseConfig{URL: "${DEFINITELY_UNSET_VAR_ABC}"}},
		},
	}
	_, err := cfg.ForEnvironment("production")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unset environment variable")
	assert.Contains(t, err.Error(), "DEFINITELY_UNSET_VAR_ABC")
}

func TestForEnvironment_UnsetVarReturnsBestEffortConfig(t *testing.T) {
	// On an unset connection secret the error is returned, but the config is
	// still populated best-effort so non-connection overlay (schema) survives —
	// an explicit --db can then proceed with the right schema.
	cfg := &Config{
		Schema: "base.fga",
		Environments: map[string]EnvironmentConfig{
			"production": {
				Schema:   "prod.fga",
				Database: DatabaseConfig{URL: "${DEFINITELY_UNSET_VAR_ABC}"},
			},
		},
	}

	resolved, err := cfg.ForEnvironment("production")
	require.Error(t, err)
	require.NotNil(t, resolved)
	assert.Equal(t, "prod.fga", resolved.Schema) // overlay preserved
	assert.Empty(t, resolved.Database.URL)       // unset secret expanded to empty
}

func TestForEnvironment_ExpandsSchemaRef(t *testing.T) {
	t.Setenv("SCHEMA_DIR", "/etc/authz")
	cfg := &Config{
		Schema: "base.fga",
		Environments: map[string]EnvironmentConfig{
			"prod": {Schema: "${SCHEMA_DIR}/prod.fga"},
		},
	}
	resolved, err := cfg.ForEnvironment("prod")
	require.NoError(t, err)
	assert.Equal(t, "/etc/authz/prod.fga", resolved.Schema)
}

func TestForEnvironment_UnsetSchemaRefDoesNotBlockConnection(t *testing.T) {
	// An unset schema reference must NOT be treated as a database-resolution
	// error: the DSN is valid and the schema can be overridden by --schema.
	cfg := &Config{
		Database: DatabaseConfig{URL: "postgres://localhost/dev"},
		Environments: map[string]EnvironmentConfig{
			"prod": {Schema: "${UNSET_SCHEMA_PATH_XYZ}"},
		},
	}
	resolved, err := cfg.ForEnvironment("prod")
	require.NoError(t, err) // no error: only database refs block
	assert.Equal(t, "postgres://localhost/dev", resolved.Database.URL)
}

func TestEnvironmentSummary_RedactsAndKeepsRefs(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{URL: "postgres://localhost/dev"},
		Environments: map[string]EnvironmentConfig{
			"production": {Database: DatabaseConfig{URL: "${PROD_DATABASE_URL}"}},
			"staging":    {Database: DatabaseConfig{Host: "staging.db", Name: "app"}},
			"withpw":     {Database: DatabaseConfig{URL: "postgres://user:secret@host/db"}},
		},
	}

	base, err := cfg.EnvironmentSummary("")
	require.NoError(t, err)
	assert.Equal(t, "postgres://localhost/dev", base)

	// ${VAR} references are shown literally, not expanded.
	prod, err := cfg.EnvironmentSummary("production")
	require.NoError(t, err)
	assert.Equal(t, "${PROD_DATABASE_URL}", prod)

	// Discrete fields render as host:port/name.
	staging, err := cfg.EnvironmentSummary("staging")
	require.NoError(t, err)
	assert.Equal(t, "staging.db:5432/app", staging)

	// Passwords are redacted.
	withpw, err := cfg.EnvironmentSummary("withpw")
	require.NoError(t, err)
	assert.Equal(t, "postgres://user:****@host/db", withpw)

	_, err = cfg.EnvironmentSummary("missing")
	require.Error(t, err)
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		// Plain password.
		"postgres://user:secret@host/db": "postgres://user:****@host/db",
		// Percent-encoded password must still be redacted (regression: the
		// decoded value would not match the raw bytes).
		"postgres://user:p%40ss@host:5432/db?sslmode=require": "postgres://user:****@host:5432/db?sslmode=require",
		// Unescaped '@' in the password: split at the LAST '@', not the first,
		// so nothing leaks.
		"postgres://user:p@ss@host/db": "postgres://user:****@host/db",
		// Password carried as a query parameter must be masked too.
		"postgres://host/db?password=secret":              "postgres://host/db?password=****",
		"postgres://host/db?user=app&password=s3cr3t&x=1": "postgres://host/db?user=app&password=****&x=1",
		// Both userinfo and query password.
		"postgres://u:pw@host/db?password=q": "postgres://u:****@host/db?password=****",
		// No password — returned unchanged.
		"postgres://user@host/db": "postgres://user@host/db",
		// No userinfo — returned unchanged.
		"postgres://host:5432/db": "postgres://host:5432/db",
		// Unparseable / reference — returned unchanged.
		"${PROD_DATABASE_URL}": "${PROD_DATABASE_URL}",
	}
	for in, want := range cases {
		assert.Equal(t, want, redactURL(in), "redactURL(%q)", in)
	}
}

func TestLoadConfig_ParsesEnvironments(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))

	configPath := filepath.Join(root, "melange.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
schema: melange/schema.fga
database:
  url: postgres://localhost/dev
default_environment: local
environments:
  local:
    database:
      url: postgres://localhost/dev
  production:
    database:
      url: ${PROD_DATABASE_URL}
`), 0o644))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(root))

	cfg, _, err := LoadConfig("")
	require.NoError(t, err)
	assert.Equal(t, "local", cfg.DefaultEnvironment)
	require.Contains(t, cfg.Environments, "production")
	assert.Equal(t, "${PROD_DATABASE_URL}", cfg.Environments["production"].Database.URL)
}

func TestLoadConfig_MigrateAndGenerateMigrationDefaults(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, ".git"), 0o755)
	require.NoError(t, err)

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	err = os.Chdir(root)
	require.NoError(t, err)

	cfg, _, err := LoadConfig("")
	require.NoError(t, err)

	// melange migrate defaults
	assert.False(t, cfg.Migrate.DryRun)
	assert.False(t, cfg.Migrate.Force)

	// melange generate migration defaults
	assert.Equal(t, "melange", cfg.Generate.Migration.Name)
	assert.Equal(t, "split", cfg.Generate.Migration.Format)
	assert.Empty(t, cfg.Generate.Migration.Output)
}
