package command

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// functionsSchemaFile is the testdata schema, with a pull_request relation
// whose function names are truncated.
func functionsSchemaFile(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("../../../test/testutil/testdata/schema.fga")
	require.NoError(t, err)
	return map[string]string{"schema.fga": string(data)}
}

const longRelation = "pull_request#can_read_security_advisories_and_vulnerability_reports"

func TestCLI_FunctionsFileModeText(t *testing.T) {
	out, err := runCLIWithFiles(t, "schema: schema.fga\n", functionsSchemaFile(t), "functions")
	require.NoError(t, err)
	for _, want := range []string{
		"Source:    schema.fga (not checked against a database)",
		"Exposure:  default (every relation, every family)",
		"\norganization\n  admin  exposed\n",
		"check_organization_admin",
		"accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b",
		"parent_key",
		"(truncated)",
		"\ndispatchers\n",
		"check_permission_bulk",
		"not checked",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "\x1b[", "output to a pipe is plain text")
}

func TestCLI_FunctionsFileModeJSON(t *testing.T) {
	config := "schema: schema.fga\nsql:\n  relations:\n    include: [\"can_*\"]\n  functions:\n    explain: false\n"
	out, err := runCLIWithFiles(t, config, functionsSchemaFile(t), "functions", "--format", "json")
	require.NoError(t, err)
	var r sqlapi.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r))
	assert.Equal(t, "file", r.Source)
	assert.False(t, r.Checked)
	assert.Equal(t, []string{"can_*"}, r.Exposure.Relations.Include)
	assert.Empty(t, r.Orphans)

	pr := r.Find("pull_request", "can_read_security_advisories_and_vulnerability_reports")
	require.NotNil(t, pr)
	assert.True(t, pr.Exposed)
	names := map[sqlgen.FunctionKind]string{}
	for _, f := range pr.Functions {
		names[f.Kind] = f.Name
		assert.Equal(t, sqlapi.StatusNotChecked, f.Status)
	}
	assert.Equal(t, "accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c", names[sqlgen.KindAccessible])
	assert.Equal(t, "accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b", names[sqlgen.KindParentKey])
	assert.NotContains(t, names, sqlgen.KindExplain)
	assert.Contains(t, pr.NotInstalled, sqlgen.NotInstalled{
		Kind: sqlgen.KindExplain, Reason: sqlgen.ReasonOnDemand,
		Detail: "sql.functions.explain is off; the CLI and studio compile it on demand",
	})

	reader := r.Find("repository", "reader")
	require.NotNil(t, reader)
	assert.False(t, reader.Exposed, "reader is internal: only can_* relations are exposed")

	// The raw document carries the documented field names.
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	for _, key := range []string{"source", "database_schema", "melange_version", "checked", "exposure", "summary", "relations", "dispatchers"} {
		assert.Contains(t, doc, key)
	}
}

func TestCLI_FunctionsFilters(t *testing.T) {
	files := functionsSchemaFile(t)
	config := "schema: schema.fga\nsql:\n  relations:\n    include: [\"can_*\"]\n"
	out, err := runCLIWithFiles(t, config, files, "functions", "--format", "json", "--type", "issue", "--kind", "accessible", "--exposed")
	require.NoError(t, err)
	var r sqlapi.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r))
	require.NotEmpty(t, r.Relations)
	assert.Empty(t, r.Dispatchers)
	for _, rel := range r.Relations {
		assert.Equal(t, "issue", rel.Type)
		assert.True(t, rel.Exposed)
		for _, f := range rel.Functions {
			assert.Equal(t, sqlgen.KindAccessible, f.Kind)
		}
	}

	out, err = runCLIWithFiles(t, config, files, "functions", "--format", "json", "--internal", "--type", "repository")
	require.NoError(t, err)
	r = sqlapi.Report{}
	require.NoError(t, json.Unmarshal([]byte(out), &r))
	require.NotEmpty(t, r.Relations)
	for _, rel := range r.Relations {
		assert.False(t, rel.Exposed, rel.Relation)
	}

	out, err = runCLIWithFiles(t, config, files, "functions", "--kind", "dispatcher")
	require.NoError(t, err)
	assert.Contains(t, out, "check_permission_unexposed", "a restricted exposure installs the raising helper")
	assert.NotContains(t, out, "check_organization_admin")

	_, err = runCLIWithFiles(t, config, files, "functions", "--type", "nosuchtype")
	require.ErrorContains(t, err, `--type "nosuchtype"`)
}

