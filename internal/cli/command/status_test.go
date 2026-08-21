package command

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// model builds a one-type model whose viewer relation grants the given subject
// types, which is enough shape to produce every class of drift under test.
func model(subjectTypes ...string) []schema.TypeDefinition {
	rels := []schema.RelationDefinition{{Name: "viewer"}}
	for _, st := range subjectTypes {
		rels[0].SubjectTypeRefs = append(rels[0].SubjectTypeRefs, schema.SubjectTypeRef{Type: st})
	}
	return []schema.TypeDefinition{
		{Name: "user"},
		{Name: "document", Relations: rels},
	}
}

func TestSummarizeDrift_Additive(t *testing.T) {
	deployed := model("user")
	local := append(model("user"), schema.TypeDefinition{Name: "folder"})

	d := summarizeDrift(deployed, local)
	if d == nil {
		t.Fatal("expected drift detail for an added type")
	}
	if d.Breaking != 0 {
		t.Errorf("added type must not be breaking, got %d breaking", d.Breaking)
	}
	if d.Additive != 1 {
		t.Errorf("additive = %d, want 1", d.Additive)
	}
	if len(d.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(d.Changes))
	}
}

func TestSummarizeDrift_Breaking(t *testing.T) {
	deployed := append(model("user"), schema.TypeDefinition{Name: "folder"})
	local := model("user")

	d := summarizeDrift(deployed, local)
	if d == nil {
		t.Fatal("expected drift detail for a removed type")
	}
	if d.Breaking != 1 {
		t.Errorf("breaking = %d, want 1 (removing a type narrows access)", d.Breaking)
	}
}

// Equivalent models with different checksums are real: reformatting or a
// comment edit moves the checksum without changing behavior. Status must not
// invent changes to explain that.
func TestSummarizeDrift_EquivalentModelsReportNoDetail(t *testing.T) {
	if d := summarizeDrift(model("user"), model("user")); d != nil {
		t.Errorf("equivalent models should produce no drift detail, got %+v", d)
	}
}

