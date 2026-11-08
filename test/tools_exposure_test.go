package test

import (
	"context"
	"database/sql"
	"errors"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
)

// The runtime, studio and the explain/expand CLI against a database migrated
// with a restricted exposure. exposureSchema (migrator_exposure_test.go):
// exposing document#editor installs editor and owner, which editor reads,
// and leaves viewer and auditor out.

const notExposedViewer = `relation "viewer" on type "document" is not exposed`

// exposedToolsDB migrates exposureSchema with exposure and adds tuples:
// alice owns document 1, bob views it, carol audits it.
func exposedToolsDB(t *testing.T, exposure *migrator.Exposure) (*sql.DB, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db, dsn := newTuplesDBWithDSN(t)
	_, err := migrateExposure(t, db, writeSchema(t, exposureSchema), exposure, false)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `INSERT INTO melange_tuples VALUES
		('user', 'alice', 'owner', 'document', '1'),
		('user', 'bob', 'viewer', 'document', '1'),
		('user', 'carol', 'auditor', 'document', '1')`)
	require.NoError(t, err)
	return db, dsn
}

// explainExpandOff is editorOnly with the explain and expand families off.
func explainExpandOff() *migrator.Exposure {
	e := editorOnly()
	e.Functions = compiler.FunctionFamilies{Explain: new(bool), Expand: new(bool)}
	return e
}

// requireValidation asserts err is the ValidationError M2003 maps to.
func requireValidation(t *testing.T, err error, msg string) {
	t.Helper()
	var ve *melange.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, melange.ErrorCodeValidation, ve.Code)
	assert.Equal(t, msg, ve.Message)
}

// The runtime maps M2003 to a ValidationError on every entry point, through
// lib/pq and pgx alike, and answers for the exposed relation.
func TestRuntime_NotExposedIsValidationError(t *testing.T) {
	db, dsn := exposedToolsDB(t, editorOnly())
	pq, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pq.Close() })
	ctx := context.Background()
	alice, doc := melange.Object{Type: "user", ID: "alice"}, melange.Object{Type: "document", ID: "1"}
	tuples := []melange.ContextualTuple{{Subject: melange.Object{Type: "user", ID: "dave"}, Relation: "editor", Object: doc}}

	for driver, conn := range map[string]*sql.DB{"pgx": db, "lib/pq": pq} {
		t.Run(driver, func(t *testing.T) {
			c := melange.NewChecker(conn)
			ok, err := c.Check(ctx, alice, melange.Relation("editor"), doc)
			require.NoError(t, err)
			assert.True(t, ok, "editor is exposed and alice owns the document")
			ids, err := c.ListObjectsAll(ctx, alice, melange.Relation("editor"), "document")
			require.NoError(t, err)
			assert.Equal(t, []string{"1"}, ids)

			viewer := melange.Relation("viewer")
			calls := map[string]func() error{
				"Check": func() error { _, err := c.Check(ctx, alice, viewer, doc); return err },
				"CheckWithContextualTuples": func() error {
					_, err := c.CheckWithContextualTuples(ctx, alice, viewer, doc, tuples)
					return err
				},
				"BulkCheck": func() error {
					_, err := c.NewBulkCheck(ctx).Add(alice, melange.Relation("editor"), doc).Add(alice, viewer, doc).Execute()
					return err
				},
				"ListObjects": func() error {
					_, _, err := c.ListObjects(ctx, alice, viewer, "document", melange.PageOptions{})
					return err
				},
				"ListObjectsWithContextualTuples": func() error {
					_, _, err := c.ListObjectsWithContextualTuples(ctx, alice, viewer, "document", tuples, melange.PageOptions{})
					return err
				},
				"ListSubjects": func() error { _, _, err := c.ListSubjects(ctx, doc, viewer, "user", melange.PageOptions{}); return err },
				"ListSubjectsWithContextualTuples": func() error {
					_, _, err := c.ListSubjectsWithContextualTuples(ctx, doc, viewer, "user", tuples, melange.PageOptions{})
					return err
				},
				"Explain": func() error { _, err := c.Explain(ctx, alice, viewer, doc); return err },
				"Expand":  func() error { _, err := c.Expand(ctx, doc, viewer); return err },
			}
			for name, call := range calls {
				t.Run(name, func(t *testing.T) { requireValidation(t, call(), notExposedViewer) })
			}

			// Expand answers for the relations editor's pointers lead to.
			users, err := c.ExpandRecursive(ctx, doc, melange.Relation("editor"))
			require.NoError(t, err)
			assert.Equal(t, []string{"user:alice"}, users)
		})
	}
}

