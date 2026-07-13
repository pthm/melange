package test

import (
	"database/sql"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/test/testutil"
)

// kitchenSinkProbe is a (object type, relation) pair to differentially test. These
// span every list strategy and the feature combinations where recent bugs lived
// (self-ref recursive TTU, wildcard-via-TTU, userset-in-closure, intersection /
// exclusion in list_subjects).
var kitchenSinkProbes = []struct{ objType, relation string }{
	{"group", "member"},          // self-referential userset
	{"group", "active_member"},   // self-ref userset + wildcard + exclusion
	{"organization", "member"},   // implied + userset
	{"organization", "admin"},    // implied
	{"team", "member"},           // union + userset + cross-type TTU
	{"project", "viewer"},        // userset(other type) + implied
	{"project", "admin"},         // cross-type TTU + union
	{"folder", "viewer"},         // #12: self-ref recursive TTU + cross-type anchor + userset
	{"folder", "editor"},         // pure self-ref recursive TTU
	{"folder", "co_owner"},       // intersection composing against self-ref recursive targets
	{"folder", "protected"},      // recursive TTU minuend + exclusion (Recursive+Exclusion)
	{"document", "editor"},       // direct + userset + userset(other) + implied + TTU
	{"document", "viewer"},       // the big union incl. wildcard via TTU
	{"document", "can_view"},     // exclusion (but not blocked, incl. wildcard)
	{"document", "can_edit"},     // intersection (editor and active)
	{"document", "can_comment"},  // intersection-then-exclusion
	{"document", "can_manage"},   // intersection with TTU part
	{"document", "can_share"},    // exclusion with TTU subtrahend
	{"document", "super_view"},   // cross-type TTU to platform singleton
	{"report", "inherited_view"}, // pure TTU -> Composed
	{"report", "audience"},       // pure userset -> Composed
	{"comment", "can_read"},      // closure-inherited exclusion via TTU
}

// TestKitchenSink_ListVsCheck is the differential correctness harness: over the
// kitchen-sink dataset, for every probed (object type, relation), it asserts that
// list_objects and list_subjects agree with check_permission — the oracle. For
// plain subjects the agreement is exact (list == {o : check=1}); for wildcard /
// userset query subjects it is a subset (list ⊆ check), because list functions
// may legitimately under-report those. This is the assertion that would have
// failed on #12, wildcard-via-TTU, and the parent_closure over-report.
func TestKitchenSink_ListVsCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("kitchenSink differential test requires a database")
	}
	db := testutil.SetupKitchenSinkDB(t)
	testutil.LoadKitchenSinkTuples(t, db, testutil.GenerateKitchenSinkTuples(testutil.KitchenSinkScaleSmall))

	users := distinct(t, db, `SELECT DISTINCT subject_id FROM kitchen_sink_tuples WHERE subject_type='user' AND subject_id <> '*' ORDER BY subject_id`)
	require.NotEmpty(t, users)
	subjectSample := sample(users, 8)

	// Coverage counters guard against a vacuous pass (all list/check empty).
	var totalAllow, totalDeny, listedTotal int

	for _, p := range kitchenSinkProbes {
		t.Run(p.objType+"."+p.relation, func(t *testing.T) {
			objects := distinct(t, db, `SELECT DISTINCT object_id FROM kitchen_sink_tuples WHERE object_type=$1 ORDER BY object_id`, p.objType)
			require.NotEmpty(t, objects, "no objects for type %s", p.objType)

			// ---- list_objects vs check (plain subjects: exact) ----
			for _, u := range subjectSample {
				listed := setOf(listObjects(t, db, "user", u, p.relation, p.objType))
				listedTotal += len(listed)
				for _, o := range objects {
					allowed := checkPerm(t, db, "user", u, p.relation, p.objType, o) == 1
					if allowed {
						totalAllow++
					} else {
						totalDeny++
					}
					_, inList := listed[o]
					require.Equalf(t, allowed, inList,
						"list_objects(%s) vs check for user:%s %s %s:%s (listed=%v check=%v)",
						p.objType, u, p.relation, p.objType, o, inList, allowed)
				}
			}

			// ---- list_objects for wildcard subject: list ⊆ check ----
			for _, o := range setToSlice(setOf(listObjects(t, db, "user", "*", p.relation, p.objType))) {
				require.Equalf(t, 1, checkPerm(t, db, "user", "*", p.relation, p.objType, o),
					"list_objects over-reported wildcard user:* for %s:%s %s", p.objType, o, p.relation)
			}

			// ---- list_subjects vs check ----
			objSample := sample(objects, 8)
			for _, o := range objSample {
				listed := setOf(listSubjects(t, db, p.objType, o, p.relation, "user"))
				_, wildcardListed := listed["*"]
				// list ⊆ check for every listed subject, INCLUDING '*': a wildcard
				// that survived an exclusion (e.g. document.can_view where blocked
				// excludes user:*) is an over-report check_permission would reject.
				for subj := range listed {
					require.Equalf(t, 1, checkPerm(t, db, "user", subj, p.relation, p.objType, o),
						"list_subjects over-reported user:%s for %s:%s %s", subj, p.objType, o, p.relation)
				}
				if wildcardListed {
					continue // '*' means everyone; skip per-user completeness
				}
				for _, u := range subjectSample {
					allowed := checkPerm(t, db, "user", u, p.relation, p.objType, o) == 1
					_, inList := listed[u]
					require.Equalf(t, allowed, inList,
						"list_subjects vs check for user:%s %s %s:%s (listed=%v check=%v)",
						u, p.relation, p.objType, o, inList, allowed)
				}
			}
		})
	}

	t.Logf("coverage: %d allows, %d denies, %d listed objects across %d probes",
		totalAllow, totalDeny, listedTotal, len(kitchenSinkProbes))
	require.Greater(t, totalAllow, 100, "differential test is vacuous: too few granted (subject,object) pairs")
	require.Greater(t, totalDeny, 100, "differential test is vacuous: too few denied (subject,object) pairs")
	require.Greater(t, listedTotal, 50, "differential test is vacuous: list_objects returned almost nothing")
}

// ---- SQL helpers ----

func listObjects(t *testing.T, db *sql.DB, st, sid, rel, ot string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT object_id FROM list_accessible_objects($1,$2,$3,$4)`, st, sid, rel, ot)
	require.NoError(t, err)
	return scanStrings(t, rows)
}

func listSubjects(t *testing.T, db *sql.DB, ot, oid, rel, st string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT subject_id FROM list_accessible_subjects($1,$2,$3,$4)`, ot, oid, rel, st)
	require.NoError(t, err)
	return scanStrings(t, rows)
}

func checkPerm(t *testing.T, db *sql.DB, st, sid, rel, ot, oid string) int {
	t.Helper()
	var r int
	require.NoError(t, db.QueryRow(`SELECT check_permission($1,$2,$3,$4,$5)`, st, sid, rel, ot, oid).Scan(&r))
	return r
}

func distinct(t *testing.T, db *sql.DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(q, args...)
	require.NoError(t, err)
	return scanStrings(t, rows)
}

func scanStrings(t *testing.T, rows *sql.Rows) []string {
	t.Helper()
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func setOf(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[x] = struct{}{}
	}
	return m
}

func setToSlice(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sample returns up to n evenly-spread elements (deterministic).
func sample(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	out := make([]string, 0, n)
	step := len(xs) / n
	for i := 0; i < len(xs) && len(out) < n; i += step {
		out = append(out, xs[i])
	}
	return out
}
