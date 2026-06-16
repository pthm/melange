package test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/lib/sqlgen"
	"github.com/pthm/melange/lib/sqlgen/analysis"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// TestRecommendIndexes_DDLValidAgainstRealPG verifies every CREATE INDEX
// produced by RecommendIndexes parses and executes against a PostgreSQL
// instance with a melange_tuples-shaped table. Catches column-name typos,
// composite-syntax mistakes, partial-index WHERE-clause errors, and other
// regressions that pure Go tests can't see.
func TestRecommendIndexes_DDLValidAgainstRealPG(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	models := map[string]string{
		"direct_only": `model
  schema 1.1
type user
type document
  relations
    define owner: [user]`,
		"wildcard": `model
  schema 1.1
type user
type document
  relations
    define public: [user, user:*]`,
		"computed_userset_chain": `model
  schema 1.1
type user
type document
  relations
    define writer: [user]
    define editor: [user] or writer
    define viewer: [user] or editor`,
		"ttu_with_userset_wildcard": `model
  schema 1.1
type user
type group
  relations
    define member: [user, group#member]
type folder
  relations
    define parent: [folder]
    define viewer: [user, group#member, user:*]
    define blocked: [user]
type document
  relations
    define parent: [folder]
    define owner: [user]
    define blocked_viewer: blocked from parent
    define viewer: (owner or viewer from parent) but not blocked_viewer`,
	}

	// Use a short, unique table name per subtest so rewritten index names stay
	// well below PG's 63-byte identifier limit. The actual production names
	// (`idx_melange_tuples_by_object_type_object_id` etc.) are 43–53 chars, but
	// a longer subtest-derived prefix could silently truncate and collide.
	shortIDs := map[string]string{
		"direct_only":               "d",
		"wildcard":                  "w",
		"computed_userset_chain":    "c",
		"ttu_with_userset_wildcard": "k",
	}

	for name, model := range models {
		t.Run(name, func(t *testing.T) {
			recs := recommendForModel(t, model)
			require.NotEmpty(t, recs, "expected at least one recommendation")

			db := testutil.DB(t)
			ctx := context.Background()

			short, ok := shortIDs[name]
			require.True(t, ok, "missing short ID for %s", name)
			tableName := "audit_t_" + short
			_, err := db.ExecContext(ctx, `
				DROP TABLE IF EXISTS `+tableName+`;
				CREATE TABLE `+tableName+` (
					subject_type TEXT NOT NULL,
					subject_id   TEXT NOT NULL,
					relation     TEXT NOT NULL,
					object_type  TEXT NOT NULL,
					object_id    TEXT NOT NULL
				);
			`)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tableName)
			})

			// Apply each recommendation against the test table, rewriting the
			// target from melange_tuples to our test table.
			for i, rec := range recs {
				ddl := strings.ReplaceAll(rec.DDL, "ON melange_tuples ", "ON "+tableName+" ")
				// Rename the index so multiple subtests don't collide if PG is
				// shared across runs. Keep the suffix to retain the wildcard
				// discriminator.
				ddl = strings.Replace(ddl, "idx_melange_tuples_", "idx_"+short+"_", 1)
				require.LessOrEqualf(t, indexNameFromDDL(ddl), 63,
					"rewritten index name exceeds PG's 63-byte limit; DDL: %s", ddl)

				_, err := db.ExecContext(ctx, ddl)
				require.NoErrorf(t, err, "recommendation[%d] DDL failed: %s\nfull DDL: %s", i, err, ddl)
			}

			// Verify the indexes actually exist in pg_indexes.
			rows, err := db.QueryContext(ctx, `
				SELECT indexname, indexdef
				FROM pg_indexes
				WHERE tablename = $1
				ORDER BY indexname
			`, tableName)
			require.NoError(t, err)
			defer rows.Close()

			var got []string
			for rows.Next() {
				var name, def string
				require.NoError(t, rows.Scan(&name, &def))
				got = append(got, name+" :: "+def)
			}
			require.NoError(t, rows.Err())
			assert.Lenf(t, got, len(recs),
				"expected %d indexes created from %d recommendations, got %d:\n%s",
				len(recs), len(recs), len(got), strings.Join(got, "\n"))

			// Confirm the partial-index WHERE clause survived the round-trip
			// to pg_indexes — catches cases where PG silently dropped the
			// predicate due to a syntax variant.
			for _, rec := range recs {
				if rec.WhereClause == "" {
					continue
				}
				wantSnippet := "WHERE (subject_id = '*'::text)"
				found := false
				for _, g := range got {
					if strings.Contains(g, wantSnippet) {
						found = true
						break
					}
				}
				assert.Truef(t, found,
					"partial-index WHERE clause %q not in any pg_indexes definition:\n%s",
					wantSnippet, strings.Join(got, "\n"))
			}
		})
	}
}