// With the explain and expand families off, their public functions raise
// for every relation; with the expand function gone, Expand reports
// ErrMissingFunction.
func TestRuntime_FamilyOffAndMissingExpand(t *testing.T) {
	db, _ := exposedToolsDB(t, explainExpandOff())
	ctx := context.Background()
	c := melange.NewChecker(db)
	alice, doc := melange.Object{Type: "user", ID: "alice"}, melange.Object{Type: "document", ID: "1"}
	_, err := c.Explain(ctx, alice, melange.Relation("editor"), doc)
	requireValidation(t, err, `relation "editor" on type "document" is not exposed`)
	_, err = c.Expand(ctx, doc, melange.Relation("editor"))
	requireValidation(t, err, `relation "editor" on type "document" is not exposed`)

	_, err = db.ExecContext(ctx, `DROP FUNCTION expand_permission`)
	require.NoError(t, err)
	_, err = c.Expand(ctx, doc, melange.Relation("editor"))
	assert.True(t, errors.Is(err, melange.ErrMissingFunction), "%v", err)
}

// Studio under a restricted recorded exposure labels what the SQL API does
// not answer for and does not call it.
func TestStudio_RestrictedExposure(t *testing.T) {
	_, dsn := exposedToolsDB(t, editorOnly())
	c := newStudio(t, openStudioDB(t, dsn), "public")

	t.Run("matrix", func(t *testing.T) {
		got := flat(c.on(t).query(url.Values{"mode": {"matrix"}, "subject": {"user:alice"}, "object": {"document:1"}}))
		assert.Contains(t, got, "1 of 4 relations allowed", "only editor is checked: alice is its owner")
		assert.Contains(t, got, "3 not exposed")
		assert.NotContains(t, got, `class="error"`)
		assert.NotContains(t, got, "M2003")
		assert.Equal(t, 1, strings.Count(got, `hx-post="/explain"`), "only editor explains")
	})
	t.Run("explain an unexposed relation", func(t *testing.T) {
		got := flat(c.on(t).query(url.Values{"mode": {"explain"}, "subject": {"user:bob"}, "relation": {"viewer"}, "object": {"document:1"}}))
		assert.Contains(t, got, "document#viewer is not exposed")
		assert.Contains(t, got, "--include-relation document#viewer")
		assert.NotContains(t, got, "M2003")
	})
	t.Run("explain an exposed relation", func(t *testing.T) {
		got := flat(c.on(t).query(url.Values{"mode": {"explain"}, "subject": {"user:alice"}, "relation": {"editor"}, "object": {"document:1"}}))
		assert.Contains(t, got, "allowed")
		assert.NotContains(t, got, `class="error"`)
	})
	t.Run("analyze an unexposed relation", func(t *testing.T) {
		got := flat(c.on(t).query(url.Values{"mode": {"analyze"}, "subject": {"user:bob"}, "relation": {"viewer"}, "object": {"document:1"}}))
		assert.Contains(t, got, "document#viewer is not exposed")
	})
	t.Run("expand and drill into an installed but unexposed pointer", func(t *testing.T) {
		got := flat(c.on(t).query(url.Values{"mode": {"expand"}, "relation": {"editor"}, "object": {"document:1"}}))
		assert.NotContains(t, got, `class="error"`)
		assert.Contains(t, got, `/expand?userset=document%3A1%23owner`, "owner is in editor's expand closure")
		got = flat(c.on(t).get("/expand?" + url.Values{"userset": {"document:1#owner"}}.Encode()))
		assert.Contains(t, got, "user:alice")
		assert.NotContains(t, got, `class="error"`)
	})
	t.Run("expand an unexposed relation", func(t *testing.T) {
		got := flat(c.on(t).get("/expand?" + url.Values{"userset": {"document:1#viewer"}}.Encode()))
		assert.Contains(t, got, "document#viewer is not exposed")
		assert.NotContains(t, got, "M2003")
	})
	t.Run("entity access lists skip unexposed relations", func(t *testing.T) {
		got := flat(c.on(t).get("/entity/group?" + url.Values{"type": {"user"}, "id": {"alice"}, "tab": {"can"}, "group": {"document"}}.Encode()))
		assert.Contains(t, got, "Not listed:")
		assert.Contains(t, got, "<code>viewer</code>")
		assert.NotContains(t, got, `class="error"`)
		assert.Contains(t, got, ">editor<", "editor is listed for alice")
		got = flat(c.on(t).get("/entity/group?" + url.Values{"type": {"document"}, "id": {"1"}, "tab": {"by"}, "group": {"user"}}.Encode()))
		assert.Contains(t, got, "Not listed:")
		assert.NotContains(t, got, `class="error"`)
	})
	t.Run("relation page", func(t *testing.T) {
		got := flat(c.on(t).get("/relation?type=document&relation=owner"))
		assert.Contains(t, got, "Not answered by the SQL API")
		assert.Contains(t, got, "check: not exposed")
		assert.NotContains(t, got, "expand: not exposed", "expand answers for owner")
		got = flat(c.on(t).get("/relation?type=document&relation=editor"))
		assert.NotContains(t, got, "Not answered by the SQL API")
	})
}

