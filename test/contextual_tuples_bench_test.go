package test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/test/testutil"
)

// This file isolates the *plumbing* cost of the contextual-tuples mechanism
// from the cost of the permission query itself, so that fixes proposed in
// specs/proposals/CONTEXTUAL_TUPLES_PERFORMANCE.md can be measured in
// isolation. Each strategy implements the same contract:
//
//   1. Acquire a session (the temp view must live on a single connection).
//   2. Make pg_temp.melange_tuples visible with the supplied contextual tuples
//      unioned onto the base view.
//   3. Run check_permission once.
//   4. Tear the temp objects down.
//
// We deliberately bypass melange.Checker so that wins or regressions cannot
// hide behind unrelated Checker work. The check itself is identical across
// strategies — only the plumbing varies.

// ctxStrategy implements one approach to making contextual tuples visible
// inside generated SQL functions. The plumbing must end up with a
// pg_temp.melange_tuples view whose rows are <base view> UNION ALL <tuples>,
// because generated check_permission/list_* bodies reference melange_tuples
// unqualified and rely on pg_temp shadowing (see specs proposal for details).
type ctxStrategy struct {
	name  string
	setup func(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error
}

// teardown is identical for every strategy: drop both potential temp objects.
// Inline-VALUES strategies create only the view, but DROP IF EXISTS is cheap.
func teardownContextual(ctx context.Context, conn *sql.Conn) {
	_, _ = conn.ExecContext(ctx, "DROP VIEW IF EXISTS pg_temp.melange_tuples")
	_, _ = conn.ExecContext(ctx, "DROP TABLE IF EXISTS pg_temp.melange_contextual_tuples")
}

// strategyBaseline mirrors melange/checker.go:830 prepareContextualTuples
// exactly: pg_class lookup, CREATE TEMP TABLE, one INSERT per tuple,
// CREATE TEMP VIEW UNION-ALL'ing base + temp table.
//
// Round trips: 1 (lookup) + 1 (create table) + N (inserts) + 1 (create view) = 3+N
func strategyBaseline(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error {
	baseSchema, err := lookupBaseSchema(ctx, conn)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TEMP TABLE melange_contextual_tuples (
			subject_type TEXT NOT NULL,
			subject_id   TEXT NOT NULL,
			relation     TEXT NOT NULL,
			object_type  TEXT NOT NULL,
			object_id    TEXT NOT NULL
		)
	`); err != nil {
		return err
	}
	for _, t := range tuples {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO melange_contextual_tuples
				(subject_type, subject_id, relation, object_type, object_id)
			VALUES ($1, $2, $3, $4, $5)
		`, t.Subject.Type, t.Subject.ID, t.Relation, t.Object.Type, t.Object.ID); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, fmt.Sprintf(`
		CREATE TEMP VIEW melange_tuples AS
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM %s.melange_tuples
		UNION ALL
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM melange_contextual_tuples
	`, quoteIdent(baseSchema)))
	return err
}

