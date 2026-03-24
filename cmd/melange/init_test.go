package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pthm/melange/lib/cli"
)

// readInitConfig unmarshals the generated YAML into our initConfig struct,
// which uses json tags matching what writeConfig produces.
// cli.Config uses mapstructure tags (for viper), so we use initConfig for test assertions.
func readInitConfig(t *testing.T, path string) initConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var c initConfig
	require.NoError(t, yaml.Unmarshal(data, &c))
	return c
}

// chdir changes to the given directory and returns a cleanup function.
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// resetInitFlags resets all package-level init flags to zero values.
func resetInitFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		initYes = false
		initNoInstall = false
		initSchema = ""
		initDB = ""
		initTemplate = ""
		initRuntime = ""
		initOutput = ""
		initPackage = ""
		initIDType = ""
		initMigrationStrategy = ""
		initMigrationOutput = ""
		initMigrationFormat = ""
		initMigrationName = ""
	})
}

// --- Project detection ---

func TestDetectProject_GoMod(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0o644))
	chdir(t, dir)

	proj := detectProject()
	assert.True(t, proj.goMod)
	assert.False(t, proj.pkgJSON)
	assert.Equal(t, "go", proj.runtime)
}

func TestDetectProject_PackageJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0o644))
	chdir(t, dir)

	proj := detectProject()
	assert.False(t, proj.goMod)
	assert.True(t, proj.pkgJSON)
	assert.Equal(t, "typescript", proj.runtime)
}

func TestDetectProject_BothGoAndTS_PrefersGo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0o644))
	chdir(t, dir)

	proj := detectProject()
	assert.True(t, proj.goMod)
	assert.True(t, proj.pkgJSON)
	assert.Equal(t, "go", proj.runtime)
}

func TestDetectProject_NoProjectFiles(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	proj := detectProject()
	assert.False(t, proj.goMod)
	assert.False(t, proj.pkgJSON)
	assert.Empty(t, proj.runtime)
}

// --- Default answers based on detection ---

func TestDefaultAnswers_GoProject(t *testing.T) {
	a := defaultAnswers(detectedProject{runtime: "go", goMod: true})
	assert.True(t, a.GenerateCode)
	assert.Equal(t, "go", a.ClientRuntime)
	assert.Equal(t, "internal/authz", a.ClientOutput)
	assert.Equal(t, "authz", a.ClientPackage)
}

func TestDefaultAnswers_TypeScriptProject(t *testing.T) {
	a := defaultAnswers(detectedProject{runtime: "typescript", pkgJSON: true})
	assert.True(t, a.GenerateCode)
	assert.Equal(t, "typescript", a.ClientRuntime)
	assert.Equal(t, "src/authz", a.ClientOutput)
}

func TestDefaultAnswers_NoProject(t *testing.T) {
	a := defaultAnswers(detectedProject{})
	assert.False(t, a.GenerateCode)
	assert.Equal(t, "go", a.ClientRuntime)            // fallback default
	assert.Equal(t, "internal/authz", a.ClientOutput) // go default
}

// --- Config path selection ---

func TestConfigPathForSchema_MelangeDir(t *testing.T) {
	assert.Equal(t, filepath.Join("melange", "config.yaml"), configPathForSchema("melange/schema.fga"))
	assert.Equal(t, filepath.Join("melange", "config.yaml"), configPathForSchema("melange/models/auth.fga"))
}

func TestConfigPathForSchema_RootLevel(t *testing.T) {
	assert.Equal(t, "melange.yaml", configPathForSchema("schemas/schema.fga"))
	assert.Equal(t, "melange.yaml", configPathForSchema("schema.fga"))
}

// --- Write config ---

func TestWriteConfig_NoClientGen(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	a := &initAnswers{
		SchemaPath:   "melange/schema.fga",
		DatabaseURL:  "postgres://localhost:5432/mydb",
		GenerateCode: false,
	}

	require.NoError(t, writeConfig(filepath.Join("melange", "config.yaml"), a))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	assert.Equal(t, "melange/schema.fga", c.Schema)
	assert.Equal(t, "postgres://localhost:5432/mydb", c.Database.URL)
	assert.Nil(t, c.Generate, "generate block should be absent")
}

