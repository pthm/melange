package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/compiler"
)

// loadSQLConfig loads content as a discovered melange.yaml ("" for none) in
// a fresh repository root.
func loadSQLConfig(t *testing.T, content string) (*Config, error) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	if content != "" {
		require.NoError(t, os.WriteFile(filepath.Join(root, "melange.yaml"), []byte(content), 0o644))
	}
	t.Chdir(root)
	cfg, _, err := LoadConfig("")
	return cfg, err
}

func allFamilies(on bool) SQLFunctionsConfig {
	return SQLFunctionsConfig{ListObjects: on, ListSubjects: on, Accessible: on, ParentKey: on, Expand: on, Explain: on}
}

func off() *bool {
	b := false
	return &b
}

// Without a config file, and with a file or an sql section that leaves keys
// out, every key keeps its default: every relation, every family.
func TestLoadConfig_SQLDefaults(t *testing.T) {
	for name, content := range map[string]string{
		"no config file":        "",
		"no sql section":        "schema: s.fga\n",
		"empty sql section":     "sql:\n",
		"empty subsections":     "sql:\n  relations:\n  functions:\n",
		"only exclude":          "sql:\n  relations:\n    exclude: []\n",
		"only one family":       "sql:\n  functions:\n    expand: true\n",
		"explicit default list": "sql:\n  relations:\n    include: ['*']\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := loadSQLConfig(t, content)
			require.NoError(t, err)
			assert.Equal(t, []string{"*"}, cfg.SQL.Relations.Include)
			assert.Empty(t, cfg.SQL.Relations.Exclude)
			assert.Equal(t, allFamilies(true), cfg.SQL.Functions)
			assert.True(t, cfg.SQL.Exposure().IsDefault(), "%+v", cfg.SQL.Exposure())
		})
	}
}

func TestLoadConfig_SQLFromFile(t *testing.T) {
	cfg, err := loadSQLConfig(t, `
sql:
  relations:
    include: ["document#can_*", "folder#viewer"]
    exclude: ["document#can_admin"]
  functions:
    expand: false
    explain: false
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"document#can_*", "folder#viewer"}, cfg.SQL.Relations.Include)
	assert.Equal(t, []string{"document#can_admin"}, cfg.SQL.Relations.Exclude)
	want := allFamilies(true)
	want.Expand, want.Explain = false, false
	assert.Equal(t, want, cfg.SQL.Functions)
}

// MELANGE_SQL_* variables override the file key by key. A list is
// comma-separated, with spaces after the commas allowed.
func TestLoadConfig_SQLEnvOverridesFile(t *testing.T) {
	t.Setenv("MELANGE_SQL_RELATIONS_INCLUDE", "document#viewer, document#editor")
	t.Setenv("MELANGE_SQL_FUNCTIONS_LIST_SUBJECTS", "false")
	t.Setenv("MELANGE_SQL_FUNCTIONS_EXPAND", "true")
	cfg, err := loadSQLConfig(t, `
sql:
  relations:
    include: ["folder#viewer"]
    exclude: ["document#owner"]
  functions:
    expand: false
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"document#viewer", "document#editor"}, cfg.SQL.Relations.Include)
	assert.Equal(t, []string{"document#owner"}, cfg.SQL.Relations.Exclude, "keys without a variable keep the file value")
	want := allFamilies(true)
	want.ListSubjects = false
	assert.Equal(t, want, cfg.SQL.Functions)
}

// Variables bind without a config file too, since every key has a default.
func TestLoadConfig_SQLEnvWithoutFile(t *testing.T) {
	t.Setenv("MELANGE_SQL_RELATIONS_EXCLUDE", "owner")
	t.Setenv("MELANGE_SQL_FUNCTIONS_PARENT_KEY", "0")
	cfg, err := loadSQLConfig(t, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, cfg.SQL.Relations.Include)
	assert.Equal(t, []string{"owner"}, cfg.SQL.Relations.Exclude)
	assert.False(t, cfg.SQL.Functions.ParentKey)
}

