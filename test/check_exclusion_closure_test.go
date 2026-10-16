package test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A union must evaluate an implied relation's exclusion before granting access,
// even when the excluded relation is reached through multiple aliases.
func TestCheckPermission_ExclusionClosure(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()
	db := installAdHocSchema(t, ctx, `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define writer: [user]
    define blocked: [user]
    define active: [user]
    define reader: writer but not blocked
    define eligible: writer and active
    define can_read: owner or reader
    define can_edit: owner or eligible
    define alias: reader
    define forwarded: owner or alias
    define writer_override: forwarded or writer
`, "exclusion-closure")

	for _, tc := range []struct {
		name                                         string
		tuples                                       []string
		reader, canRead, eligible, canEdit, override int
	}{
		{name: "blocked_writer", tuples: []string{"writer", "blocked"}, override: 1},
		{name: "writer", tuples: []string{"writer"}, reader: 1, canRead: 1, override: 1},
		{name: "owner", tuples: []string{"owner"}, canRead: 1, canEdit: 1},
		{name: "blocked_owner_writer", tuples: []string{"owner", "writer", "blocked"}, canRead: 1, canEdit: 1, override: 1},
		{name: "active_writer", tuples: []string{"writer", "active"}, reader: 1, canRead: 1, eligible: 1, canEdit: 1, override: 1},
		{name: "blocked_active_writer", tuples: []string{"writer", "active", "blocked"}, eligible: 1, canEdit: 1, override: 1},
		{name: "stranger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, relation := range tc.tuples {
				insertTuple(t, ctx, db, "user", tc.name, relation, "document", "doc")
			}
			for relation, want := range map[string]int{
				"reader": tc.reader, "can_read": tc.canRead,
				"eligible": tc.eligible, "can_edit": tc.canEdit,
				"alias": tc.reader, "forwarded": tc.canRead,
				"writer_override": max(tc.canRead, tc.override),
			} {
				var got, noWildcard int
				var listedObject, listedSubject bool
				err := db.QueryRowContext(ctx,
					`SELECT
						check_permission('user', $1, $2, 'document', 'doc'),
						check_permission_nw('user', $1, $2, 'document', 'doc'),
						EXISTS (SELECT 1 FROM list_accessible_objects('user', $1, $2, 'document') WHERE object_id = 'doc'),
						EXISTS (SELECT 1 FROM list_accessible_subjects('document', 'doc', $2, 'user') WHERE subject_id = $1)`,
					tc.name, relation).Scan(&got, &noWildcard, &listedObject, &listedSubject)
				require.NoError(t, err)
				assert.Equal(t, want, got, "%s permission", relation)
				assert.Equal(t, want, noWildcard, "%s permission without wildcards", relation)
				assert.Equal(t, want == 1, listedObject, "%s list objects", relation)
				assert.Equal(t, want == 1, listedSubject, "%s list subjects", relation)
			}
		})
	}
}