// With explain and expand off, studio says they are not installed instead
// of calling them, and the entity tabs keep listing.
func TestStudio_FamiliesOff(t *testing.T) {
	_, dsn := exposedToolsDB(t, explainExpandOff())
	c := newStudio(t, openStudioDB(t, dsn), "public")
	got := flat(c.query(url.Values{"mode": {"explain"}, "subject": {"user:alice"}, "relation": {"editor"}, "object": {"document:1"}}))
	assert.Contains(t, got, "the explain functions are not installed")
	got = flat(c.query(url.Values{"mode": {"expand"}, "relation": {"editor"}, "object": {"document:1"}}))
	assert.Contains(t, got, "the expand functions are not installed")
	got = flat(c.query(url.Values{"mode": {"matrix"}, "subject": {"user:alice"}, "object": {"document:1"}}))
	assert.NotContains(t, got, `hx-post="/explain"`, "no relation can be explained")
	assert.Contains(t, got, `class="chip off"`)
	got = flat(c.get("/entity/group?" + url.Values{"type": {"user"}, "id": {"alice"}, "tab": {"can"}, "group": {"document"}}.Encode()))
	assert.Contains(t, got, `class="chip off"`, "listed relations cannot be explained")
	assert.NotContains(t, got, `hx-post="/explain"`)
}

// Under the default exposure, and against a migration table from before the
// exposure column, studio shows no exposure labels and answers as before.
func TestStudio_DefaultAndLegacyExposure(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "legacy table"}[legacy], func(t *testing.T) {
			db, dsn := exposedToolsDB(t, nil)
			if legacy {
				_, err := db.ExecContext(context.Background(), `ALTER TABLE melange_migrations DROP COLUMN exposure`)
				require.NoError(t, err)
			}
			c := newStudio(t, openStudioDB(t, dsn), "public")
			got := flat(c.on(t).query(url.Values{"mode": {"matrix"}, "subject": {"user:alice"}, "object": {"document:1"}}))
			assert.Contains(t, got, "3 of 4 relations allowed", "owner, editor and viewer; not auditor")
			assert.Equal(t, 4, strings.Count(got, `hx-post="/explain"`))
			got += flat(c.on(t).query(url.Values{"mode": {"explain"}, "subject": {"user:bob"}, "relation": {"viewer"}, "object": {"document:1"}}))
			got += flat(c.on(t).query(url.Values{"mode": {"expand"}, "relation": {"viewer"}, "object": {"document:1"}}))
			got += flat(c.on(t).get("/entity/group?" + url.Values{"type": {"user"}, "id": {"alice"}, "tab": {"can"}, "group": {"document"}}.Encode()))
			got += flat(c.on(t).get("/relation?type=document&relation=viewer"))
			for _, label := range []string{"not exposed", "not installed", "Not listed", "Not answered", `class="error"`, `chip off`} {
				assert.NotContains(t, got, label)
			}
			assert.Contains(t, got, `/expand?userset=document%3A1%23editor`)
		})
	}
}

// flat unescapes a page and collapses its whitespace.
func flat(body string) string {
	return strings.Join(strings.Fields(html.UnescapeString(body)), " ")
}

