package command

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const clientSchema = `model
  schema 1.1

type user

type folder
  relations
    define viewer: [user]

type document
  relations
    define parent: [folder]
    define owner: [user]
    define editor: [user] or owner
    define can_view: viewer from parent
    define can_edit: editor
`

var (
	relConstant        = regexp.MustCompile(`(?m)^\tRel\w+\s+melange\.Relation = "([^"]+)"$`)
	accessibleConstant = regexp.MustCompile(`(?m)^\t(SQLAccessible\w+)\s*= "([^"]+)"$`)
)

// generatedClient runs generate client for Go with config as melange.yaml
// (none when empty) and returns the relations with constants and the
// accessible function constants, as identifier=name.
func generatedClient(t *testing.T, config string, args ...string) (relations, accessible []string) {
	t.Helper()
	out, err := runCLIWithFiles(t, config, map[string]string{"schema.fga": clientSchema},
		append([]string{"generate", "client", "--runtime", "go", "--schema", "schema.fga"}, args...)...)
	require.NoError(t, err)
	for _, m := range relConstant.FindAllStringSubmatch(out, -1) {
		relations = append(relations, m[1])
	}
	for _, m := range accessibleConstant.FindAllStringSubmatch(out, -1) {
		accessible = append(accessible, m[1]+"="+m[2])
	}
	slices.Sort(relations)
	return relations, accessible
}

func TestCLI_GenerateClientRelations(t *testing.T) {
	all := []string{"can_edit", "can_view", "editor", "owner", "parent", "viewer"}
	canView := []string{"SQLAccessibleDocumentCanView=accessible_document_can_view", "SQLAccessibleDocumentCanViewByParent=accessible_document_can_view_by_parent"}
	tests := []struct {
		name       string
		config     string
		env        map[string]string
		args       []string
		relations  []string
		accessible []string // nil: not checked
	}{
		{
			name:      "no config: every relation and accessible function",
			relations: all,
			accessible: []string{
				"SQLAccessibleDocumentCanEdit=accessible_document_can_edit",
				"SQLAccessibleDocumentCanView=accessible_document_can_view",
				"SQLAccessibleDocumentEditor=accessible_document_editor",
				"SQLAccessibleDocumentOwner=accessible_document_owner",
				"SQLAccessibleDocumentParent=accessible_document_parent",
				"SQLAccessibleFolderViewer=accessible_folder_viewer",
				"SQLAccessibleDocumentCanViewByParent=accessible_document_can_view_by_parent",
			},
		},
		{
			name:       "inherits sql.relations",
			config:     "sql:\n  relations:\n    include: [document#can_view]\n",
			relations:  []string{"can_view"},
			accessible: canView,
		},
		{
			name:      "own selector replaces sql.relations; names follow what migrate installs",
			config:    "sql:\n  relations:\n    include: [document#can_view]\ngenerate:\n  client:\n    relations:\n      include: ['can_*']\n",
			relations: []string{"can_edit", "can_view"},
			// can_edit is not exposed by sql.relations, so it has no
			// accessible function name.
			accessible: canView,
		},
		{
			name:      "own exclude alone narrows the inherited sql.relations",
			config:    "sql:\n  relations:\n    include: ['can_*', owner, editor]\n    exclude: [editor]\ngenerate:\n  client:\n    relations:\n      exclude: [can_edit]\n",
			relations: []string{"can_view", "owner"},
		},
		{
			name:      "env over file",
			config:    "generate:\n  client:\n    relations:\n      include: ['can_*']\n",
			env:       map[string]string{"MELANGE_GENERATE_CLIENT_RELATIONS_INCLUDE": "owner, editor"},
			relations: []string{"editor", "owner"},
		},
		{
			name:      "flag over env",
			env:       map[string]string{"MELANGE_GENERATE_CLIENT_RELATIONS_INCLUDE": "owner"},
			args:      []string{"--include-relation", "document#can_edit", "--include-relation", "editor"},
			relations: []string{"can_edit", "editor"},
			accessible: []string{
				"SQLAccessibleDocumentCanEdit=accessible_document_can_edit",
				"SQLAccessibleDocumentEditor=accessible_document_editor",
			},
		},
		{
			name:      "exclude flag",
			args:      []string{"--exclude-relation", "can_*"},
			relations: []string{"editor", "owner", "parent", "viewer"},
		},
		{
			name:      "families off in sql.functions",
			config:    "sql:\n  relations:\n    include: ['can_*']\n  functions:\n    accessible: false\n",
			relations: []string{"can_edit", "can_view"},
			accessible: []string{
				"SQLAccessibleDocumentCanViewByParent=accessible_document_can_view_by_parent",
			},
		},
		{
			name:       "parent_key off from the environment",
			config:     "sql:\n  relations:\n    include: ['can_*']\n",
			env:        map[string]string{"MELANGE_SQL_FUNCTIONS_PARENT_KEY": "false"},
			relations:  []string{"can_edit", "can_view"},
			accessible: []string{"SQLAccessibleDocumentCanEdit=accessible_document_can_edit", "SQLAccessibleDocumentCanView=accessible_document_can_view"},
		},
		{
			name:       "deprecated filter in the file narrows after the selector",
			config:     "sql:\n  relations:\n    include: [can_view, editor]\ngenerate:\n  client:\n    filter: can_\n",
			relations:  []string{"can_view"},
			accessible: canView,
		},
		{
			name:       "deprecated --filter flag",
			args:       []string{"--filter", "can_v"},
			relations:  []string{"can_view"},
			accessible: canView,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			relations, accessible := generatedClient(t, tc.config, tc.args...)
			assert.Equal(t, tc.relations, relations)
			if tc.accessible != nil {
				assert.Equal(t, tc.accessible, accessible)
			}
		})
	}
}

