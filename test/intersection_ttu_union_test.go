package test

import (
	"context"
	"testing"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
	"github.com/stretchr/testify/require"
)

// TestIntersectionWithTTUUnionParsing verifies that intersection rules containing
// unions of tuple-to-userset patterns are correctly parsed.
//
// Schema: can_view: viewer and (member from group or owner from group)
// This should produce 2 intersection groups (via distributive law):
//   - Group 1: viewer AND (member from group)
//   - Group 2: viewer AND (owner from group)
func TestIntersectionWithTTUUnionParsing(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type group
  relations
    define owner: [user]
    define member: [user]

type folder
  relations
    define group: [group]
    define viewer: [user]
    define can_view: viewer and (member from group or owner from group)
`

	types, err := parser.ParseSchemaString(dsl)
	require.NoError(t, err)

	// Find the can_view relation on folder
	var canViewRel *schema.RelationDefinition
	for i := range types {
		if types[i].Name != "folder" {
			continue
		}
		for j := range types[i].Relations {
			if types[i].Relations[j].Name == "can_view" {
				canViewRel = &types[i].Relations[j]
				break
			}
		}
	}
	require.NotNil(t, canViewRel, "can_view relation not found")

	// Should have 2 intersection groups due to distributive expansion
	require.Len(t, canViewRel.IntersectionGroups, 2,
		"expected 2 intersection groups from distributive law: (viewer AND member from group) OR (viewer AND owner from group)")

	// Each group should have the viewer relation
	for i, g := range canViewRel.IntersectionGroups {
		require.Contains(t, g.Relations, "viewer",
			"group %d should contain 'viewer' relation", i)
		require.Len(t, g.ParentRelations, 1,
			"group %d should have exactly 1 parent relation", i)
		require.Equal(t, "group", g.ParentRelations[0].LinkingRelation,
			"group %d parent relation should link via 'group'", i)
	}

	// Verify we have both member and owner parent relations across groups
	parentRels := make(map[string]bool)
	for _, g := range canViewRel.IntersectionGroups {
		for _, pr := range g.ParentRelations {
			parentRels[pr.Relation] = true
		}
	}
	require.True(t, parentRels["member"], "should have 'member from group' parent relation")
	require.True(t, parentRels["owner"], "should have 'owner from group' parent relation")
}

// TestIntersectionWithTTUUnionSQL verifies that the generated SQL correctly
// enforces the intersection semantics with TTU unions.
//
// Schema: can_view: viewer and (member from group or owner from group)
//
// A user can view a folder if:
//   - They have the 'viewer' relation on the folder, AND
//   - They are either 'member' OR 'owner' of the folder's linked group
func TestIntersectionWithTTUUnionSQL(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type group
  relations
    define owner: [user]
    define member: [user]

type folder
  relations
    define group: [group]
    define viewer: [user]
    define can_view: viewer and (member from group or owner from group)
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	err := migrator.MigrateFromString(ctx, db, dsl)
	require.NoError(t, err)

	// Create test data:
	// - group:engineering with member:alice, owner:bob
	// - folder:docs linked to group:engineering
	// - viewer relations: alice, bob, charlie (charlie is NOT in group)
	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT * FROM (VALUES
			-- Group membership
			('user', 'alice', 'member', 'group', 'engineering'),
			('user', 'bob', 'owner', 'group', 'engineering'),

			-- Folder -> Group link
			('group', 'engineering', 'group', 'folder', 'docs'),

			-- Viewer relations on folder
			('user', 'alice', 'viewer', 'folder', 'docs'),
			('user', 'bob', 'viewer', 'folder', 'docs'),
			('user', 'charlie', 'viewer', 'folder', 'docs'),

			-- Diana is member of group but NOT viewer of folder
			('user', 'diana', 'member', 'group', 'engineering')
		) AS t(subject_type, subject_id, relation, object_type, object_id)
	`)
	require.NoError(t, err)

	tests := []struct {
		name     string
		user     string
		expected bool
		reason   string
	}{
		{
			name:     "alice_allowed",
			user:     "alice",
			expected: true,
			reason:   "alice is viewer AND member of group",
		},
		{
			name:     "bob_allowed",
			user:     "bob",
			expected: true,
			reason:   "bob is viewer AND owner of group",
		},
		{
			name:     "charlie_denied",
			user:     "charlie",
			expected: false,
			reason:   "charlie is viewer but NOT member or owner of group",
		},
		{
			name:     "diana_denied",
			user:     "diana",
			expected: false,
			reason:   "diana is member of group but NOT viewer of folder",
		},
		{
			name:     "unknown_denied",
			user:     "unknown",
			expected: false,
			reason:   "unknown user has no relations",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var allowed bool
			err := db.QueryRowContext(ctx,
				`SELECT check_permission('user', $1, 'can_view', 'folder', 'docs')`,
				tc.user,
			).Scan(&allowed)
			require.NoError(t, err)
			require.Equal(t, tc.expected, allowed, tc.reason)
		})
	}
}