// TestRecommendIndexes_ProductionNamesUnder63Bytes verifies that the index
// names produced by RecommendIndexes (without any test rewriting) stay within
// PostgreSQL's 63-byte identifier limit. PG silently truncates longer names
// which would cause two distinct recommendations to collide.
func TestRecommendIndexes_ProductionNamesUnder63Bytes(t *testing.T) {
	// Use a wide-ranging schema so every kind of recommendation gets emitted.
	model := `model
  schema 1.1
type user
type group
  relations
    define member: [user, group#member]
type folder
  relations
    define parent: [folder]
    define viewer: [user, group#member, user:*]
    define blocked: [user]
type document
  relations
    define parent: [folder]
    define owner: [user]
    define blocked_viewer: blocked from parent
    define viewer: (owner or viewer from parent) but not blocked_viewer`

	recs := recommendForModel(t, model)
	require.NotEmpty(t, recs)

	for i, rec := range recs {
		name := indexNameFromDDL(rec.DDL)
		assert.LessOrEqualf(t, name, 63,
			"recommendation[%d] index name length %d > 63 bytes (PG identifier limit); DDL: %s",
			i, name, rec.DDL)
	}
}

// TestRecommendIndexes_PlannerPicksThem creates each recommended index against
// a populated test table, runs the access patterns generated SQL actually uses
// (object-keyed check, subject-keyed list_objects, wildcard partial lookup),
// and asserts via EXPLAIN that PG picks the index rather than seq-scanning.
//
// This is the strongest evidence that the recommendations are right:
// syntactically valid + planner-recognized + chosen over a sequential scan.
func TestRecommendIndexes_PlannerPicksThem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// A schema covering the three recommendation families: direct, wildcard,
	// userset (drives list_*_obj subject-keyed access).
	model := `model
  schema 1.1
type user
type group
  relations
    define member: [user]
type document
  relations
    define public: [user, user:*]
    define viewer: [user, group#member]`
	recs := recommendForModel(t, model)
	require.NotEmpty(t, recs)

	db := testutil.DB(t)
	ctx := context.Background()

	tableName := "audit_planner_picks"
	_, err := db.ExecContext(ctx, `
		DROP TABLE IF EXISTS `+tableName+`;
		CREATE TABLE `+tableName+` (
			subject_type TEXT NOT NULL,
			subject_id   TEXT NOT NULL,
			relation     TEXT NOT NULL,
			object_type  TEXT NOT NULL,
			object_id    TEXT NOT NULL
		);
	`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(ctx, "DROP TABLE IF EXISTS "+tableName) })

	// Populate enough rows that PG prefers an index over seq-scan. PG's
	// planner won't switch to an index for tiny tables. 10k rows is safely
	// above the threshold for these access patterns.
	_, err = db.ExecContext(ctx, `
		INSERT INTO `+tableName+` (subject_type, subject_id, relation, object_type, object_id)
		SELECT
			CASE WHEN (n % 50) = 0 THEN 'user' ELSE 'group' END,
			'u' || (n % 500),
			CASE WHEN (n % 7) = 0 THEN 'viewer' ELSE 'member' END,
			'document',
			'd' || (n % 1000)
		FROM generate_series(1, 10000) AS n
	`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO `+tableName+` (subject_type, subject_id, relation, object_type, object_id) VALUES ('user', '*', 'public', 'document', 'd1')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, "ANALYZE "+tableName)
	require.NoError(t, err)

	// Drop any existing indexes so each subtest can create the one it needs in
	// isolation. This proves each recommendation drives its own access pattern.
	dropAllIndexes := func() {
		rows, err := db.QueryContext(ctx, `
			SELECT indexname FROM pg_indexes
			WHERE tablename = $1 AND indexname LIKE 'idx_%'`, tableName)
		require.NoError(t, err)
		var names []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			names = append(names, n)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		for _, n := range names {
			_, err := db.ExecContext(ctx, "DROP INDEX "+n)
			require.NoError(t, err)
		}
	}

	short := "pp"
	type access struct {
		name     string
		query    string
		minIndex string // index name substring that MUST appear in EXPLAIN
	}

	// Look up recommendations by shape so the test stays decoupled from
	// recommendation ordering.
	var objKeyedRec, subjKeyedRec, wildcardRec *sqlgen.IndexRecommendation
	for i := range recs {
		switch {
		case recs[i].WhereClause != "":
			wildcardRec = &recs[i]
		case len(recs[i].Columns) > 0 && recs[i].Columns[0] == "object_type":
			objKeyedRec = &recs[i]
		case len(recs[i].Columns) > 0 && recs[i].Columns[0] == "subject_type":
			subjKeyedRec = &recs[i]
		}
	}
	require.NotNil(t, objKeyedRec, "missing object-keyed recommendation")
	require.NotNil(t, subjKeyedRec, "missing subject-keyed recommendation")
	require.NotNil(t, wildcardRec, "missing wildcard recommendation")

	rewriteDDL := func(rec *sqlgen.IndexRecommendation) string {
		ddl := strings.ReplaceAll(rec.DDL, "ON melange_tuples ", "ON "+tableName+" ")
		return strings.Replace(ddl, "idx_melange_tuples_", "idx_"+short+"_", 1)
	}

	cases := []access{
		{
			name:     "object_keyed_check_alone",
			query:    `SELECT 1 FROM ` + tableName + ` WHERE object_type='document' AND object_id='d10' AND relation='viewer' AND subject_type='user' AND subject_id='u5' LIMIT 1`,
			minIndex: "idx_" + short + "_by_object_type_object_id",
		},
		{
			name:     "subject_keyed_list_objects_alone",
			query:    `SELECT DISTINCT object_id FROM ` + tableName + ` WHERE subject_type='user' AND subject_id='u5' AND relation='viewer' AND object_type='document'`,
			minIndex: "idx_" + short + "_by_subject_type_subject_id",
		},
		{
			name:     "wildcard_partial_alone",
			query:    `SELECT 1 FROM ` + tableName + ` WHERE object_type='document' AND object_id='d1' AND relation='public' AND subject_id='*' LIMIT 1`,
			minIndex: "idx_" + short + "_by_object_type_object_id_wildcard",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dropAllIndexes()

			// Create only the recommendation that should service this access
			// pattern. If the planner still has to seq-scan, the recommendation
			// is wrong.
			var rec *sqlgen.IndexRecommendation
			switch tc.name {
			case "object_keyed_check_alone":
				rec = objKeyedRec
			case "subject_keyed_list_objects_alone":
				rec = subjKeyedRec
			case "wildcard_partial_alone":
				rec = wildcardRec
			}
			_, err := db.ExecContext(ctx, rewriteDDL(rec))
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "ANALYZE "+tableName)
			require.NoError(t, err)

			rows, err := db.QueryContext(ctx, "EXPLAIN "+tc.query)
			require.NoError(t, err)
			defer rows.Close()

			var plan strings.Builder
			for rows.Next() {
				var line string
				require.NoError(t, rows.Scan(&line))
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			require.NoError(t, rows.Err())

			explained := plan.String()
			assert.Containsf(t, explained, tc.minIndex,
				"expected EXPLAIN to mention %q; got:\n%s", tc.minIndex, explained)
			assert.NotContainsf(t, explained, "Seq Scan",
				"expected an index plan, got seq scan:\n%s", explained)
		})
	}
}