// melange explain and expand, built from the root module against the
// published runtime, name the relation and how to expose it instead of
// surfacing the database error.
func TestCLI_ExplainExpandUnderRestrictedExposure(t *testing.T) {
	db, dsn := exposedToolsDB(t, editorOnly())
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	fails := func(t *testing.T, want []string, args ...string) {
		t.Helper()
		out, stderr, err := cli.exec(dir, append(args, "--db", dsn)...)
		require.Error(t, err, "melange %v: %s", args, out)
		for _, w := range want {
			assert.Contains(t, stderr, w)
		}
		for _, raw := range []string{"M2003", "42883", "pq:", "SQLSTATE"} {
			assert.NotContains(t, stderr, raw)
		}
	}
	works := func(t *testing.T, args ...string) string {
		t.Helper()
		out, stderr, err := cli.exec(dir, append(args, "--db", dsn)...)
		require.NoError(t, err, "melange %v: %s", args, stderr)
		return out
	}

	t.Run("unexposed relation", func(t *testing.T) {
		want := []string{"document#viewer is not exposed", "sql.relations.include", "--include-relation document#viewer", "melange migrate"}
		fails(t, want, "explain", "user:bob", "viewer", "document:1")
		fails(t, want, "expand", "document:1", "viewer")
		fails(t, want, "expand", "document:1", "viewer", "--recursive")
	})
	t.Run("exposed relation and its expand closure", func(t *testing.T) {
		assert.Contains(t, works(t, "explain", "user:alice", "editor", "document:1", "--format", "json"), `"result": true`)
		assert.Equal(t, "user:alice\n", works(t, "expand", "document:1", "editor", "--recursive"))
		assert.Contains(t, works(t, "expand", "document:1", "owner", "--flatten"), "user:alice", "owner is installed and expand answers for it")
	})
	t.Run("no recorded model falls back to the SQLSTATE", func(t *testing.T) {
		_, err := db.ExecContext(context.Background(), `UPDATE melange_migrations SET model_json = '{}'`)
		require.NoError(t, err)
		fails(t, []string{"document#viewer is not exposed", "--include-relation document#viewer"}, "explain", "user:bob", "viewer", "document:1")
	})
}

// A 42883 is reported as the public function missing only when it names
// that function, so the database error for any other missing function, or
// for the function in the wrong schema, still says what is missing.
func TestCLI_ExpandMissingFunction(t *testing.T) {
	db, dsn := exposedToolsDB(t, nil)
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	ctx := context.Background()

	_, err := db.ExecContext(ctx, `CREATE SCHEMA wrong`)
	require.NoError(t, err)
	_, stderr, err := cli.exec(dir, "expand", "document:1", "editor", "--db-schema", "wrong", "--db", dsn)
	require.Error(t, err)
	assert.Contains(t, stderr, "wrong.expand_permission is not installed")
	assert.Contains(t, stderr, "--db-schema")

	_, err = db.ExecContext(ctx, `DROP FUNCTION expand_document_editor`)
	require.NoError(t, err)
	_, stderr, err = cli.exec(dir, "expand", "document:1", "editor", "--db", dsn)
	require.Error(t, err)
	assert.Contains(t, stderr, "expand_document_editor")
	assert.NotContains(t, stderr, "expand_permission is not installed")
}

func TestCLI_ExplainExpandFamiliesOff(t *testing.T) {
	_, dsn := exposedToolsDB(t, explainExpandOff())
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	for family, args := range map[string][]string{
		"explain": {"explain", "user:alice", "editor", "document:1"},
		"expand":  {"expand", "document:1", "editor"},
	} {
		t.Run(family, func(t *testing.T) {
			_, stderr, err := cli.exec(dir, append(args, "--db", dsn)...)
			require.Error(t, err)
			assert.Contains(t, stderr, "the "+family+" functions are not installed")
			assert.Contains(t, stderr, "does not answer for document#editor")
			assert.Contains(t, stderr, "sql.functions."+family+" off")
			assert.NotContains(t, stderr, "M2003")
		})
	}
	// The dispatcher is generated with the family off and keeps the
	// unknown-pair answer for a relation the model does not define.
	out, stderr, err := cli.exec(dir, "explain", "user:alice", "nosuchrel", "document:1", "--format", "json", "--db", dsn)
	require.NoError(t, err, stderr)
	assert.Contains(t, out, `"result": false`)
}

// writeConfigFile writes a melange.yaml into dir.
func writeConfigFile(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "melange.yaml"), []byte(content), 0o644))
}

// End to end: `melange migrate` applies a restricted sql section from
// melange.yaml, and explain and expand, run from the same project, follow it.
func TestCLI_MigrateRestrictedThenExplainExpand(t *testing.T) {
	_, dsn := exposedToolsDB(t, nil)
	cli := buildMelangeCLI(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schema.fga"), []byte(exposureSchema), 0o644))
	writeConfigFile(t, dir, "schema: schema.fga\ndatabase:\n  url: "+dsn+"\nsql:\n  relations:\n    include: [document#editor]\n  functions:\n    explain: false\n")
	cli.runIn(t, dir, "migrate")

	_, stderr, err := cli.exec(dir, "explain", "user:alice", "editor", "document:1")
	require.Error(t, err)
	assert.Contains(t, stderr, "the explain functions are not installed")
	_, stderr, err = cli.exec(dir, "expand", "document:1", "viewer")
	require.Error(t, err)
	assert.Contains(t, stderr, "document#viewer is not exposed")
	assert.Equal(t, "user:alice\n", cli.runIn(t, dir, "expand", "document:1", "editor", "--recursive"))
}