func TestDeployedTypesFromRecord_PrefersModelJSON(t *testing.T) {
	encoded, err := schema.MarshalModel(model("user"))
	if err != nil {
		t.Fatalf("MarshalModel: %v", err)
	}
	// A DSL that would parse to something different, so preferring the JSON is
	// observable rather than merely plausible.
	rec := &migrator.MigrationRecord{
		SchemaDSL: "model\n  schema 1.1\ntype user\n",
		ModelJSON: encoded,
	}

	types, err := deployedTypesFromRecord(rec)
	if err != nil {
		t.Fatalf("deployedTypesFromRecord: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("types = %d, want 2 from model_json (not the 1-type DSL)", len(types))
	}
}

func TestDeployedTypesFromRecord_FallsBackToDSL(t *testing.T) {
	rec := &migrator.MigrationRecord{
		SchemaDSL: "model\n  schema 1.1\ntype user\n",
		ModelJSON: nil, // never written, or written empty
	}

	types, err := deployedTypesFromRecord(rec)
	if err != nil {
		t.Fatalf("deployedTypesFromRecord: %v", err)
	}
	if len(types) != 1 || types[0].Name != "user" {
		t.Fatalf("types = %+v, want the single type parsed from the DSL", types)
	}
}

func TestDeployedTypesFromRecord_PreStorageRecordIsNotAnError(t *testing.T) {
	types, err := deployedTypesFromRecord(&migrator.MigrationRecord{SchemaChecksum: "abc"})
	if err != nil {
		t.Fatalf("a record without a DSL must not error, got %v", err)
	}
	if types != nil {
		t.Errorf("types = %+v, want nil", types)
	}
}

func TestDeployedTypesFromRecord_CorruptDSLErrors(t *testing.T) {
	rec := &migrator.MigrationRecord{SchemaDSL: "this is not a schema"}
	if _, err := deployedTypesFromRecord(rec); err == nil {
		t.Error("expected an error for an unparseable recorded DSL")
	}
}

// The drift block is omitted from JSON when absent, so consumers that only
// check `sync` are unaffected by this addition.
func TestStatusReport_DriftOmittedWhenAbsent(t *testing.T) {
	out, err := json.Marshal(statusReport{Sync: syncInSync})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(out); got != `{"schema_file":"","tuples_view":"","sync":"in_sync"}` {
		t.Errorf("unexpected JSON: %s", got)
	}
}

// gitRun runs a git command in the current working directory, failing the test
// on error.
func gitRun(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// requireGit skips the test when git is unavailable — every probe test shells
// out to it.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// tempDir returns a temporary directory with symlinks resolved: on macOS
// t.TempDir() lives under /var, which is a link to /private/var, and `git
// rev-parse --show-toplevel` reports the resolved path — an unresolved cwd makes
// every repo-relative path wrong.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	return dir
}

// writeFile writes a file relative to the current working directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// gitRepoWithSchema creates a git repo containing schemaPath at each of the
// given contents, one commit per version, and chdirs into it. Returns the
// checksums in commit order.
func gitRepoWithSchema(t *testing.T, schemaPath string, versions ...string) []string {
	t.Helper()
	requireGit(t)
	t.Chdir(tempDir(t))
	// Isolate from the developer's global config: commit.gpgsign, hooks, or
	// core.autocrlf would otherwise fail or skew these tests.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	gitRun(t, "init")
	gitRun(t, "config", "user.email", "test@example.com")
	gitRun(t, "config", "user.name", "Test")

	checksums := make([]string, 0, len(versions))
	for i, content := range versions {
		writeFile(t, schemaPath, content)
		gitRun(t, "add", schemaPath)
		gitRun(t, "commit", "-m", fmt.Sprintf("version %d", i))
		checksums = append(checksums, migrator.ComputeSchemaChecksum(content))
	}
	return checksums
}

const (
	schemaV1 = "model\n  schema 1.1\ntype user\n"
	schemaV2 = "model\n  schema 1.1\ntype user\ntype folder\n"
)

// unknownChecksum is the checksum of a schema no test repo ever commits: the
// deployed model that this checkout cannot account for.
var unknownChecksum = migrator.ComputeSchemaChecksum("model\n  schema 1.1\ntype nobody_has_this\n")

// A deployed checksum matching an older committed version means the database is
// simply behind: ordinary drift, not "ahead".
func TestDatabaseAhead_DeployedVersionIsInHistory(t *testing.T) {
	checksums := gitRepoWithSchema(t, "schema.fga", schemaV1, schemaV2)

	if databaseAhead("schema.fga", checksums[0], maxHistoryRevisions) {
		t.Error("a deployed checksum found in git history is drift, not database_ahead")
	}
}

func TestDatabaseAhead_DeployedModelUnknownLocally(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1, schemaV2)

	if !databaseAhead("schema.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("a deployed checksum absent from git history should report database_ahead")
	}
}

// Outside a git repo the probe cannot answer, and must not accuse.
func TestDatabaseAhead_OutsideGitRepoStaysDrift(t *testing.T) {
	requireGit(t)
	t.Chdir(tempDir(t))
	writeFile(t, "schema.fga", schemaV1)

	if databaseAhead("schema.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("outside a git repo the probe must fall back to drift")
	}
}

// A modular schema's checksum spans the manifest and every module, which the
// single-path probe cannot reconstruct, so it never claims to know.
func TestDatabaseAhead_ModularSchemaStaysDrift(t *testing.T) {
	gitRepoWithSchema(t, "fga.mod", "schema: 1.2\ncontents:\n  - core.fga\n")

	if databaseAhead("fga.mod", unknownChecksum, maxHistoryRevisions) {
		t.Error("modular schemas must fall back to drift")
	}
}

// An uncommitted schema has no history to search; that is not evidence of a
// foreign deployment.
func TestDatabaseAhead_UncommittedSchemaStaysDrift(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	writeFile(t, "other.fga", schemaV2)

	if databaseAhead("other.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("an uncommitted schema must fall back to drift")
	}
}

func TestSyncDescription_CoversEveryState(t *testing.T) {
	for _, state := range []string{syncInSync, syncDrift, syncUnknown, syncDatabaseAhead} {
		if got := syncDescription(state); got == "" || got == "not recorded" {
			t.Errorf("syncDescription(%q) = %q, want a state-specific description", state, got)
		}
	}
	if got := syncDescription(syncNotRecorded); got != "not recorded" {
		t.Errorf("syncDescription(%q) = %q, want \"not recorded\"", syncNotRecorded, got)
	}
}

