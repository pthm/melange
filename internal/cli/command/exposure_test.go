package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

func falsePtr() *bool {
	b := false
	return &b
}

// exposureFor loads config (none when empty) as melange.yaml, parses args as
// cmd's flags and resolves the exposure cmd would apply.
func exposureFor(t *testing.T, cmd *cobra.Command, config string, args ...string) (compiler.Exposure, error) {
	t.Helper()
	t.Chdir(tempDir(t))
	if config != "" {
		writeFile(t, "melange.yaml", config)
	}
	loaded, _, err := cli.LoadConfig("")
	require.NoError(t, err)
	resetFlags(cmd)
	t.Cleanup(func() { resetFlags(cmd) })
	require.NoError(t, cmd.ParseFlags(args))
	return resolveExposure(cmd, loaded.SQL)
}

const restrictedConfig = `
sql:
  relations:
    include: [viewer]
    exclude: [document#viewer]
  functions:
    expand: false
`

// Every command that generates or compares the SQL API takes the exposure
// from flag > MELANGE_SQL_* > config file > default, a flag replacing its key.
func TestResolveExposure_Precedence(t *testing.T) {
	for _, cmd := range []*cobra.Command{migrateCmd, generateMigrationCmd, doctorCmd, statusCmd} {
		t.Run(cmd.Name(), func(t *testing.T) {
			t.Run("default", func(t *testing.T) {
				got, err := exposureFor(t, cmd, "")
				require.NoError(t, err)
				assert.True(t, got.IsDefault(), "%+v", got)
			})

			t.Run("config file", func(t *testing.T) {
				got, err := exposureFor(t, cmd, restrictedConfig)
				require.NoError(t, err)
				assert.Equal(t, compiler.Exposure{
					Relations: compiler.RelationPatterns{Include: []string{"viewer"}, Exclude: []string{"document#viewer"}},
					Functions: compiler.FunctionFamilies{Expand: falsePtr()},
				}, got)
			})

			t.Run("env over file", func(t *testing.T) {
				t.Setenv("MELANGE_SQL_RELATIONS_INCLUDE", "editor,owner")
				t.Setenv("MELANGE_SQL_FUNCTIONS_EXPLAIN", "false")
				got, err := exposureFor(t, cmd, restrictedConfig)
				require.NoError(t, err)
				assert.Equal(t, []string{"editor", "owner"}, got.Relations.Include)
				assert.Equal(t, []string{"document#viewer"}, got.Relations.Exclude)
				assert.Equal(t, compiler.FunctionFamilies{Expand: falsePtr(), Explain: falsePtr()}, got.Functions)
			})

			t.Run("flags over env", func(t *testing.T) {
				t.Setenv("MELANGE_SQL_RELATIONS_INCLUDE", "editor")
				got, err := exposureFor(t, cmd, restrictedConfig,
					"--include-relation", "owner", "--include-relation", "folder#can_*",
					"--exclude-relation", "folder#can_admin",
					"--no-list-objects", "--no-list-subjects", "--no-accessible", "--no-parent-key", "--no-explain",
					"--no-expand=false")
				require.NoError(t, err)
				assert.Equal(t, compiler.Exposure{
					Relations: compiler.RelationPatterns{
						Include: []string{"owner", "folder#can_*"},
						Exclude: []string{"folder#can_admin"},
					},
					Functions: compiler.FunctionFamilies{
						ListObjects: falsePtr(), ListSubjects: falsePtr(), Accessible: falsePtr(),
						ParentKey: falsePtr(), Explain: falsePtr(),
					},
				}, got, "--no-expand=false re-enables a family the file disables")
			})

			t.Run("a flag replaces only its key", func(t *testing.T) {
				got, err := exposureFor(t, cmd, restrictedConfig, "--exclude-relation", "owner")
				require.NoError(t, err)
				assert.Equal(t, []string{"viewer"}, got.Relations.Include)
				assert.Equal(t, []string{"owner"}, got.Relations.Exclude)
			})

			t.Run("invalid flag pattern", func(t *testing.T) {
				_, err := exposureFor(t, cmd, "", "--include-relation", "a#b#c")
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--include-relation")
				assert.Contains(t, err.Error(), `"a#b#c"`)
			})
		})
	}
}