// strategyCachedSchema is identical to baseline but skips the pg_class
// lookup. Caches the schema result on a captured variable.
//
// Round trips: 1 (create table) + N (inserts) + 1 (create view) = 2+N
func makeStrategyCachedSchema(baseSchema string) ctxStrategy {
	return ctxStrategy{
		name: "cachedSchema",
		setup: func(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error {
			if _, err := conn.ExecContext(ctx, `
				CREATE TEMP TABLE melange_contextual_tuples (
					subject_type TEXT NOT NULL,
					subject_id   TEXT NOT NULL,
					relation     TEXT NOT NULL,
					object_type  TEXT NOT NULL,
					object_id    TEXT NOT NULL
				)
			`); err != nil {
				return err
			}
			for _, t := range tuples {
				if _, err := conn.ExecContext(ctx, `
					INSERT INTO melange_contextual_tuples
						(subject_type, subject_id, relation, object_type, object_id)
					VALUES ($1, $2, $3, $4, $5)
				`, t.Subject.Type, t.Subject.ID, t.Relation, t.Object.Type, t.Object.ID); err != nil {
					return err
				}
			}
			_, err := conn.ExecContext(ctx, fmt.Sprintf(`
				CREATE TEMP VIEW melange_tuples AS
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM %s.melange_tuples
				UNION ALL
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM melange_contextual_tuples
			`, quoteIdent(baseSchema)))
			return err
		},
	}
}

// strategyMultiRowInsert keeps the temp table but collapses N inserts into
// one multi-row INSERT. Schema is also cached.
//
// Round trips: 1 (create table) + 1 (multi-insert) + 1 (create view) = 3
func makeStrategyMultiRowInsert(baseSchema string) ctxStrategy {
	return ctxStrategy{
		name: "multiRowInsert",
		setup: func(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error {
			if _, err := conn.ExecContext(ctx, `
				CREATE TEMP TABLE melange_contextual_tuples (
					subject_type TEXT NOT NULL,
					subject_id   TEXT NOT NULL,
					relation     TEXT NOT NULL,
					object_type  TEXT NOT NULL,
					object_id    TEXT NOT NULL
				)
			`); err != nil {
				return err
			}
			if len(tuples) > 0 {
				var sb strings.Builder
				sb.WriteString(`INSERT INTO melange_contextual_tuples
					(subject_type, subject_id, relation, object_type, object_id) VALUES `)
				args := make([]any, 0, len(tuples)*5)
				for i, t := range tuples {
					if i > 0 {
						sb.WriteString(", ")
					}
					base := i*5 + 1
					fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d)", base, base+1, base+2, base+3, base+4)
					args = append(args, t.Subject.Type, t.Subject.ID, t.Relation, t.Object.Type, t.Object.ID)
				}
				if _, err := conn.ExecContext(ctx, sb.String(), args...); err != nil {
					return err
				}
			}
			_, err := conn.ExecContext(ctx, fmt.Sprintf(`
				CREATE TEMP VIEW melange_tuples AS
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM %s.melange_tuples
				UNION ALL
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM melange_contextual_tuples
			`, quoteIdent(baseSchema)))
			return err
		},
	}
}

// strategyInlineValues skips the temp table entirely. The temp view body
// inlines the contextual tuples as a VALUES list of SQL literals. Schema is
// cached.
//
// Round trips: 1 (create view).
//
// Postgres rejects bind parameters inside a view body, so the values are
// embedded as escaped SQL literals. Each tuple component is small (subject
// type/id, relation, object type/id) and already validated by the public
// API, so the only escape concern is single quotes — handled by sqlLiteral.
func makeStrategyInlineValues(baseSchema string) ctxStrategy {
	return ctxStrategy{
		name: "inlineValues",
		setup: func(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error {
			var sb strings.Builder
			fmt.Fprintf(&sb, `CREATE TEMP VIEW melange_tuples AS
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM %s.melange_tuples`, quoteIdent(baseSchema))
			if len(tuples) == 0 {
				_, err := conn.ExecContext(ctx, sb.String())
				return err
			}
			sb.WriteString(`
				UNION ALL
				SELECT subject_type, subject_id, relation, object_type, object_id FROM (VALUES `)
			for i, t := range tuples {
				if i > 0 {
					sb.WriteString(", ")
				}
				fmt.Fprintf(&sb, "(%s::text, %s::text, %s::text, %s::text, %s::text)",
					sqlLiteral(string(t.Subject.Type)),
					sqlLiteral(t.Subject.ID),
					sqlLiteral(string(t.Relation)),
					sqlLiteral(string(t.Object.Type)),
					sqlLiteral(t.Object.ID),
				)
			}
			sb.WriteString(") AS ctx(subject_type, subject_id, relation, object_type, object_id)")
			_, err := conn.ExecContext(ctx, sb.String())
			return err
		},
	}
}

// strategyArrayUnnest passes contextual tuples as five parallel text[] bind
// parameters and unnest()s them into a temp table, then creates the temp
// view. Avoids the inlineValues escaping concern at the cost of one extra
// round trip — interesting as a "safe fast path" data point.
//
// Round trips: 1 (CREATE TABLE AS) + 1 (CREATE VIEW) = 2
func makeStrategyArrayUnnest(baseSchema string) ctxStrategy {
	return ctxStrategy{
		name: "arrayUnnest",
		setup: func(ctx context.Context, conn *sql.Conn, tuples []melange.ContextualTuple) error {
			subjectTypes := make([]string, len(tuples))
			subjectIDs := make([]string, len(tuples))
			relations := make([]string, len(tuples))
			objectTypes := make([]string, len(tuples))
			objectIDs := make([]string, len(tuples))
			for i, t := range tuples {
				subjectTypes[i] = string(t.Subject.Type)
				subjectIDs[i] = t.Subject.ID
				relations[i] = string(t.Relation)
				objectTypes[i] = string(t.Object.Type)
				objectIDs[i] = t.Object.ID
			}
			if _, err := conn.ExecContext(ctx, `
				CREATE TEMP TABLE melange_contextual_tuples AS
				SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[])
					AS t(subject_type, subject_id, relation, object_type, object_id)
			`, pgTextArray(subjectTypes), pgTextArray(subjectIDs), pgTextArray(relations),
				pgTextArray(objectTypes), pgTextArray(objectIDs)); err != nil {
				return err
			}
			_, err := conn.ExecContext(ctx, fmt.Sprintf(`
				CREATE TEMP VIEW melange_tuples AS
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM %s.melange_tuples
				UNION ALL
				SELECT subject_type, subject_id, relation, object_type, object_id
				FROM melange_contextual_tuples
			`, quoteIdent(baseSchema)))
			return err
		},
	}
}

// sqlLiteral wraps a string in single quotes, doubling embedded single
// quotes per Postgres escaping rules.
func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// pgTextArray formats a Go []string as a Postgres text[] literal that pgx
// accepts as a bind value.
func pgTextArray(s []string) any {
	// pgx/v5 accepts []string directly as a text[] when the placeholder is
	// $N::text[]. Just return the slice — keeping the helper for clarity
	// at call sites and so a future driver swap has one place to change.
	return s
}

// lookupBaseSchema mirrors melange/checker.go:932 lookupTuplesSchema.
func lookupBaseSchema(ctx context.Context, conn *sql.Conn) (string, error) {
	var schema string
	err := conn.QueryRowContext(ctx, `
		SELECT n.nspname
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'melange_tuples'
		  AND c.relkind IN ('r', 'v', 'm')
		ORDER BY n.nspname
		LIMIT 1
	`).Scan(&schema)
	return schema, err
}

// quoteIdent wraps name in double quotes, doubling embedded quotes per Postgres
// escaping rules. Tiny helper — we don't need the full sqldsl pull for this.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// runCheckUnderStrategy is the inner loop body. Acquires a fresh conn,
// runs setup, runs check_permission, tears down, releases the conn — exactly
// matching what the production code path does for one contextual call.
func runCheckUnderStrategy(
	ctx context.Context,
	db *sql.DB,
	strategy ctxStrategy,
	tuples []melange.ContextualTuple,
	subjectType, subjectID, relation, objectType, objectID string,
) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()

	if err := strategy.setup(ctx, conn, tuples); err != nil {
		teardownContextual(ctx, conn)
		return false, err
	}
	defer teardownContextual(ctx, conn)

	var result int
	if err := conn.QueryRowContext(ctx,
		`SELECT public.check_permission($1, $2, $3, $4, $5)`,
		subjectType, subjectID, relation, objectType, objectID,
	).Scan(&result); err != nil {
		return false, err
	}
	return result == 1, nil
}

// makeFiller produces noisy contextual tuples that don't grant the relation
// under test. Used to vary N without changing the answer.
func makeFiller(n int) []melange.ContextualTuple {
	out := make([]melange.ContextualTuple, n)
	for i := range out {
		out[i] = melange.ContextualTuple{
			Subject:  melange.Object{Type: "user", ID: fmt.Sprintf("filler-%d", i)},
			Relation: "writer",
			Object:   melange.Object{Type: "repository", ID: fmt.Sprintf("nope-%d", i)},
		}
	}
	return out
}

// scenario is one (strategy, tuple-count) pairing the benchmark runs.
type scenario struct {
	strategy ctxStrategy
	n        int
}

// BenchmarkContextualTuples_Strategies runs every strategy across N tuple
// counts. The first call under each scenario also asserts that the
// permission check returns the expected answer, so a regression in the
// strategy's correctness fails fast rather than silently winning the bench.
func BenchmarkContextualTuples_Strategies(b *testing.B) {
	ctx := context.Background()
	db := testutil.DB(b)

	// Resolve the base schema once for all "cached" strategies.
	bootstrapConn, err := db.Conn(ctx)
	if err != nil {
		b.Fatalf("bootstrap conn: %v", err)
	}
	baseSchema, err := lookupBaseSchema(ctx, bootstrapConn)
	if err != nil {
		_ = bootstrapConn.Close()
		b.Fatalf("lookup base schema: %v", err)
	}
	_ = bootstrapConn.Close()

	strategies := []ctxStrategy{
		{name: "baseline", setup: strategyBaseline},
		makeStrategyCachedSchema(baseSchema),
		makeStrategyMultiRowInsert(baseSchema),
		makeStrategyArrayUnnest(baseSchema),
		makeStrategyInlineValues(baseSchema),
	}

	tupleCounts := []int{1, 5, 25, 100}

	// The contextual tuple under test grants reader on repository:r1 to
	// user:bob via the owner→...→reader implication chain in the embedded
	// schema. Filler tuples push N up without changing the answer.
	grantingTuple := melange.ContextualTuple{
		Subject:  melange.Object{Type: "user", ID: "bob"},
		Relation: "owner",
		Object:   melange.Object{Type: "repository", ID: "r1"},
	}

	for _, strategy := range strategies {
		for _, n := range tupleCounts {
			s := scenario{strategy: strategy, n: n}
			b.Run(fmt.Sprintf("%s/N=%d", s.strategy.name, s.n), func(b *testing.B) {
				tuples := append([]melange.ContextualTuple{grantingTuple}, makeFiller(s.n-1)...)

				// Sanity check: the strategy must actually make the
				// contextual tuple visible to check_permission. If it
				// doesn't, we'd be benchmarking a fast no-op.
				ok, err := runCheckUnderStrategy(ctx, db, s.strategy, tuples,
					"user", "bob", "reader", "repository", "r1")
				if err != nil {
					b.Fatalf("warmup: %v", err)
				}
				if !ok {
					b.Fatalf("warmup: contextual tuple did not grant reader — strategy %q is broken", s.strategy.name)
				}

				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ok, err := runCheckUnderStrategy(ctx, db, s.strategy, tuples,
						"user", "bob", "reader", "repository", "r1")
					if err != nil {
						b.Fatalf("check: %v", err)
					}
					if !ok {
						b.Fatalf("check returned false — temp view lost the contextual tuple")
					}
				}
			})
		}
	}
}

// BenchmarkContextualTuples_Lowerbound measures a single check_permission
// call on a freshly-acquired pooled connection — same connection lifecycle
// the strategies pay, but with no temp-view setup. This is the absolute
// floor any contextual strategy can approach.
func BenchmarkContextualTuples_Lowerbound(b *testing.B) {
	ctx := context.Background()
	db := testutil.DB(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			b.Fatalf("conn: %v", err)
		}
		var result int
		if err := conn.QueryRowContext(ctx,
			`SELECT public.check_permission($1, $2, $3, $4, $5)`,
			"user", "bob", "reader", "repository", "r1",
		).Scan(&result); err != nil {
			_ = conn.Close()
			b.Fatalf("check: %v", err)
		}
		_ = conn.Close()
	}
}
