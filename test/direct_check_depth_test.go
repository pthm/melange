package test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/melange"
)

// Generated check functions call each other directly when the target pair is
// known when the SQL is generated, instead of through
// check_permission_internal. The dispatcher raised M2002 once p_visited
// reached 26 entries; these tests pin that every check function now raises
// at the same depth itself, so a chain that crosses only direct calls still
// fails where it did through the dispatcher.

// directDepthSchema has single-type parent and org links, so
// check_folder_viewer calls check_folder_viewer and check_org_owner directly.
const directDepthSchema = `model
  schema 1.1

type user

type org
  relations
    define owner: [user]

type folder
  relations
    define parent: [folder]
    define org: [org]
    define viewer: [user] or viewer from parent or owner from org
`

// installFolderChain installs directDepthSchema with folders f0..f<hops>,
// each f<i> the parent of f<i-1>.
func installFolderChain(t *testing.T, ctx context.Context, version string, hops int) *sql.DB {
	t.Helper()
	db := installAdHocSchema(t, ctx, directDepthSchema, version)
	for i := 0; i < hops; i++ {
		insertTuple(t, ctx, db, "folder", fmt.Sprintf("f%d", i+1), "parent", "folder", fmt.Sprintf("f%d", i))
	}
	return db
}

// checkFolderViewer checks whether user:alice is a viewer of folder:f0.
func checkFolderViewer(ctx context.Context, db *sql.DB) (bool, error) {
	return melange.NewChecker(db).Check(ctx, melange.Object{Type: "user", ID: "alice"}, melange.Relation("viewer"), melange.Object{Type: "folder", ID: "f0"})
}

func requireTooComplex(t *testing.T, err error) {
	t.Helper()
	var ve *melange.ValidationError
	require.Truef(t, errors.As(err, &ve), "want M2002 (resolution too complex), got %v", err)
	assert.Equal(t, melange.ErrorCodeResolutionTooComplex, ve.Code)
}

func TestDirectCheck_DepthLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()

	t.Run("bodies call check functions directly", func(t *testing.T) {
		db := installAdHocSchema(t, ctx, directDepthSchema, "v1.0.0-direct-body")
		var src string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'check_folder_viewer'`).Scan(&src))
		assert.Contains(t, src, `check_folder_viewer(p_subject_type, p_subject_id, link.subject_id, p_visited || ARRAY[v_key])`)
		assert.Contains(t, src, `check_org_owner(p_subject_type, p_subject_id, link.subject_id, p_visited || ARRAY[v_key])`)
		assert.NotContains(t, src, "check_permission_internal")

		require.NoError(t, db.QueryRowContext(ctx, `SELECT prosrc FROM pg_proc WHERE proname = 'check_org_owner'`).Scan(&src))
		assert.Contains(t, src, "IF array_length(p_visited, 1) >= 26 THEN", "the leaf check function must raise at the depth limit itself")
	})

	// 24 parent hops reach f24 with 24 visited keys; its org lookup calls
	// check_org_owner with 25, within the limit.
	t.Run("24 parent hops then the org lookup is allowed", func(t *testing.T) {
		db := installFolderChain(t, ctx, "v1.0.0-direct-24", 24)
		insertTuple(t, ctx, db, "org", "o1", "org", "folder", "f24")
		insertTuple(t, ctx, db, "user", "alice", "owner", "org", "o1")
		allowed, err := checkFolderViewer(ctx, db)
		require.NoError(t, err)
		assert.True(t, allowed)
	})

	// 25 parent hops put 26 keys on the leaf call: check_org_owner raises,
	// as check_permission_internal did before routing to it.
	t.Run("25 parent hops then the org lookup raises M2002", func(t *testing.T) {
		db := installFolderChain(t, ctx, "v1.0.0-direct-25", 25)
		insertTuple(t, ctx, db, "org", "o1", "org", "folder", "f25")
		insertTuple(t, ctx, db, "user", "alice", "owner", "org", "o1")
		_, err := checkFolderViewer(ctx, db)
		requireTooComplex(t, err)
	})

	// A 26-folder cycle with no grant re-enters f0 with 26 keys. The depth
	// guard runs before the cycle check, so it raises as the dispatcher did
	// instead of returning 0 at the repeated key.
	t.Run("26-folder cycle raises M2002", func(t *testing.T) {
		db := installFolderChain(t, ctx, "v1.0.0-direct-cycle", 25)
		insertTuple(t, ctx, db, "folder", "f0", "parent", "folder", "f25")
		_, err := checkFolderViewer(ctx, db)
		requireTooComplex(t, err)
	})
}

// TestDirectCheck_IllTypedLinks pins that a direct call keeps the type filter
// the dispatcher made redundant. check_folder_viewer calls itself on
// link.subject_id for parent links and check_org_owner for org links, which
// is only sound while the query keeps link.subject_type to folder and org.
// melange_tuples is user-defined, so it can hold links of a type the model
// does not allow; each one here names an id that would grant access if it
// were checked as the called function's type.
func TestDirectCheck_IllTypedLinks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	ctx := context.Background()
	db := installAdHocSchema(t, ctx, directDepthSchema, "v1.0.0-direct-ill-typed")
	// folder:f0's parent is org:x, but alice views folder:x.
	insertTuple(t, ctx, db, "org", "x", "parent", "folder", "f0")
	insertTuple(t, ctx, db, "user", "alice", "viewer", "folder", "x")
	// folder:f0's org is folder:y, but alice owns org:y.
	insertTuple(t, ctx, db, "folder", "y", "org", "folder", "f0")
	insertTuple(t, ctx, db, "user", "alice", "owner", "org", "y")

	checker := melange.NewChecker(db)
	alice := melange.Object{Type: "user", ID: "alice"}

	allowed, err := checkFolderViewer(ctx, db)
	require.NoError(t, err)
	assert.False(t, allowed, "an ill-typed parent or org link must not grant viewer on folder:f0")

	ids, err := checker.ListObjectsAll(ctx, alice, melange.Relation("viewer"), "folder")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"x"}, ids)

	ids, err = checker.ListSubjectsAll(ctx, melange.Object{Type: "folder", ID: "f0"}, melange.Relation("viewer"), "user")
	require.NoError(t, err)
	assert.Empty(t, ids)
}