// An invalid pattern in the file fails every command at load, naming the key.
func TestCLI_InvalidSQLPatternFailsAtLoad(t *testing.T) {
	_, err := runCLI(t, "sql:\n  relations:\n    include: [\"document#\"]\n", "validate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sql.relations.include")
	assert.Contains(t, err.Error(), `"document#"`)
}

func TestCLI_ConfigShow_SQL(t *testing.T) {
	out, err := runCLI(t, restrictedConfig, "config", "show")
	require.NoError(t, err)
	for _, want := range []string{"SQL:", "Relations:", "Include:", "- viewer", "Exclude:", "- document#viewer", "Expand: false", "Explain: true"} {
		assert.Contains(t, out, want)
	}

	out, err = runCLI(t, "schema: s.fga\n", "config", "show")
	require.NoError(t, err)
	assert.Contains(t, out, "Include:\n    - '*'", "the defaults are shown")
	assert.Contains(t, out, "ListSubjects: true")
}

const (
	exposureSchemaV1 = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
`
	exposureSchemaV2 = exposureSchemaV1 + "    define auditor: [user]\n"
	editorConfig     = "schema: schema.fga\nsql:\n  relations:\n    include: [document#editor]\n"
)

// fn is how generated SQL creates a function in the public schema.
func fn(name string) string { return `FUNCTION "public"."` + name + `"(` }

// gitRepoAt creates a git repository holding files in one commit and chdirs
// into it.
func gitRepoAt(t *testing.T, files map[string]string) {
	t.Helper()
	gitRepoWithSchema(t, "schema.fga", exposureSchemaV1)
	for name, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
		writeFile(t, name, content)
		gitRun(t, "add", name)
	}
	gitRun(t, "commit", "--allow-empty", "-m", "config")
}

// generateUp runs `generate migration --up` with args.
func generateUp(t *testing.T, args ...string) string {
	t.Helper()
	out, err := executeCLI(t, append([]string{"generate", "migration", "--schema", "schema.fga", "--up"}, args...)...)
	require.NoError(t, err)
	return out
}

// legacyGitRefUp is the UP that `generate migration --git-ref` produced
// before the exposure existed: both schemas compiled with the default API
// and change detection against the previous one.
func legacyGitRefUp(t *testing.T, previous, current, ref string) string {
	t.Helper()
	compile := func(content string) ([]schema.TypeDefinition, compiler.GeneratedSQL, compiler.ListGeneratedSQL, []string, []compiler.NamedFunction) {
		types, err := parser.ParseRecordedSchemaString(content)
		require.NoError(t, err)
		closure := schema.ComputeRelationClosure(types)
		analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closure))
		inline := compiler.BuildInlineSQLData(closure, analyses)
		check, err := compiler.GenerateSQL(analyses, inline, "public")
		require.NoError(t, err)
		list, err := compiler.GenerateListSQL(analyses, inline, "public")
		require.NoError(t, err)
		return types, check, list, compiler.CollectFunctionNames(analyses), compiler.CollectNamedFunctions(check, list, analyses)
	}
	prevTypes, _, _, prevNames, prevNamed := compile(previous)
	types, check, list, names, named := compile(current)
	result := compiler.GenerateMigrationSQL(check, list, names, compiler.MigrationOptions{
		DatabaseSchema:        "public",
		Version:               version.Version,
		SchemaChecksum:        migrator.ComputeSchemaChecksum(current),
		CodegenVersion:        migrator.CodegenVersion(),
		NamedFunctions:        named,
		PreviousFunctionNames: prevNames,
		PreviousChecksums:     migrator.ComputeFunctionChecksums(prevNamed),
		PreviousSource:        "git:" + ref,
	})
	return semanticDiffHeader(schema.Diff(prevTypes, types)) + result.Up
}

// With the default exposure and a config file readable at the ref, or no
// config file at all, --git-ref output is unchanged: only the changed
// functions, byte for byte what it was before the exposure existed.
func TestGenerateMigration_GitRefDefaultExposureUnchanged(t *testing.T) {
	for name, config := range map[string]string{
		"config without sql section": "schema: schema.fga\n",
		"no config file":             "",
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			if config != "" {
				files["melange.yaml"] = config
			}
			gitRepoAt(t, files)
			writeFile(t, "schema.fga", exposureSchemaV2)

			up := generateUp(t, "--git-ref", "HEAD")
			assert.Equal(t, legacyGitRefUp(t, exposureSchemaV1, exposureSchemaV2, "HEAD"), up)
			assert.Contains(t, up, "-- Changed functions: ")
			assert.NotContains(t, up, "Previous exposure unknown")
		})
	}
}

// A restricted sql section at the ref is the previous exposure: the same
// one now compares function by function, and widening back to the default
// emits the functions the restricted one left out.
func TestGenerateMigration_GitRefReadsExposureAtRef(t *testing.T) {
	t.Run("unchanged restricted exposure", func(t *testing.T) {
		gitRepoAt(t, map[string]string{"melange.yaml": editorConfig})
		up := generateUp(t, "--git-ref", "HEAD")
		assert.Contains(t, up, `-- Exposure: {"relations":{"include":["document#editor"]}`)
		assert.Contains(t, up, "-- Changed functions: 0 of ")
		assert.NotContains(t, up, "Previous exposure unknown")
		assert.NotContains(t, up, "DROP FUNCTION", "nothing was removed")
	})

	t.Run("section removed widens", func(t *testing.T) {
		gitRepoAt(t, map[string]string{"melange.yaml": editorConfig})
		writeFile(t, "melange.yaml", "schema: schema.fga\n")
		up := generateUp(t, "--git-ref", "HEAD")
		assert.NotContains(t, up, "-- Exposure:")
		assert.Contains(t, up, "-- Changed functions: ")
		assert.Contains(t, up, fn("check_document_viewer"), "the function the restricted exposure left out is created")
		assert.NotContains(t, up, fn("check_document_editor"), "functions both exposures install are unchanged")
	})

	// With no config file in use the ref is searched the way discovery
	// searches the working tree, so deleting a restricted file widens.
	for _, name := range []string{"melange.yaml", "melange/config.yaml"} {
		t.Run("config file "+name+" deleted widens", func(t *testing.T) {
			gitRepoAt(t, map[string]string{name: editorConfig})
			gitRun(t, "rm", "-q", name)
			up := generateUp(t, "--git-ref", "HEAD")
			assert.NotContains(t, up, "-- Exposure:")
			assert.NotContains(t, up, "Previous exposure unknown")
			assert.Contains(t, up, fn("check_document_viewer"), "the function the restricted exposure left out is created")
			assert.NotContains(t, up, fn("check_document_editor"), "functions both exposures install are unchanged")
		})
	}

	t.Run("narrowed drops the de-exposed functions", func(t *testing.T) {
		gitRepoAt(t, map[string]string{"melange.yaml": "schema: schema.fga\n"})
		writeFile(t, "melange.yaml", editorConfig)
		up := generateUp(t, "--git-ref", "HEAD")
		assert.Contains(t, up, "-- Changed functions: ")
		assert.Contains(t, up, "DROP FUNCTION IF EXISTS \"public\".\"check_document_viewer\"")
	})

	t.Run("flags are not read at the ref", func(t *testing.T) {
		gitRepoAt(t, map[string]string{"melange.yaml": "schema: schema.fga\n"})
		up := generateUp(t, "--git-ref", "HEAD", "--include-relation", "document#editor")
		assert.Contains(t, up, `-- Exposure: {"relations":{"include":["document#editor"]}`)
		assert.Contains(t, up, "DROP FUNCTION IF EXISTS \"public\".\"check_document_viewer\"",
			"the ref's config is the default, so the flag narrows")
	})
}

// When the config file in use cannot be read at the ref, the previous
// exposure is unknown and every function is emitted, even under the default
// exposure, while orphans are still dropped.
func TestGenerateMigration_GitRefUnreadableConfigEmitsEverything(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"not tracked at the ref": func(t *testing.T) {
			gitRepoAt(t, nil)
			writeFile(t, "melange.yaml", "schema: schema.fga\n")
		},
		"invalid at the ref": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange.yaml": "sql:\n  relations:\n    include: [\"a#b#c\"]\n"})
			writeFile(t, "melange.yaml", "schema: schema.fga\n")
		},
		"not yaml at the ref": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange.yaml": "sql: [unclosed\n"})
			writeFile(t, "melange.yaml", "schema: schema.fga\n")
		},
		"invalid at the ref and deleted now": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange.yaml": "sql:\n  relations:\n    include: [\"a#b#c\"]\n"})
			gitRun(t, "rm", "-q", "melange.yaml")
		},
		"sql section in an environment at the ref": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange.yaml": "environments:\n  prod:\n    sql:\n      relations:\n        include: [viewer]\n"})
			writeFile(t, "melange.yaml", "schema: schema.fga\n")
		},
		// melange.yaml shadowed melange/config.yaml at the ref, so its
		// restricted section applied there, not the file in use now.
		"shadowed at the ref by a deleted file": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange.yaml": editorConfig, "melange/config.yaml": "schema: schema.fga\n"})
			gitRun(t, "rm", "-q", "melange.yaml")
		},
		"not the file discovery finds at the ref": func(t *testing.T) {
			gitRepoAt(t, map[string]string{"melange/config.yaml": editorConfig})
			writeFile(t, "melange.yaml", "schema: schema.fga\n")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			setup(t)
			writeFile(t, "schema.fga", strings.Replace(exposureSchemaV1, "    define viewer: [user] or editor\n", "", 1))
			up := generateUp(t, "--git-ref", "HEAD")
			assert.Contains(t, up, "-- Previous exposure unknown: every function is included")
			assert.NotContains(t, up, "-- Changed functions:")
			assert.Contains(t, up, fn("check_document_editor"), "unchanged functions are emitted")
			assert.Contains(t, up, "DROP FUNCTION IF EXISTS \"public\".\"check_document_viewer\"", "orphans are dropped")
			assert.Contains(t, up, "DROP FUNCTION IF EXISTS \"public\".\""+compiler.UnexposedFunctionName+"\"",
				"the helper only a restricted exposure installs is dropped")
		})
	}
}

// With --config the file read at the ref is that one, wherever discovery
// would look.
func TestGenerateMigration_GitRefExplicitConfig(t *testing.T) {
	gitRepoAt(t, map[string]string{"melange.yaml": "schema: schema.fga\n", "other.yaml": editorConfig})
	up := generateUp(t, "--config", "other.yaml", "--git-ref", "HEAD")
	assert.Contains(t, up, "-- Changed functions: 0 of ")
	assert.NotContains(t, up, "Previous exposure unknown")
}

// --previous-schema has no record of the exposure, so every function is
// emitted, under the default exposure too.
func TestGenerateMigration_PreviousSchemaEmitsEverything(t *testing.T) {
	seedDir(t, "", map[string]string{"schema.fga": exposureSchemaV2, "old.fga": exposureSchemaV1})
	up := generateUp(t, "--previous-schema", "old.fga")
	assert.Contains(t, up, "-- Previous state: file:old.fga")
	assert.Contains(t, up, "-- Previous exposure unknown: every function is included")
	assert.Contains(t, up, fn("check_document_editor"))
	assert.Contains(t, up, "DROP FUNCTION IF EXISTS \"public\".\""+compiler.UnexposedFunctionName+"\"",
		"the previous deployment may have installed the helper")

	up = generateUp(t, "--previous-schema", "old.fga", "--include-relation", "document#editor")
	assert.NotContains(t, up, "DROP FUNCTION IF EXISTS \"public\".\""+compiler.UnexposedFunctionName+"\"",
		"the current exposure installs the helper")
	assert.Contains(t, up, fn(compiler.UnexposedFunctionName))
}

// Without a comparison the output is the full SQL under the local exposure.
func TestGenerateMigration_FullModeUsesLocalExposure(t *testing.T) {
	seedDir(t, editorConfig, map[string]string{"schema.fga": exposureSchemaV1})
	up := generateUp(t)
	assert.Contains(t, up, `-- Exposure: {"relations":{"include":["document#editor"]}`)
	assert.NotContains(t, up, fn("check_document_viewer"))
	assert.Contains(t, up, fn(compiler.UnexposedFunctionName))

	up = generateUp(t, "--include-relation", "*")
	assert.NotContains(t, up, "-- Exposure:")
	assert.Contains(t, up, fn("check_document_viewer"))
}
