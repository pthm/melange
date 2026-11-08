package openfgatests

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// runListPagingParityAssertions pages every list_objects assertion through
// list_accessible_objects with small page sizes and checks that the pages, in
// order, are exactly the expected objects, with next_cursor set on every page
// but the last. The other sweeps list without a limit, which runs a different
// statement: the paged one reads arms limited to a page (a page of each
// parent, for TTU compositions), and only paging exercises it.
//
// Error-coded and contextual-tuple assertions are skipped.
func runListPagingParityAssertions(t *testing.T, ctx context.Context, client *Client, storeID string, stage *Stage) {
	t.Helper()
	if client.openfgaBackend != nil {
		return // melange-only function
	}
	s, ok := client.storeByID(storeID)
	require.True(t, ok, "store %s", storeID)
	list := sqldsl.PrefixIdent("list_accessible_objects", client.databaseSchema)

	for i, a := range stage.ListObjectsAssertions {
		if a.ErrorCode != 0 || len(a.ContextualTuples) != 0 {
			continue
		}
		subjectType, subjectID, ok := strings.Cut(a.Request.User, ":")
		if !ok {
			continue
		}
		want := make([]string, 0, len(a.Expectation))
		for _, o := range a.Expectation {
			want = append(want, strings.TrimPrefix(o, a.Request.Type+":"))
		}
		sort.Strings(want)

		for _, size := range []int{1, 2} {
			t.Run(fmt.Sprintf("list_paging_parity_%d_by_%d", i, size), func(t *testing.T) {
				got := []string{}
				var cursor sql.NullString
				for pages := 0; ; pages++ {
					require.LessOrEqual(t, pages, len(want)/size+1, "%s(%s) did not stop paging", list, a.Request.User)
					rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT object_id, next_cursor FROM %s($1, $2, $3, $4, $5, $6)`, list),
						subjectType, subjectID, a.Request.Relation, a.Request.Type, size, cursor)
					require.NoError(t, err)
					var next sql.NullString
					n := 0
					for rows.Next() {
						var id string
						require.NoError(t, rows.Scan(&id, &next))
						got = append(got, id)
						n++
					}
					require.NoError(t, rows.Err())
					_ = rows.Close()
					require.LessOrEqual(t, n, size, "page of %d returned %d objects", size, n)
					if !next.Valid {
						break
					}
					require.Equal(t, size, n, "a page with next_cursor must be full")
					cursor = next
				}
				require.Equal(t, want, got, "pages of %d of %s(%s, %s, %s) vs list_objects expectation",
					size, list, a.Request.User, a.Request.Relation, a.Request.Type)
			})
		}
	}
}