// An invalid pattern fails the load, naming the key and the pattern, and the
// variable when it came from one.
func TestLoadConfig_SQLInvalidPattern(t *testing.T) {
	_, err := loadSQLConfig(t, "sql:\n  relations:\n    exclude: [\"a#b#c\"]\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sql.relations.exclude")
	assert.Contains(t, err.Error(), `"a#b#c"`)
	assert.NotContains(t, err.Error(), "MELANGE_")

	t.Setenv("MELANGE_SQL_RELATIONS_INCLUDE", "document#")
	_, err = loadSQLConfig(t, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sql.relations.include (from MELANGE_SQL_RELATIONS_INCLUDE)")
	assert.Contains(t, err.Error(), `"document#"`)
}

// A key the sql section does not know fails the load, naming the key: a
// misspelled key would otherwise keep its default and widen the API.
func TestLoadConfig_SQLUnknownKey(t *testing.T) {
	for content, want := range map[string]string{
		"sql:\n  functions:\n    expnd: false\n":                 "sql.functions.expnd: unknown key",
		"sql:\n  relation:\n    include: [document#can_*]\n":     "sql.relation.include: unknown key",
		"sql:\n  relations:\n    includes: [viewer]\n":           "sql.relations.includes: unknown key",
		"sql:\n  relations:\n    include:\n      viewer: true\n": "sql.relations.include.viewer: unknown key",
		"sql:\n  functions:\n    check: false\n":                 "sql.functions.check: check functions are always generated",
		"sql:\n  views: true\n":                                  "sql.views: unknown key",
		"sql: 5\n":                                               "sql: must be a section",
		"sql:\n  functions: false\n":                             "sql.functions: must be a section",
	} {
		_, err := loadSQLConfig(t, content)
		require.Error(t, err, content)
		assert.Contains(t, err.Error(), want, content)
	}

	for _, content := range []string{"sql:\n", "sql: {}\n", "sql:\n  relations:\n  functions:\n"} {
		_, err := loadSQLConfig(t, content)
		require.NoError(t, err, "an empty section is allowed: %q", content)
	}
	cfg, err := loadSQLConfig(t, "sql:\n  relations:\n    include:\n  functions:\n    Expand: false\n")
	require.NoError(t, err, "keys are case-insensitive and an empty list is allowed")
	assert.False(t, cfg.SQL.Functions.Expand)
}

// The sql section describes the schema, so an environment profile cannot
// carry one: silently ignoring it would deploy a different API than the file
// appears to ask for.
func TestLoadConfig_SQLNotPerEnvironment(t *testing.T) {
	_, err := loadSQLConfig(t, `
environments:
  production:
    database:
      url: postgres://prod/app
    sql:
      relations:
        include: [viewer]
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "environments.production.sql")
	assert.Contains(t, err.Error(), "top level")
}

// The overlay keeps the top-level sql section for every environment.
func TestForEnvironment_KeepsSQL(t *testing.T) {
	cfg, err := loadSQLConfig(t, `
sql:
  relations:
    include: [viewer]
environments:
  production:
    database:
      url: postgres://prod/app
`)
	require.NoError(t, err)
	resolved, err := cfg.ForEnvironment("production")
	require.NoError(t, err)
	assert.Equal(t, []string{"viewer"}, resolved.SQL.Relations.Include)
}

func TestSQLConfig_Exposure(t *testing.T) {
	cfg := SQLConfig{
		Relations: SQLRelationsConfig{Include: []string{"document#viewer"}, Exclude: []string{"owner"}},
		Functions: allFamilies(true),
	}
	cfg.Functions.ListSubjects = false
	cfg.Functions.Explain = false
	assert.Equal(t, compiler.Exposure{
		Relations: compiler.RelationPatterns{Include: []string{"document#viewer"}, Exclude: []string{"owner"}},
		Functions: compiler.FunctionFamilies{ListSubjects: off(), Explain: off()},
	}, cfg.Exposure())

	all := SQLConfig{Functions: allFamilies(false)}.Exposure()
	assert.Equal(t, compiler.FunctionFamilies{
		ListObjects: off(), ListSubjects: off(), Accessible: off(), ParentKey: off(), Expand: off(), Explain: off(),
	}, all.Functions)
}

// ParseConfig reads content as LoadConfig reads a file: defaults, variables
// and validation included.
func TestParseConfig(t *testing.T) {
	t.Setenv("MELANGE_SQL_FUNCTIONS_EXPAND", "false")
	cfg, err := ParseConfig([]byte("sql:\n  relations:\n    include: [viewer]\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"viewer"}, cfg.SQL.Relations.Include)
	assert.False(t, cfg.SQL.Functions.Expand)
	assert.True(t, cfg.SQL.Functions.Explain)

	cfg, err = ParseConfig(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, cfg.SQL.Relations.Include)

	_, err = ParseConfig([]byte("sql:\n  relations:\n    include: ['a b']\n"))
	assert.ErrorContains(t, err, "sql.relations.include")
	_, err = ParseConfig([]byte("sql: [unclosed\n"))
	assert.Error(t, err)
}

// ConfigSearchPaths follows findConfigFile's order from a directory up to the
// repository root.
func TestConfigSearchPaths(t *testing.T) {
	assert.Equal(t, configFileNames, ConfigSearchPaths(""))
	paths := ConfigSearchPaths("a/b/")
	n := len(configFileNames)
	require.Len(t, paths, 3*n)
	assert.Equal(t, "a/b/melange.yaml", paths[0])
	assert.Equal(t, "a/melange/config.yaml", paths[n+2])
	assert.Equal(t, "melange/melange.yml", paths[3*n-1])
}