// TestIntersectionWithMixedUnionSQL tests intersection with a union containing
// both a simple relation and a TTU pattern.
//
// Schema: can_edit: editor and (admin or owner from org)
//
// A user can edit if:
//   - They have the 'editor' relation, AND
//   - They are either 'admin' directly OR 'owner' of the linked org
func TestIntersectionWithMixedUnionSQL(t *testing.T) {
	dsl := `
model
  schema 1.1

type user

type org
  relations
    define owner: [user]

type document
  relations
    define org: [org]
    define editor: [user]
    define admin: [user]
    define can_edit: editor and (admin or owner from org)
`

	db := testutil.EmptyDB(t)
	ctx := context.Background()

	err := migrator.MigrateFromString(ctx, db, dsl)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT * FROM (VALUES
			-- Org ownership
			('user', 'ceo', 'owner', 'org', 'acme'),

			-- Document -> Org link
			('org', 'acme', 'org', 'document', 'report'),

			-- Editor relations
			('user', 'alice', 'editor', 'document', 'report'),
			('user', 'bob', 'editor', 'document', 'report'),
			('user', 'ceo', 'editor', 'document', 'report'),
			('user', 'charlie', 'editor', 'document', 'report'),

			-- Admin relations (direct, no org needed)
			('user', 'alice', 'admin', 'document', 'report')
		) AS t(subject_type, subject_id, relation, object_type, object_id)
	`)
	require.NoError(t, err)

	tests := []struct {
		name     string
		user     string
		expected bool
		reason   string
	}{
		{
			name:     "alice_allowed_via_admin",
			user:     "alice",
			expected: true,
			reason:   "alice is editor AND admin (direct relation path)",
		},
		{
			name:     "ceo_allowed_via_org_owner",
			user:     "ceo",
			expected: true,
			reason:   "ceo is editor AND owner of linked org (TTU path)",
		},
		{
			name:     "bob_denied",
			user:     "bob",
			expected: false,
			reason:   "bob is editor but NOT admin and NOT owner of org",
		},
		{
			name:     "charlie_denied",
			user:     "charlie",
			expected: false,
			reason:   "charlie is editor but NOT admin and NOT owner of org",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var allowed bool
			err := db.QueryRowContext(ctx,
				`SELECT check_permission('user', $1, 'can_edit', 'document', 'report')`,
				tc.user,
			).Scan(&allowed)
			require.NoError(t, err)
			require.Equal(t, tc.expected, allowed, tc.reason)
		})
	}
}

// BenchmarkIntersectionWithTTUUnion benchmarks permission checks for
// intersection rules with TTU unions.
//
// Schema: can_view: viewer and (member from group or owner from group)
func BenchmarkIntersectionWithTTUUnion(b *testing.B) {
	dsl := `
model
  schema 1.1

type user

type group
  relations
    define owner: [user]
    define member: [user]

type folder
  relations
    define group: [group]
    define viewer: [user]
    define can_view: viewer and (member from group or owner from group)
`

	db := testutil.EmptyDB(b)
	ctx := context.Background()

	err := migrator.MigrateFromString(ctx, db, dsl)
	require.NoError(b, err)

	_, err = db.ExecContext(ctx, `
		CREATE VIEW melange_tuples AS
		SELECT * FROM (VALUES
			('user', 'alice', 'member', 'group', 'engineering'),
			('user', 'bob', 'owner', 'group', 'engineering'),
			('group', 'engineering', 'group', 'folder', 'docs'),
			('user', 'alice', 'viewer', 'folder', 'docs'),
			('user', 'bob', 'viewer', 'folder', 'docs'),
			('user', 'charlie', 'viewer', 'folder', 'docs'),
			('user', 'diana', 'member', 'group', 'engineering')
		) AS t(subject_type, subject_id, relation, object_type, object_id)
	`)
	require.NoError(b, err)

	benchmarks := []struct {
		name     string
		user     string
		expected bool
	}{
		{"allowed_via_member", "alice", true},
		{"allowed_via_owner", "bob", true},
		{"denied_not_in_group", "charlie", false},
		{"denied_not_viewer", "diana", false},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var allowed bool
				err := db.QueryRowContext(ctx,
					`SELECT check_permission('user', $1, 'can_view', 'folder', 'docs')`,
					bm.user,
				).Scan(&allowed)
				if err != nil {
					b.Fatal(err)
				}
				if allowed != bm.expected {
					b.Fatalf("expected %v, got %v", bm.expected, allowed)
				}
			}
		})
	}
}
