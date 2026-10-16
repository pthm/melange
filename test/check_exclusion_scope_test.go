package test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A branch's exclusion must not deny an independent union grant, even when
// the enclosing relation has an exclusion of its own.
func TestCheckPermission_ExclusionScope(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	for _, complex := range []bool{false, true} {
		t.Run(fmt.Sprintf("complex_exclusions_%t", complex), func(t *testing.T) {
			branchExcluded, outerExcluded := "b", "d"
			if complex {
				branchExcluded, outerExcluded = "branch_blocked", "outer_blocked"
			}
			ctx := context.Background()
			db := installAdHocSchema(t, ctx, fmt.Sprintf(`model
  schema 1.1
type user
type document
  relations
    define a: [user]
    define b: [user]
    define c: [user]
    define d: [user]
    define branch_blocked: b
    define outer_blocked: d
    define reader: a but not %s
    define can_read: reader or c
    define final: can_read but not %s
    define alias: final
`, branchExcluded, outerExcluded), "exclusion-scope")

			for mask := 0; mask < 16; mask++ {
				subject := fmt.Sprintf("user%d", mask)
				for bit, relation := range []string{"a", "b", "c", "d"} {
					if mask&(1<<bit) != 0 {
						insertTuple(t, ctx, db, "user", subject, relation, "document", "doc")
					}
				}
				reader := mask&1 != 0 && mask&2 == 0
				canRead := reader || mask&4 != 0
				final := canRead && mask&8 == 0
				for relation, want := range map[string]bool{
					"reader": reader, "can_read": canRead, "final": final, "alias": final,
				} {
					var got, noWildcard int
					var listedObject, listedSubject bool
					err := db.QueryRowContext(ctx, `SELECT
                        check_permission('user', $1, $2, 'document', 'doc'),
                        check_permission_nw('user', $1, $2, 'document', 'doc'),
                        EXISTS (SELECT 1 FROM list_accessible_objects('user', $1, $2, 'document') WHERE object_id = 'doc'),
                        EXISTS (SELECT 1 FROM list_accessible_subjects('document', 'doc', $2, 'user') WHERE subject_id = $1)`,
						subject, relation).Scan(&got, &noWildcard, &listedObject, &listedSubject)
					require.NoError(t, err)
					assert.Equal(t, want, got == 1, "mask=%d %s permission", mask, relation)
					assert.Equal(t, want, noWildcard == 1, "mask=%d %s permission without wildcards", mask, relation)
					assert.Equal(t, want, listedObject, "mask=%d %s list objects", mask, relation)
					assert.Equal(t, want, listedSubject, "mask=%d %s list subjects", mask, relation)
				}
			}
		})
	}
}
