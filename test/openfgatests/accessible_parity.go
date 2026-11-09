package openfgatests

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/schema"
)

// runAccessibleParityAssertions checks the accessible functions applications
// join against, which list_objects only partly goes through, against the
// stage's assertions:
//
//   - accessible_{type}_{relation}(subject) returns exactly the objects of
//     each list_objects assertion, and contains the object of each check
//     assertion exactly when the check expects true.
//   - For a relation with a parent-key function, the objects whose link
//     tuple points at a parent the function returns are exactly the objects
//     of each list_objects assertion. A subject that is a userset on the
//     object type itself is skipped: the function documents it as not
//     covered.
//
// Error-coded, contextual-tuple and conditional assertions are skipped (the
// functions take none of them), as are relations without an accessible
// function, and checks the accessible function cannot answer within the list
// depth limit, which is one hop shorter than check's (as in OpenFGA).
func runAccessibleParityAssertions(t *testing.T, ctx context.Context, client *Client, storeID, modelID string, stage *Stage) {
	t.Helper()
	if client.openfgaBackend != nil {
		return // melange-only functions
	}
	s, ok := client.storeByID(storeID)
	require.True(t, ok, "store %s", storeID)
	m := s.models[modelID]
	require.NotNil(t, m, "model %s", modelID)

	conn, err := s.db.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	if client.databaseSchema != "" {
		// The accessible functions are inlined into the caller's query, so
		// melange_tuples resolves on the caller's search_path.
		_, err := conn.ExecContext(ctx, "SET search_path = "+sqldsl.QuoteIdent(client.databaseSchema)+", public")
		require.NoError(t, err)
		defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "RESET search_path") }()
	}

	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(m.types, schema.ComputeRelationClosure(m.types)))
	parentKeys := map[string][]sqlgen.ParentKeyFunction{}
	for _, pk := range sqlgen.ParentKeyFunctions(analyses) {
		key := pk.ObjectType + "." + pk.Relation
		parentKeys[key] = append(parentKeys[key], pk)
	}
	exists := map[string]bool{}
	accessibleFn := func(objectType, relation string) (string, bool) {
		name := sqlgen.AccessibleObjectsFunctionName(objectType, relation)
		if _, seen := exists[name]; !seen {
			var ok bool
			require.NoError(t, conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace WHERE p.proname = $1 AND n.nspname = current_schema())`,
				name).Scan(&ok))
			exists[name] = ok
		}
		return sqldsl.PrefixIdent(name, client.databaseSchema), exists[name]
	}
	objects := func(query string, args ...any) []string {
		t.Helper()
		rows, err := conn.QueryContext(ctx, query, args...)
		require.NoError(t, err, query)
		defer func() { _ = rows.Close() }()
		out := []string{}
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		require.NoError(t, rows.Err())
		sort.Strings(out)
		return out
	}

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

		t.Run(fmt.Sprintf("accessible_parity_objects_%d", i), func(t *testing.T) {
			fn, ok := accessibleFn(a.Request.Type, a.Request.Relation)
			if !ok {
				t.Skipf("no accessible function for %s.%s", a.Request.Type, a.Request.Relation)
			}
			require.Equal(t, want, objects(fmt.Sprintf(`SELECT DISTINCT object_id FROM %s($1, $2)`, fn), subjectType, subjectID),
				"%s(%s) vs list_objects expectation", fn, a.Request.User)

			// A userset on the object type itself is not covered by the
			// parent-key functions.
			if subjectType == a.Request.Type && strings.Contains(subjectID, "#") {
				return
			}
			for _, pk := range parentKeys[a.Request.Type+"."+a.Request.Relation] {
				got := objects(fmt.Sprintf(`SELECT DISTINCT t.object_id FROM melange_tuples t
					WHERE t.object_type = $3 AND t.relation = $4 AND t.subject_type = $5
					  AND t.subject_id IN (SELECT * FROM %s($1, $2))`, sqldsl.PrefixIdent(pk.Name, client.databaseSchema)),
					subjectType, subjectID, pk.ObjectType, pk.Link, pk.ParentType)
				require.Equal(t, want, got, "objects linked to %s(%s) vs list_objects expectation", pk.Name, a.Request.User)
			}
		})
	}

	for i, a := range stage.CheckAssertions {
		if a.ErrorCode != 0 || len(a.ContextualTuples) != 0 || a.Context != nil {
			continue
		}
		subjectType, subjectID, ok1 := strings.Cut(a.Tuple.User, ":")
		objectType, objectID, ok2 := strings.Cut(a.Tuple.Object, ":")
		if !ok1 || !ok2 {
			continue
		}
		fn, ok := accessibleFn(objectType, a.Tuple.Relation)
		if !ok {
			continue
		}
		t.Run(fmt.Sprintf("accessible_parity_check_%d", i), func(t *testing.T) {
			var got bool
			err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s($1, $2) a WHERE a.object_id = $3)`, fn),
				subjectType, subjectID, objectID).Scan(&got)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "M2002" {
				// The accessible functions walk like list_objects, which
				// raises one hop sooner than check, as OpenFGA's do.
				t.Skipf("%s(%s) is past the list depth limit", fn, a.Tuple.User)
			}
			require.NoError(t, err)
			require.Equal(t, a.Expectation, got, "%s(%s) contains %s", fn, a.Tuple.User, a.Tuple.Object)
		})
	}
}