func TestCLI_GenerateClientRelationsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		config string
		env    map[string]string
		args   []string
		want   []string
	}{
		{
			name:   "inherit is not a value",
			config: "generate:\n  client:\n    relations: inherit\n",
			want:   []string{"generate.client.relations", "left out to inherit"},
		},
		{
			name:   "unknown key",
			config: "generate:\n  client:\n    relations:\n      includes: [can_view]\n",
			want:   []string{"generate.client.relations.includes", "unknown key"},
		},
		{
			name:   "bad pattern in the file",
			config: "generate:\n  client:\n    relations:\n      include: ['a#b#c']\n",
			want:   []string{"generate.client.relations.include", "more than one '#'"},
		},
		{
			name: "bad pattern from the environment",
			env:  map[string]string{"MELANGE_GENERATE_CLIENT_RELATIONS_EXCLUDE": "doc#"},
			want: []string{"generate.client.relations.exclude (from MELANGE_GENERATE_CLIENT_RELATIONS_EXCLUDE)"},
		},
		{
			name: "bad flag",
			args: []string{"--include-relation", "a b"},
			want: []string{"--include-relation"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := runCLIWithFiles(t, tc.config, map[string]string{"schema.fga": clientSchema},
				append([]string{"generate", "client", "--runtime", "go", "--schema", "schema.fga"}, tc.args...)...)
			require.Error(t, err)
			for _, want := range tc.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TypeScript output follows the same selection.
func TestCLI_GenerateClientTypeScript(t *testing.T) {
	seedDir(t, "sql:\n  relations:\n    include: [document#can_view]\n", map[string]string{"schema.fga": clientSchema})
	_, err := executeCLI(t, "generate", "client", "--runtime", "typescript", "--schema", "schema.fga", "--output", "out")
	require.NoError(t, err)
	read := func(path string) string {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		return string(content)
	}
	sql := read("out/sql.ts")
	assert.Contains(t, sql, "can_view: \"accessible_document_can_view\",")
	assert.Contains(t, sql, "parent: \"accessible_document_can_view_by_parent\",")
	assert.NotContains(t, sql, "can_edit")
	types := read("out/types.ts")
	assert.Contains(t, types, "CanView: \"can_view\"")
	assert.NotContains(t, types, "CanEdit")
}
