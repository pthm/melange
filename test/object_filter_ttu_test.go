package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/test/testutil"
)

// A pure TTU intersection: every operand of the AND is a tuple-to-userset
// check, so the group carries only ParentRelations and no Relations. Combined
// with a directly-assignable "folder" relation to filter on.
const pureTTUIntersectionFilterSchema = `
model
  schema 1.1

type user

type folder

type project
  relations
    define reader: [user]

type document
  relations
    define folder: [folder]
    define primary_project: [project]
    define secondary_project: [project]
    define can_view: reader from primary_project and reader from secondary_project
`

// TestObjectFilter_PureTTUIntersection checks that an object filter narrows a
// pure TTU intersection without loosening it.
//
// The filter is pushed into each INTERSECT part rather than applied to the
// group's result, because PostgreSQL will not push a qualifier through a set
// operation. That rewrite is only sound because filter(A INTERSECT B) equals
// filter(A) INTERSECT filter(B) — if it were applied as a union of filtered
// parts instead, an object satisfying only one operand would leak through.
// doc2 is the guard for exactly that: it is in the filtered folder and matches
// the primary project, but has no secondary project link.
func TestObjectFilter_PureTTUIntersection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.EmptyDB(t)
	ctx := context.Background()
	m := migrator.NewMigrator(db, "")
	createTuplesTable(t, ctx, db)

	_, err := db.ExecContext(ctx, `
		INSERT INTO melange_tuples VALUES
		 ('user','alice','reader','project','alpha'),
		 ('user','alice','reader','project','beta'),
		 -- doc1: both project links, in folder f1
		 ('project','alpha','primary_project','document','doc1'),
		 ('project','beta','secondary_project','document','doc1'),
		 ('folder','f1','folder','document','doc1'),
		 -- doc2: ONLY the primary link, in folder f1 — must fail the AND
		 ('project','alpha','primary_project','document','doc2'),
		 ('folder','f1','folder','document','doc2'),
		 -- doc3: both links, but in folder f2
		 ('project','alpha','primary_project','document','doc3'),
		 ('project','beta','secondary_project','document','doc3'),
		 ('folder','f2','folder','document','doc3')`)
	require.NoError(t, err)

	migrateSchema(t, ctx, m, pureTTUIntersectionFilterSchema,
		migrator.InternalMigrateOptions{Version: "v0.9.4"})

	list := func(filter any) []string {
		t.Helper()
		rows, err := db.QueryContext(ctx,
			`SELECT object_id FROM list_accessible_objects('user','alice','can_view','document',NULL,NULL,$1) ORDER BY 1`,
			filter)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var ids []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		require.NoError(t, rows.Err())
		return ids
	}

	assert.Equal(t, []string{"doc1", "doc3"}, list(nil),
		"unfiltered: doc2 satisfies only one operand of the AND")
	assert.Equal(t, []string{"doc1"}, list("folder@folder:f1"),
		"filtered: doc2 is in f1 but must still fail the AND")
	assert.Equal(t, []string{"doc3"}, list("folder@folder:f2"))

	// The filtered list must agree with check_permission object by object.
	for doc, want := range map[string]int{"doc1": 1, "doc2": 0, "doc3": 1} {
		var allowed int
		require.NoError(t, db.QueryRowContext(ctx,
			`SELECT check_permission('user','alice','can_view','document',$1)`, doc).Scan(&allowed))
		assert.Equal(t, want, allowed, "check_permission disagrees for %s", doc)
	}
}