func TestWriteConfig_WithGoClient(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	a := &initAnswers{
		SchemaPath:    "melange/schema.fga",
		DatabaseURL:   "postgres://localhost:5432/app",
		GenerateCode:  true,
		ClientRuntime: "go",
		ClientOutput:  "internal/authz",
		ClientPackage: "authz",
		ClientIDType:  "string",
	}

	configPath := filepath.Join("melange", "config.yaml")
	require.NoError(t, writeConfig(configPath, a))

	c := readInitConfig(t, filepath.Join(dir, configPath))
	assert.Equal(t, "go", c.Generate.Client.Runtime)
	assert.Equal(t, "internal/authz", c.Generate.Client.Output)
	assert.Equal(t, "authz", c.Generate.Client.Package)
	assert.Equal(t, "string", c.Generate.Client.IDType)
}

func TestWriteConfig_WithTypeScriptClient(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	a := &initAnswers{
		SchemaPath:    "melange/schema.fga",
		DatabaseURL:   "postgres://localhost:5432/app",
		GenerateCode:  true,
		ClientRuntime: "typescript",
		ClientOutput:  "src/authz",
		ClientIDType:  "string",
	}

	configPath := filepath.Join("melange", "config.yaml")
	require.NoError(t, writeConfig(configPath, a))

	c := readInitConfig(t, filepath.Join(dir, configPath))
	assert.Equal(t, "typescript", c.Generate.Client.Runtime)
	assert.Equal(t, "src/authz", c.Generate.Client.Output)
	assert.Empty(t, c.Generate.Client.Package, "package should be omitted for typescript")
}

// --- Write schema ---

func TestWriteSchema_AllTemplates(t *testing.T) {
	for name := range schemaTemplates {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			chdir(t, dir)

			schemaPath := filepath.Join("melange", "schema.fga")
			require.NoError(t, writeSchema(schemaPath, name))

			data, err := os.ReadFile(filepath.Join(dir, schemaPath))
			require.NoError(t, err)

			content := string(data)
			assert.Contains(t, content, "model")
			assert.Contains(t, content, "schema 1.1")
			assert.Contains(t, content, "type user")
		})
	}
}

func TestWriteSchema_UnknownTemplate(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	err := writeSchema("schema.fga", "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown template")
}

func TestWriteSchema_OrgRBACHasFullModel(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	require.NoError(t, writeSchema("schema.fga", "org-rbac"))

	data, err := os.ReadFile(filepath.Join(dir, "schema.fga"))
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "type organization")
	assert.Contains(t, content, "type repository")
	assert.Contains(t, content, "define can_read")
}

func TestWriteSchema_DocSharingHasDocumentType(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	require.NoError(t, writeSchema("schema.fga", "doc-sharing"))

	data, err := os.ReadFile(filepath.Join(dir, "schema.fga"))
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, "type document")
	assert.Contains(t, content, "define viewer")
}

// --- Find existing config ---

func TestFindExistingConfig_RootYaml(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile("melange.yaml", []byte("schema: s.fga"), 0o644))

	assert.Equal(t, "melange.yaml", findExistingConfig())
}

func TestFindExistingConfig_MelangeDir(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.MkdirAll("melange", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("melange", "config.yaml"), []byte("schema: s.fga"), 0o644))

	assert.Equal(t, filepath.Join("melange", "config.yaml"), findExistingConfig())
}

func TestFindExistingConfig_None(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	assert.Empty(t, findExistingConfig())
}

// --- Package manager detection ---

func TestDetectPkgManager_Bun(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile("bun.lockb", nil, 0o644))

	mgr, args := detectPkgManager()
	assert.Equal(t, "bun", mgr)
	assert.Equal(t, []string{"add"}, args)
}

func TestDetectPkgManager_BunLock(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile("bun.lock", nil, 0o644))

	mgr, _ := detectPkgManager()
	assert.Equal(t, "bun", mgr)
}

func TestDetectPkgManager_Pnpm(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile("pnpm-lock.yaml", nil, 0o644))

	mgr, args := detectPkgManager()
	assert.Equal(t, "pnpm", mgr)
	assert.Equal(t, []string{"add"}, args)
}

