package testutil

import (
	"database/sql"
	"testing"
	"time"
)

// BenchLoop runs op b.N times. When db is non-nil and has pg_stat_statements
// (see EnableServerStats), it also reports where the time went:
//
//   - db-ns/op: server time (planning, if tracked, plus execution) of the
//     statements op sent, from pg_stat_statements
//   - net-ns/op: the rest of the wall time: network round trips plus client
//     and driver work
//   - stmts/op: statements op sent
//
// db-ns/op and net-ns/op add up to the wall time of the loop, which matches
// ns/op apart from timer granularity. test/cmd/benchreport turns these into
// tables.
func BenchLoop(b *testing.B, db *sql.DB, op func()) {
	b.Helper()
	before, ok := serverStats(db)
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		op()
	}
	elapsed := time.Since(start)
	b.StopTimer()
	if !ok {
		return
	}
	after, ok := serverStats(db)
	if !ok {
		return
	}
	n := float64(b.N)
	dbNs := (after.ms - before.ms) * 1e6 / n
	// ReportMetric must follow ResetTimer, which deletes reported metrics.
	b.ReportMetric(dbNs, "db-ns/op")
	b.ReportMetric(float64(elapsed.Nanoseconds())/n-dbNs, "net-ns/op")
	b.ReportMetric(float64(after.calls-before.calls)/n, "stmts/op")
}

// EnableServerStats installs pg_stat_statements in db. It only works when the
// server preloads the library (Cloud SQL does; the default test container does
// not), and BenchLoop skips the breakdown when it is unavailable. A nil db is
// a no-op.
func EnableServerStats(db *sql.DB) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_stat_statements`)
}

type statsSnapshot struct {
	calls int64
	ms    float64
}

// serverStats totals pg_stat_statements for the current database, leaving out
// its own query. Each benchmark has its own database (from EmptyDB or
// DSNWithDatabaseSchema, not the shared testutil.DB) and runs alone, so the
// difference between two snapshots is the benchmark's server work.
func serverStats(db *sql.DB) (statsSnapshot, bool) {
	var s statsSnapshot
	if db == nil {
		return s, false
	}
	err := db.QueryRow(`
		SELECT coalesce(sum(calls), 0)::bigint,
		       coalesce(sum(total_exec_time + total_plan_time), 0)::float8
		FROM pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND query NOT LIKE '%pg_stat_statements%'`).Scan(&s.calls, &s.ms)
	return s, err == nil
}