// TestRecommendIndexes_AppliesToProductionView documents the explicit failure
// mode: recommendations target melange_tuples which is a view in production.
// PG refuses CREATE INDEX on a view, and users must translate the DDL to the
// underlying source tables themselves. This test asserts that failure happens
// loudly so we don't accidentally claim production-applicability.
func TestRecommendIndexes_AppliesToProductionView(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	model := `model
  schema 1.1
type user
type document
  relations
    define owner: [user]`

	recs := recommendForModel(t, model)
	require.NotEmpty(t, recs)

	db := testutil.DB(t)
	ctx := context.Background()

	// melange_tuples is a VIEW in the test DB. CREATE INDEX must fail.
	_, err := db.ExecContext(ctx, recs[0].DDL)
	require.Errorf(t, err, "CREATE INDEX on melange_tuples view must fail (got success: %s)", recs[0].DDL)
	require.Containsf(t, err.Error(), "melange_tuples",
		"error should mention melange_tuples (got: %s)", err.Error())
}

// indexNameFromDDL extracts the index name length from a CREATE INDEX DDL.
// Used to assert names stay within PG's 63-byte identifier limit.
func indexNameFromDDL(ddl string) int {
	// CREATE INDEX IF NOT EXISTS <name> ON ...
	const prefix = "CREATE INDEX IF NOT EXISTS "
	if !strings.HasPrefix(ddl, prefix) {
		return 0
	}
	rest := ddl[len(prefix):]
	end := strings.Index(rest, " ")
	if end < 0 {
		return 0
	}
	return len(rest[:end])
}

func recommendForModel(t *testing.T, model string) []sqlgen.IndexRecommendation {
	t.Helper()

	types, err := parser.ParseSchemaString(model)
	require.NoError(t, err, "parse model")
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inline := compiler.BuildInlineSQLData(closureRows, analyses)

	generated, err := compiler.GenerateSQL(analyses, inline, "")
	require.NoError(t, err, "generate SQL")

	// Sanity: compiler.GenerateSQL must surface the recommendations through
	// the same struct that GenerateSQL in lib/sqlgen returns.
	_ = analysis.RelationFeatures{} // keep the import even if not directly referenced
	return generated.IndexRecommendations
}
