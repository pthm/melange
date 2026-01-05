package test

import (
	"context"
	"testing"

	"github.com/pthm/melange/schema"
	"github.com/pthm/melange/test/testutil"
	"github.com/pthm/melange/tooling"
	"github.com/stretchr/testify/require"
)

func TestSubtractDifferenceExclusionParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor but not owner)
`

	types, err := tooling.ParseSchemaString(dsl)
	require.NoError(t, err)

	var viewerRel *schema.RelationDefinition
	for i := range types {
		if types[i].Name != "document" {
			continue
		}
		for j := range types[i].Relations {
			if types[i].Relations[j].Name == "viewer" {
				viewerRel = &types[i].Relations[j]
				break
			}
		}
	}
	require.NotNil(t, viewerRel)
	require.Len(t, viewerRel.ExcludedIntersectionGroups, 1)
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Relations, []string{"editor"})
	require.Contains(t, viewerRel.ExcludedIntersectionGroups[0].Exclusions, "editor")
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Exclusions["editor"], []string{"owner"})
}

func TestSubtractDifferenceExclusionSQL(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor but not owner)
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	err := tooling.MigrateFromString(ctx, db, dsl)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT 'user'::text AS subject_type,
		       subject_id,
		       relation,
		       'document'::text AS object_type,
		       object_id
		FROM (VALUES
			('aardvark', 'writer', '1'),
			('aardvark', 'editor', '1'),
			('aardvark', 'owner', '1'),
			('badger', 'writer', '2'),
			('badger', 'editor', '2'),
			('cheetah', 'writer', '3'),
			('cheetah', 'owner', '3')
		) AS t(subject_id, relation, object_id)
	`)
	require.NoError(t, err)

	var allowed bool
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'aardvark', 'viewer', 'document', '1')`).Scan(&allowed)
	require.NoError(t, err)
	require.True(t, allowed, "aardvark should be allowed (editor but not owner is false)")

	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'viewer', 'document', '2')`).Scan(&allowed)
	require.NoError(t, err)
	require.False(t, allowed, "badger should be excluded (editor but not owner is true)")

	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'cheetah', 'viewer', 'document', '3')`).Scan(&allowed)
	require.NoError(t, err)
	require.True(t, allowed, "cheetah should be allowed")
}