// A schema that was renamed still has its earlier versions in history; --follow
// finds them, so a deployment from before the rename is drift, not "ahead".
func TestDatabaseAhead_FollowsRenames(t *testing.T) {
	checksums := gitRepoWithSchema(t, "schema.fga", schemaV1)
	gitRun(t, "mv", "schema.fga", "authz.fga")
	// Change the content in the same commit as the rename: the pre-rename
	// checksum then exists only under the old path, so finding it proves the
	// search followed the rename rather than reading the current path.
	writeFile(t, "authz.fga", schemaV2)
	gitRun(t, "add", "-A")
	gitRun(t, "commit", "-m", "rename and extend schema")

	if databaseAhead("authz.fga", checksums[0], maxHistoryRevisions) {
		t.Error("a version committed under the old name must not report database_ahead")
	}
}

// More history than the window means the search could not be exhaustive: the
// deployed model may sit just past the cutoff, so the probe must not accuse.
func TestDatabaseAhead_TruncatedHistoryIsInconclusive(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1, schemaV2, schemaV1+"\n")

	if databaseAhead("schema.fga", unknownChecksum, 2) {
		t.Error("a history longer than the window must fall back to drift")
	}
	// The same checksum is conclusive once the window covers every revision.
	if !databaseAhead("schema.fga", unknownChecksum, 10) {
		t.Error("with the whole history searched, an unknown checksum is database_ahead")
	}
}