func TestDetectPkgManager_Yarn(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	require.NoError(t, os.WriteFile("yarn.lock", nil, 0o644))

	mgr, args := detectPkgManager()
	assert.Equal(t, "yarn", mgr)
	assert.Equal(t, []string{"add"}, args)
}

func TestDetectPkgManager_DefaultNpm(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	mgr, args := detectPkgManager()
	assert.Equal(t, "npm", mgr)
	assert.Equal(t, []string{"install"}, args)
}

// --- Full init flow (non-interactive) ---

// setupInitTest creates a temp directory, changes into it, resets all init
// flags, and sets initYes + initNoInstall so the test runs non-interactively.
// It returns the temp directory path.
func setupInitTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)
	resetInitFlags(t)
	initYes = true
	initNoInstall = true
	return dir
}

func TestRunInit_Defaults_NoProject(t *testing.T) {
	dir := setupInitTest(t)

	require.NoError(t, runInit(nil, nil))

	// Config created in melange/ dir
	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	assert.Equal(t, "melange/schema.fga", c.Schema)
	assert.Equal(t, "postgres://localhost:5432/mydb", c.Database.URL)
	assert.Nil(t, c.Generate, "no generate block without project detection")

	// Schema created
	schemaData, err := os.ReadFile(filepath.Join(dir, "melange", "schema.fga"))
	require.NoError(t, err)
	assert.Contains(t, string(schemaData), "type organization")
}

func TestRunInit_Defaults_GoProject(t *testing.T) {
	dir := setupInitTest(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test"), 0o644))

	require.NoError(t, runInit(nil, nil))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	assert.Equal(t, "go", c.Generate.Client.Runtime)
	assert.Equal(t, "internal/authz", c.Generate.Client.Output)
	assert.Equal(t, "authz", c.Generate.Client.Package)
}

func TestRunInit_Defaults_TypeScriptProject(t *testing.T) {
	dir := setupInitTest(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0o644))

	require.NoError(t, runInit(nil, nil))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	assert.Equal(t, "typescript", c.Generate.Client.Runtime)
	assert.Equal(t, "src/authz", c.Generate.Client.Output)
}

func TestRunInit_FlagOverrides(t *testing.T) {
	dir := setupInitTest(t)
	initSchema = "custom/auth.fga"
	initDB = "postgres://prod:5432/app"
	initTemplate = "minimal"
	initRuntime = "go"
	initOutput = "pkg/perms"
	initPackage = "perms"
	initIDType = "int64"

	require.NoError(t, runInit(nil, nil))

	// Config path should be melange.yaml since schema is not under melange/
	c := readInitConfig(t, filepath.Join(dir, "melange.yaml"))
	assert.Equal(t, "custom/auth.fga", c.Schema)
	assert.Equal(t, "postgres://prod:5432/app", c.Database.URL)
	assert.Equal(t, "go", c.Generate.Client.Runtime)
	assert.Equal(t, "pkg/perms", c.Generate.Client.Output)
	assert.Equal(t, "perms", c.Generate.Client.Package)
	assert.Equal(t, "int64", c.Generate.Client.IDType)

	// Schema should be minimal
	schemaData, err := os.ReadFile(filepath.Join(dir, "custom", "auth.fga"))
	require.NoError(t, err)
	content := string(schemaData)
	assert.Contains(t, content, "type user")
	assert.NotContains(t, content, "type organization", "minimal template should not have org")
}

func TestRunInit_TemplateNone_SkipsSchema(t *testing.T) {
	dir := setupInitTest(t)
	initTemplate = "none"

	require.NoError(t, runInit(nil, nil))

	// Config exists
	_, err := os.Stat(filepath.Join(dir, "melange", "config.yaml"))
	require.NoError(t, err)

	// Schema does not exist
	_, err = os.Stat(filepath.Join(dir, "melange", "schema.fga"))
	assert.True(t, os.IsNotExist(err), "schema file should not be created with template=none")
}

