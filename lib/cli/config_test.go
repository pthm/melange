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

func TestResolvedSchema(t *testing.T) {
	cfg := &Config{
		Schema: "top-level.fga",
		Generate: GenerateConfig{
			Client: ClientConfig{
				Schema: "client-specific.fga",
			},
		},
	}

	// Client-specific takes precedence
	assert.Equal(t, "client-specific.fga", cfg.ResolvedSchema())

	// Falls back to top-level
	cfg.Generate.Client.Schema = ""
	assert.Equal(t, "top-level.fga", cfg.ResolvedSchema())
}