func TestParseRevisionPaths(t *testing.T) {
	const sha1 = "1111111111111111111111111111111111111111"
	const sha2 = "2222222222222222222222222222222222222222"
	out := sha1 + "\n\nauthz.fga\n" + sha2 + "\n\nschema.fga\n"

	got, commits := parseRevisionPaths(out)
	want := []revisionPath{{sha1, "authz.fga"}, {sha2, "schema.fga"}}
	if len(got) != len(want) {
		t.Fatalf("parsed %d revisions, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("revision %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if commits != 2 {
		t.Errorf("commits = %d, want 2", commits)
	}
}

// A commit with no path line (possible when git filters differently than
// expected) must not pair the next commit hash with a stale path — and must
// still count against the window, which truncation is judged on.
func TestParseRevisionPaths_SkipsCommitsWithoutPaths(t *testing.T) {
	const sha1 = "1111111111111111111111111111111111111111"
	const sha2 = "2222222222222222222222222222222222222222"

	got, commits := parseRevisionPaths(sha1 + "\n\n" + sha2 + "\n\nschema.fga\n")
	if len(got) != 1 || got[0].revision != sha2 {
		t.Errorf("got %+v, want only the commit that reported a path", got)
	}
	if commits != 2 {
		t.Errorf("commits = %d, want 2 (both commits consumed the window)", commits)
	}
}

// Migrating from a dirty working tree is routine in local development, so the
// deployed model may be an uncommitted version this developer applied. Git
// cannot see it either way; the probe must not read that as a foreign
// deployment.
func TestDatabaseAhead_UncommittedEditsAreInconclusive(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	writeFile(t, "schema.fga", schemaV2)

	if databaseAhead("schema.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("a modified working tree must fall back to drift")
	}
}

// Git pathspecs resolve against the current directory, while the schema path is
// repository-root-relative — so the probe must anchor its pathspec or it
// silently finds no history whenever melange runs from a subdirectory.
func TestDatabaseAhead_WorksFromASubdirectory(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	if err := os.Mkdir("services", 0o750); err != nil {
		t.Fatalf("creating subdirectory: %v", err)
	}
	t.Chdir("services")

	if !databaseAhead("../schema.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("the probe must find the schema's history when run from a subdirectory")
	}
}

// A shallow clone's history stops at the graft boundary and git reports that as
// success, so the revision count cannot detect it. CI checkouts are shallow by
// default — precisely where a false alarm would be believed.
func TestDatabaseAhead_ShallowCloneIsInconclusive(t *testing.T) {
	checksums := gitRepoWithSchema(t, "schema.fga", schemaV1, schemaV2)
	origin, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	shallow := filepath.Join(tempDir(t), "shallow")
	gitRun(t, "clone", "--depth", "1", "file://"+origin, shallow)
	t.Chdir(shallow)

	// checksums[0] is the first version, which the shallow clone cannot see.
	if databaseAhead("schema.fga", checksums[0], maxHistoryRevisions) {
		t.Error("a shallow clone cannot prove absence and must fall back to drift")
	}
}

// git C-quotes non-ASCII paths under the default core.quotepath, which makes
// every revision unreadable. The all-reads-failed guard keeps that from becoming
// an accusation, but it also makes the probe useless — so assert it still
// reaches a verdict, not merely that it stays quiet.
func TestDatabaseAhead_NonASCIISchemaPath(t *testing.T) {
	checksums := gitRepoWithSchema(t, "schéma.fga", schemaV1, schemaV2)

	if databaseAhead("schéma.fga", checksums[0], maxHistoryRevisions) {
		t.Error("a committed version under a non-ASCII path must be found")
	}
	if !databaseAhead("schéma.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("the probe must read revisions under a non-ASCII path, not give up on them")
	}
}

// End-of-line conversion means the blob and the working tree differ while git
// still reports the file clean; comparing raw blobs would mismatch every
// revision on a CRLF checkout.
func TestDatabaseAhead_EOLNormalizedSchema(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	gitRun(t, "config", "core.autocrlf", "true")
	writeFile(t, ".gitattributes", "*.fga text\n")
	gitRun(t, "add", ".gitattributes")
	gitRun(t, "commit", "-m", "normalize eol")

	// What a checkout under these settings produces is what melange checksums.
	deployed := migrator.ComputeSchemaChecksum(strings.ReplaceAll(schemaV1, "\n", "\r\n"))
	if databaseAhead("schema.fga", deployed, maxHistoryRevisions) {
		t.Error("an eol-normalized version of a committed schema must be found")
	}
}

// A record that predates model storage cannot be compared, which the caller
// must not read as "the models are equal" — that would suppress the
// database-ahead escalation.
func TestDriftDetail_PreStorageRecordIsNotEquivalent(t *testing.T) {
	var notes []string
	detail, equivalent := driftDetail(&migrator.MigrationRecord{SchemaChecksum: "abc"}, "schema.fga", &notes)
	if detail != nil || equivalent {
		t.Errorf("detail = %+v, equivalent = %v; want nil, false", detail, equivalent)
	}
	if len(notes) != 0 {
		t.Errorf("a pre-storage record needs no note (model_recorded covers it), got %v", notes)
	}
}

func TestDriftDetail_UnparseableRecordedDSLNotesAndIsNotEquivalent(t *testing.T) {
	var notes []string
	rec := &migrator.MigrationRecord{SchemaDSL: "not a schema"}
	detail, equivalent := driftDetail(rec, "schema.fga", &notes)
	if detail != nil || equivalent {
		t.Errorf("detail = %+v, equivalent = %v; want nil, false", detail, equivalent)
	}
	if len(notes) != 1 {
		t.Errorf("notes = %v, want one explaining the failure", notes)
	}
}

// Every revision failing to read is not proof of anything. A required smudge
// filter that exits non-zero reproduces it hermetically: `git log` still lists
// the revisions, but reading their content fails — the same shape as a blobless
// clone with no network.
func TestDatabaseAhead_UnreadableRevisionsAreInconclusive(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	writeFile(t, ".gitattributes", "*.fga filter=fail\n")
	gitRun(t, "add", ".gitattributes")
	gitRun(t, "-c", "filter.fail.clean=cat", "commit", "-m", "add filter attribute")
	// clean passes so the working tree still reads as unmodified; only reading a
	// committed revision back out fails.
	gitRun(t, "config", "filter.fail.clean", "cat")
	gitRun(t, "config", "filter.fail.smudge", "exit 1")
	gitRun(t, "config", "filter.fail.required", "true")

	if databaseAhead("schema.fga", unknownChecksum, maxHistoryRevisions) {
		t.Error("no revision could be read, so the search proves nothing and must fall back to drift")
	}
}

// A local schema that does not parse leaves the checksum-level drift verdict
// standing, with a note explaining why there is no detail.
func TestDriftDetail_UnparseableLocalSchemaNotes(t *testing.T) {
	dir := tempDir(t)
	t.Chdir(dir)
	writeFile(t, "broken.fga", "this is not a schema")

	encoded, err := schema.MarshalModel(model("user"))
	if err != nil {
		t.Fatalf("MarshalModel: %v", err)
	}

	var notes []string
	detail, equivalent := driftDetail(&migrator.MigrationRecord{
		SchemaDSL: "model\n  schema 1.1\ntype user\n",
		ModelJSON: encoded,
	}, "broken.fga", &notes)

	if detail != nil || equivalent {
		t.Errorf("detail = %+v, equivalent = %v; want nil, false", detail, equivalent)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "could not parse local schema") {
		t.Errorf("notes = %v, want one naming the parse failure", notes)
	}
}
