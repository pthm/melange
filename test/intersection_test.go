package test

import (
	"context"
	"testing"

	"github.com/pthm/melange"
	"github.com/pthm/melange/schema"
	"github.com/pthm/melange/test/testutil"
	"github.com/pthm/melange/tooling"
	"github.com/stretchr/testify/require"
)

func TestIntersectionParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define viewer: writer and editor
`

	types, err := tooling.ParseSchemaString(dsl)
	require.NoError(t, err)
	require.Len(t, types, 2) // user, document

	// Find document type
	var docType *schema.TypeDefinition
	for i := range types {
		if types[i].Name == "document" {
			docType = &types[i]
			break
		}
	}
	require.NotNil(t, docType)

	// Find viewer relation
	var viewerRel *schema.RelationDefinition
	for i := range docType.Relations {
		if docType.Relations[i].Name == "viewer" {
			viewerRel = &docType.Relations[i]
			break
		}
	}
	require.NotNil(t, viewerRel)

	// Check intersection groups
	t.Logf("viewer.IntersectionGroups: %+v", viewerRel.IntersectionGroups)
	t.Logf("viewer.ImpliedBy: %+v", viewerRel.ImpliedBy)
	t.Logf("viewer.SubjectTypeRefs: %+v", viewerRel.SubjectTypeRefs)

	require.Len(t, viewerRel.IntersectionGroups, 1, "should have one intersection group")
	require.ElementsMatch(t, viewerRel.IntersectionGroups[0].Relations, []string{"writer", "editor"})
	require.Empty(t, viewerRel.ImpliedBy, "ImpliedBy should be empty for intersection")

	// Check model generation
	models := schema.ToAuthzModels(types)
	t.Logf("Generated %d models", len(models))

	// Count intersection rules for viewer
	var intersectionRules []schema.AuthzModel
	for _, m := range models {
		if m.ObjectType == "document" && m.Relation == "viewer" {
			t.Logf("viewer model: %+v", m)
			if m.RuleGroupMode != nil && *m.RuleGroupMode == "intersection" {
				intersectionRules = append(intersectionRules, m)
			}
		}
	}

	require.Len(t, intersectionRules, 2, "should have 2 intersection rules for viewer")

	// Check the rules have correct check_relations
	checkRelations := make([]string, 0, len(intersectionRules))
	for _, r := range intersectionRules {
		require.NotNil(t, r.CheckRelation)
		checkRelations = append(checkRelations, *r.CheckRelation)
	}
	require.ElementsMatch(t, checkRelations, []string{"writer", "editor"})
}

func TestIntersectionSQL(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define viewer: writer and editor
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Migrate schema
	err := tooling.MigrateFromString(ctx, db, dsl)
	require.NoError(t, err)

	// Create tuples view
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
			('badger', 'writer', '2'),
			('cheetah', 'editor', '3')
		) AS t(subject_id, relation, object_id)
	`)
	require.NoError(t, err)

	// Test permission checks
	checker := melange.NewChecker(db)

	// Test 1: aardvark should have viewer (has both writer AND editor)
	ok, err := checker.Check(ctx, melange.Object{Type: "user", ID: "aardvark"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "1"})
	require.NoError(t, err)
	require.True(t, ok, "aardvark should have viewer on document:1")

	// Test 2: badger should NOT have viewer (only has writer, not editor)
	ok, err = checker.Check(ctx, melange.Object{Type: "user", ID: "badger"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "2"})
	require.NoError(t, err)
	require.False(t, ok, "badger should NOT have viewer on document:2 (only has writer)")

	// Test 3: cheetah should NOT have viewer (only has editor, not writer)
	ok, err = checker.Check(ctx, melange.Object{Type: "user", ID: "cheetah"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "3"})
	require.NoError(t, err)
	require.False(t, ok, "cheetah should NOT have viewer on document:3 (only has editor)")

	// Debug: Test check_permission directly (Phase 5: replaced subject_has_grant)
	var hasWriter, hasEditor int
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'writer', 'document', '2')`).Scan(&hasWriter)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'editor', 'document', '2')`).Scan(&hasEditor)
	require.NoError(t, err)
	t.Logf("badger on document:2: writer=%v, editor=%v", hasWriter, hasEditor)
}

func TestIntersectionExclusionInSubtractParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor and owner)
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
	require.Len(t, viewerRel.ExcludedIntersectionGroups, 1, "should have one excluded intersection group")
	require.ElementsMatch(t, viewerRel.ExcludedIntersectionGroups[0].Relations, []string{"editor", "owner"})
}