func TestCLI_FunctionsShow(t *testing.T) {
	files := functionsSchemaFile(t)
	out, err := runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "show", longRelation)
	require.NoError(t, err)
	for _, want := range []string{
		longRelation + "  exposed",
		"Answered by: check_permission, check_permission_nw, check_permission_bulk",
		"Depends on:  repository#can_admin",
		"check_pull_req_can_read_security_advisories_and_vulner_d0edb52c\n",
		"truncated to 63 bytes",
		"signature:  check_pull_req_can_read_security_advisories_and_vulner_d0edb52c(p_subject_type text, p_subject_id text, p_object_id text, p_visited text[] DEFAULT ARRAY[]::text[]) RETURNS integer · LANGUAGE plpgsql",
		"example:    SELECT subject_id, next_cursor FROM list_pull_req_can_read_security_advisories_and_vul_d0edb52c_sub('1', 'user', 100, NULL);",
		"link:       repo → repository (reads can_admin)",
		"usage:      WHERE <repo column> IN (SELECT object_id FROM accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b('user', 'alice'))",
		"note:       Returns repository ids",
	} {
		assert.Contains(t, out, want)
	}

	// Dotted form, an exposure from flags, and the reasons.
	out, err = runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "show", "repository.reader",
		"--include-relation", "repository#can_read", "--no-list-subjects")
	require.NoError(t, err)
	assert.Contains(t, out, "repository#reader  internal (not exposed: modeling only; the public dispatchers not listed under Answered by raise M2003 for it)")
	assert.Contains(t, out, "Answered by: expand_permission")
	assert.Contains(t, out, "Not installed\n  list_subjects family off: sql.functions.list_subjects is off")

	// JSON has the one relation and no dispatchers.
	out, err = runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "show", "issue#can_read", "--format", "json")
	require.NoError(t, err)
	var r sqlapi.Report
	require.NoError(t, json.Unmarshal([]byte(out), &r))
	require.Len(t, r.Relations, 1)
	assert.Equal(t, "can_read", r.Relations[0].Relation)
	assert.Empty(t, r.Dispatchers)

	for arg, want := range map[string]string{
		"issue#nope":    "issue#nope is not a relation of the model (issue has",
		"nosuch#viewer": "nosuch#viewer is not a relation of the model (nosuch is not a type with relations)",
		"user#viewer":   "user#viewer is not a relation of the model (user is not a type with relations)",
		"nosuch":        "nosuch is not a relation of the model; use type#relation",
		"issue.nope":    "issue.nope is not a relation of the model",
	} {
		_, err := runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "show", arg)
		require.ErrorContains(t, err, want, arg)
	}
}