func TestRunInit_ExistingConfig_Errors(t *testing.T) {
	setupInitTest(t)

	// Create existing config
	require.NoError(t, os.MkdirAll("melange", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("melange", "config.yaml"), []byte("schema: old.fga"), 0o644))

	err := runInit(nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestRunInit_ExistingSchema_SkipsInNonInteractive(t *testing.T) {
	dir := setupInitTest(t)

	// Pre-create the schema file but not the config
	require.NoError(t, os.MkdirAll("melange", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("melange", "schema.fga"), []byte("original content"), 0o644))

	require.NoError(t, runInit(nil, nil))

	// Schema should NOT be overwritten
	data, err := os.ReadFile(filepath.Join(dir, "melange", "schema.fga"))
	require.NoError(t, err)
	assert.Equal(t, "original content", string(data))
}

func TestRunInit_ConfigDiscoverable_AfterInit(t *testing.T) {
	dir := setupInitTest(t)

	require.NoError(t, runInit(nil, nil))

	// The generated config should be loadable by the config system
	cfg, configPath, err := cli.LoadConfig(filepath.Join(dir, "melange", "config.yaml"))
	require.NoError(t, err)
	assert.NotEmpty(t, configPath)
	assert.Equal(t, "melange/schema.fga", cfg.Schema)
	assert.Equal(t, "postgres://localhost:5432/mydb", cfg.Database.URL)
}

func TestRunInit_SchemaValidatable_AfterInit(t *testing.T) {
	dir := setupInitTest(t)

	require.NoError(t, runInit(nil, nil))

	// Verify the schema file can be read and looks like valid FGA
	data, err := os.ReadFile(filepath.Join(dir, "melange", "schema.fga"))
	require.NoError(t, err)

	content := string(data)
	assert.NotEmpty(t, content)
	// All templates start with "model\n  schema 1.1"
	assert.Contains(t, content, "model\n  schema 1.1")
}

func TestRunInit_CreatesParentDirectories(t *testing.T) {
	dir := setupInitTest(t)
	initSchema = "deep/nested/dir/schema.fga"

	require.NoError(t, runInit(nil, nil))

	// Schema in nested dir
	_, err := os.Stat(filepath.Join(dir, "deep", "nested", "dir", "schema.fga"))
	require.NoError(t, err)

	// Config at root (schema not under melange/)
	_, err = os.Stat(filepath.Join(dir, "melange.yaml"))
	require.NoError(t, err)
}

// --- Migration strategy defaults ---

func TestDefaultAnswers_MigrationStrategy_Builtin(t *testing.T) {
	a := defaultAnswers(detectedProject{})
	assert.Equal(t, "builtin", a.MigrationStrategy)
	assert.Equal(t, "migrations/", a.MigrationOutput)
	assert.Equal(t, "split", a.MigrationFormat)
	assert.Equal(t, "melange", a.MigrationName)
}

// --- Write config: migration strategy ---

func TestWriteConfig_BuiltinMigration_NoMigrationBlock(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	a := &initAnswers{
		SchemaPath:        "melange/schema.fga",
		DatabaseURL:       "postgres://localhost:5432/mydb",
		GenerateCode:      false,
		MigrationStrategy: "builtin",
	}

	require.NoError(t, writeConfig(filepath.Join("melange", "config.yaml"), a))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	assert.Nil(t, c.Generate, "generate block should be absent for builtin migration without client gen")
}

func TestWriteConfig_VersionedMigration(t *testing.T) {
	tests := []struct {
		name   string
		output string
		format string
		suffix string
	}{
		{"split format", "migrations/", "split", "melange"},
		{"single format", "db/migrations/", "single", "authz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			chdir(t, dir)

			a := &initAnswers{
				SchemaPath:        "melange/schema.fga",
				DatabaseURL:       "postgres://localhost:5432/mydb",
				GenerateCode:      false,
				MigrationStrategy: "versioned",
				MigrationOutput:   tt.output,
				MigrationFormat:   tt.format,
				MigrationName:     tt.suffix,
			}

			configPath := filepath.Join("melange", "config.yaml")
			require.NoError(t, writeConfig(configPath, a))

			c := readInitConfig(t, filepath.Join(dir, configPath))
			require.NotNil(t, c.Generate, "generate block should be present for versioned migration")
			assert.Nil(t, c.Generate.Client, "client block should be absent when not requested")
			require.NotNil(t, c.Generate.Migration, "migration block should be present")
			assert.Equal(t, tt.output, c.Generate.Migration.Output)
			assert.Equal(t, tt.format, c.Generate.Migration.Format)
			assert.Equal(t, tt.suffix, c.Generate.Migration.Name)
		})
	}
}

func TestWriteConfig_VersionedMigration_WithClientGen(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	a := &initAnswers{
		SchemaPath:        "melange/schema.fga",
		DatabaseURL:       "postgres://localhost:5432/mydb",
		GenerateCode:      true,
		ClientRuntime:     "go",
		ClientOutput:      "internal/authz",
		ClientPackage:     "authz",
		ClientIDType:      "string",
		MigrationStrategy: "versioned",
		MigrationOutput:   "migrations/",
		MigrationFormat:   "split",
		MigrationName:     "melange",
	}

	configPath := filepath.Join("melange", "config.yaml")
	require.NoError(t, writeConfig(configPath, a))

	c := readInitConfig(t, filepath.Join(dir, configPath))
	require.NotNil(t, c.Generate, "generate block should be present")
	require.NotNil(t, c.Generate.Client, "client block should be present")
	assert.Equal(t, "go", c.Generate.Client.Runtime)
	require.NotNil(t, c.Generate.Migration, "migration block should be present")
	assert.Equal(t, "migrations/", c.Generate.Migration.Output)
}

// --- Full init flow: migration strategy ---

func TestRunInit_VersionedMigration_Flag(t *testing.T) {
	dir := setupInitTest(t)
	initMigrationStrategy = "versioned"

	require.NoError(t, runInit(nil, nil))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	require.NotNil(t, c.Generate, "generate block should be present for versioned strategy")
	require.NotNil(t, c.Generate.Migration, "migration block should be present")
	assert.Equal(t, "migrations/", c.Generate.Migration.Output)
	assert.Equal(t, "split", c.Generate.Migration.Format)
	assert.Equal(t, "melange", c.Generate.Migration.Name)
}

func TestRunInit_VersionedMigration_CustomFlags(t *testing.T) {
	dir := setupInitTest(t)
	initMigrationStrategy = "versioned"
	initMigrationOutput = "db/migrate/"
	initMigrationFormat = "single"
	initMigrationName = "authz"

	require.NoError(t, runInit(nil, nil))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	require.NotNil(t, c.Generate.Migration)
	assert.Equal(t, "db/migrate/", c.Generate.Migration.Output)
	assert.Equal(t, "single", c.Generate.Migration.Format)
	assert.Equal(t, "authz", c.Generate.Migration.Name)
}

func TestRunInit_VersionedMigration_WithGoClient(t *testing.T) {
	dir := setupInitTest(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test"), 0o644))
	initMigrationStrategy = "versioned"

	require.NoError(t, runInit(nil, nil))

	c := readInitConfig(t, filepath.Join(dir, "melange", "config.yaml"))
	require.NotNil(t, c.Generate)
	require.NotNil(t, c.Generate.Client, "client should be auto-configured for Go project")
	assert.Equal(t, "go", c.Generate.Client.Runtime)
	require.NotNil(t, c.Generate.Migration, "migration should also be configured")
	assert.Equal(t, "migrations/", c.Generate.Migration.Output)
}

func TestRunInit_VersionedMigration_ConfigDiscoverable(t *testing.T) {
	dir := setupInitTest(t)
	initMigrationStrategy = "versioned"
	initMigrationOutput = "migrations/"

	require.NoError(t, runInit(nil, nil))

	// The generated config should be loadable by the config system
	cfg, configPath, err := cli.LoadConfig(filepath.Join(dir, "melange", "config.yaml"))
	require.NoError(t, err)
	assert.NotEmpty(t, configPath)
	assert.Equal(t, "migrations/", cfg.Generate.Migration.Output)
	assert.Equal(t, "split", cfg.Generate.Migration.Format)
	assert.Equal(t, "melange", cfg.Generate.Migration.Name)
}