func TestIntersectionExclusionInSubtractSQL(t *testing.T) {
	schema := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define editor: [user]
    define owner: [user]
    define viewer: writer but not (editor and owner)
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	err := tooling.MigrateFromString(ctx, db, schema)
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
			('cheetah', 'owner', '3'),
			('duck', 'writer', '4')
		) AS t(subject_id, relation, object_id)
	`)
	require.NoError(t, err)

	var allowed bool
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'aardvark', 'viewer', 'document', '1')`).Scan(&allowed)
	require.NoError(t, err)
	require.False(t, allowed, "aardvark should be excluded by editor and owner")

	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'viewer', 'document', '2')`).Scan(&allowed)
	require.NoError(t, err)
	require.True(t, allowed, "badger should be allowed")
}

func TestIntersectionWildcardInComputedRelation(t *testing.T) {
	schema := `
model
  schema 1.1

type user

type document
  relations
    define allowed: [user]
    define viewer: [user:*] and allowed
    define can_view: viewer
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	err := tooling.MigrateFromString(ctx, db, schema)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT 'user'::text AS subject_type,
		       subject_id,
		       relation,
		       'document'::text AS object_type,
		       object_id
		FROM (VALUES
			('jon', 'allowed', '1'),
			('*', 'viewer', '1'),
			('*', 'viewer', '2')
		) AS t(subject_id, relation, object_id)
	`)
	require.NoError(t, err)

	var allowed bool
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'jon', 'can_view', 'document', '1')`).Scan(&allowed)
	require.NoError(t, err)
	require.True(t, allowed, "jon should be allowed for can_view on document:1")

	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'bob', 'can_view', 'document', '2')`).Scan(&allowed)
	require.NoError(t, err)
	require.False(t, allowed, "bob should be denied for can_view on document:2")

	rows, err := db.QueryContext(ctx, `SELECT subject_id FROM list_accessible_subjects('document', '1', 'can_view', 'user') ORDER BY subject_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var subjects []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		subjects = append(subjects, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"jon"}, subjects)
}

// TestThisAndIntersection tests the "[user] and writer" pattern.
// This tests that having a direct tuple for the relation is not sufficient;
// the subject must also satisfy the other parts of the intersection.
func TestThisAndIntersection(t *testing.T) {
	schema := `
model
  schema 1.1

type user

type document
  relations
    define writer: [user]
    define viewer: [user] and writer
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	// Migrate schema
	err := tooling.MigrateFromString(ctx, db, schema)
	require.NoError(t, err)

	// Create tuples view
	// - aardvark has both viewer and writer on doc:1 (should have viewer)
	// - badger has only viewer on doc:2 (should NOT have viewer - missing writer)
	// - cheetah has only writer on doc:3 (should NOT have viewer - missing direct tuple)
	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT 'user'::text AS subject_type,
		       subject_id,
		       relation,
		       'document'::text AS object_type,
		       object_id
		FROM (VALUES
			('aardvark', 'viewer', '1'),
			('aardvark', 'writer', '1'),
			('badger', 'viewer', '2'),
			('cheetah', 'writer', '3')
		) AS t(subject_id, relation, object_id)
	`)
	require.NoError(t, err)

	// Debug: Check what SQL functions return for badger (Phase 5: using check_permission)
	var hasViewerGrant, hasWriterGrant int
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'viewer', 'document', '2')`).Scan(&hasViewerGrant)
	require.NoError(t, err)
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'writer', 'document', '2')`).Scan(&hasWriterGrant)
	require.NoError(t, err)
	t.Logf("check_permission for badger: viewer=%d, writer=%d", hasViewerGrant, hasWriterGrant)

	var checkResult int
	err = db.QueryRowContext(ctx, `SELECT check_permission('user', 'badger', 'viewer', 'document', '2')`).Scan(&checkResult)
	require.NoError(t, err)
	t.Logf("check_permission for badger: %d", checkResult)

	checker := melange.NewChecker(db)

	// Test 1: aardvark should have viewer (has both viewer direct tuple AND writer)
	ok, err := checker.Check(ctx, melange.Object{Type: "user", ID: "aardvark"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "1"})
	require.NoError(t, err)
	require.True(t, ok, "aardvark should have viewer on document:1 (has both viewer tuple and writer)")

	// Test 2: badger should NOT have viewer (only has viewer direct tuple, not writer)
	ok, err = checker.Check(ctx, melange.Object{Type: "user", ID: "badger"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "2"})
	require.NoError(t, err)
	require.False(t, ok, "badger should NOT have viewer on document:2 (only has viewer tuple, missing writer)")

	// Test 3: cheetah should NOT have viewer (only has writer, not direct viewer tuple)
	ok, err = checker.Check(ctx, melange.Object{Type: "user", ID: "cheetah"}, melange.Relation("viewer"), melange.Object{Type: "document", ID: "3"})
	require.NoError(t, err)
	require.False(t, ok, "cheetah should NOT have viewer on document:3 (only has writer, missing viewer tuple)")
}