// Empty arrays are written as [], never null, so a consumer can iterate the
// documented arrays without a null check.
func TestCLI_FunctionsJSONEmptyArrays(t *testing.T) {
	files := functionsSchemaFile(t)
	out, err := runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "show", "issue#can_edit",
		"--include-relation", "repository#can_read", "--format", "json")
	require.NoError(t, err)
	var doc struct {
		Dispatchers json.RawMessage `json:"dispatchers"`
		Relations   []struct {
			Dispatchers json.RawMessage `json:"dispatchers"`
			Functions   json.RawMessage `json:"functions"`
		} `json:"relations"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &doc))
	assert.JSONEq(t, "[]", string(doc.Dispatchers))
	require.Len(t, doc.Relations, 1)
	assert.JSONEq(t, "[]", string(doc.Relations[0].Dispatchers), "an unreached relation has no dispatcher")
	assert.JSONEq(t, "[]", string(doc.Relations[0].Functions), "an unreached relation has no functions")

	out, err = runCLIWithFiles(t, "schema: schema.fga\n", files, "functions", "--format", "json", "--type", "organization", "--kind", "parent_key")
	require.NoError(t, err)
	var empty map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(out), &empty))
	assert.JSONEq(t, "[]", string(empty["relations"]), "a filter that matches nothing")
	assert.JSONEq(t, "[]", string(empty["dispatchers"]))
}

// A dotted argument is split where it names a relation, so a relation
// name with a dot resolves.
func TestCLI_FunctionsShowDotted(t *testing.T) {
	dsl := "model\n  schema 1.1\ntype user\ntype app\n  relations\n    define doc.viewer: [user]\n"
	out, err := runCLIWithFiles(t, "schema: schema.fga\n", map[string]string{"schema.fga": dsl}, "functions", "show", "app.doc.viewer")
	require.NoError(t, err)
	assert.Contains(t, out, "app#doc.viewer  exposed")
	assert.Contains(t, out, "check_app_doc_viewer")
}

// The text output of a checked report: statuses, overloads, orphans,
// unmanaged functions, collisions and notes.
func TestCLI_FunctionsCheckedText(t *testing.T) {
	r, err := sqlapi.Describe(mustParse(t, "model\n  schema 1.1\ntype user\ntype a_b\n  relations\n    define c: [user]\ntype a\n  relations\n    define b_c: [user]\n"), sqlgen.Exposure{}, "public")
	require.NoError(t, err)
	r.Source, r.Checked, r.BodiesChecked = "database", true, true
	for i := range r.Relations {
		for j := range r.Relations[i].Functions {
			r.Relations[i].Functions[j].Status = sqlapi.StatusInstalled
		}
	}
	r.Relations[0].Functions[0].Status = sqlapi.StatusMissing
	r.Relations[0].Functions[1].Status = sqlapi.StatusBodyChanged
	r.Relations[0].Functions[2].StaleOverloads = []string{"p_subject_type text, p_subject_id text"}
	r.Orphans = []sqlapi.InstalledFunction{{Name: "accessiblesk_a_b_c", Arguments: "p_subject_type text"}}
	r.Unmanaged = []sqlapi.InstalledFunction{{Name: "explain_invoice", Arguments: "p_id text"}}
	r.Summary = sqlapi.Summary{Functions: 10, Installed: 8, Missing: 1, BodyChanged: 1, StaleOverloads: 1, Orphans: 1}

	var buf bytes.Buffer
	printFunctionsText(&buf, r)
	out := buf.String()
	for _, want := range []string{
		"Source:    database schema public (deployed model)",
		"Status:    8 of 10 installed; 1 missing, 1 with a changed body, 1 stale overloads, 1 orphans",
		"missing",
		"body changed",
		"installed +1 stale overload",
		"Orphans (recorded by the last migration, no longer generated; melange migrate drops them):\n  accessiblesk_a_b_c(p_subject_type text)",
		"Unmanaged (named like generated functions, recorded by no migration; never dropped):\n  explain_invoice(p_id text)",
		"Warning: function names are generated for more than one relation:\n  check_a_b_c: ",
		"Note: ",
	} {
		assert.Contains(t, out, want)
	}
	assert.Less(t, strings.Index(out, "Note: "), strings.Index(out, "\na_b\n"), "notes come before the list")

	// Functions a newer melange migrated are not called droppable.
	r.CodegenAhead = true
	buf.Reset()
	printFunctionsText(&buf, r)
	assert.Contains(t, buf.String(), "Orphans (recorded by the last migration, run by a newer melange; this melange does not generate them, and its migrate leaves them):")
	assert.NotContains(t, buf.String(), "melange migrate drops them")
}

func mustParse(t *testing.T, src string) []schema.TypeDefinition {
	t.Helper()
	types, err := parser.ParseSchemaString(src)
	require.NoError(t, err)
	return types
}
