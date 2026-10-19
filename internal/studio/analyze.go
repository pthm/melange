package studio

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// analyzeTimeout bounds one Analyze, both as the statement_timeout and as
// the request context (lib/pq sends a cancel when the context ends).
const analyzeTimeout = 10 * time.Second

// analysis is one check run under EXPLAIN ANALYZE.
type analysis struct {
	Subject, Relation, Object string
	Plan                      string
	Exec, Planning            float64 // ms, parsed from the plan
	Funcs                     []funcTime
	Tracked                   bool // false: track_functions could not be set
	// Allowed is the check's answer from a second, plain run (EXPLAIN
	// discards the result); nil when that run failed or timed out.
	Allowed *bool
	// Links maps a generated check function's name to its relation page.
	Links map[string]templ.SafeURL
}

// funcTime is one generated function's share of the check, from
// pg_stat_xact_user_functions.
type funcTime struct {
	Name        string
	Calls       int64
	Total, Self float64 // ms
}

var (
	execTimeRe = regexp.MustCompile(`Execution Time: ([\d.]+) ms`)
	planTimeRe = regexp.MustCompile(`Planning Time: ([\d.]+) ms`)
)

// planTimes reads execution and planning time (ms) out of EXPLAIN ANALYZE text.
func planTimes(plan string) (exec, planning float64) {
	if m := execTimeRe.FindStringSubmatch(plan); m != nil {
		exec, _ = strconv.ParseFloat(m[1], 64)
	}
	if m := planTimeRe.FindStringSubmatch(plan); m != nil {
		planning, _ = strconv.ParseFloat(m[1], 64)
	}
	return exec, planning
}

func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	subject, err1 := parseIdent("subject", r.FormValue("subject"))
	object, err2 := parseIdent("object", r.FormValue("object"))
	relation := strings.TrimSpace(r.FormValue("relation"))
	if err := firstErr(err1, err2, required(relation, "relation")); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	// check_permission denies an unknown pair instead of failing, which
	// would time as a fast, clean check; without a model, run it anyway.
	model, _ := s.loadModel(r.Context())
	if model != nil && !slices.Contains(relationsOf(model.Types, string(object.Type)), relation) {
		render(w, r, errorBox(fmt.Sprintf("%s#%s is not a relation in the deployed model", object.Type, relation)))
		return
	}
	start := time.Now()
	a, err := s.analyzeCheck(r.Context(), subject, relation, object)
	took := time.Since(start)
	var pqErr *pq.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &pqErr) && pqErr.Code == "57014":
		render(w, r, errorBox(fmt.Sprintf("Timed out after %s; this check is very slow.", analyzeTimeout)))
	case err != nil:
		render(w, r, errorBox(failedAfter(err, took)))
	default:
		if model != nil {
			a.Links = checkFuncLinks(model.Types)
		}
		render(w, r, analyzeView(a))
	}
}

// checkFuncLinks maps every generated check function name to its relation
// page. Names go through SafeIdentifier so long names hash as codegen did.
func checkFuncLinks(types []schema.TypeDefinition) map[string]templ.SafeURL {
	out := map[string]templ.SafeURL{}
	for _, t := range types {
		for _, rel := range t.Relations {
			u := relationURL(t.Name, rel.Name)
			out[sqldsl.SafeIdentifier("check_", t.Name, rel.Name, "")] = u
			out[sqldsl.SafeIdentifier("check_", t.Name, rel.Name, "_nw")] = u
		}
	}
	return out
}

// failedAfter words a check error the same way for every verb: the
// checker's message for a depth-limit error (raw from Analyze, which calls
// SQL directly), no "pq: " prefix, and how long it ran before failing.
func failedAfter(err error, took time.Duration) string {
	msg := err.Error()
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		msg = pqErr.Message
		if pqErr.Code == "M2002" {
			msg = "resolution too complex: depth limit exceeded"
		}
	}
	return fmt.Sprintf("Failed after %s: %s", ms(took), msg)
}

// analyzeCheck runs check_permission under EXPLAIN ANALYZE in a read-only
// transaction that is always rolled back. The plan of a plpgsql call is one
// opaque Result node, so the per-function split comes from
// pg_stat_xact_user_functions, which needs track_functions; that is
// superuser-only unless granted, so its SET sits in a savepoint and failing
// it only drops the split.
func (s *Server) analyzeCheck(ctx context.Context, subject melange.Object, relation string, object melange.Object) (*analysis, error) {
	ctx, cancel := context.WithTimeout(ctx, analyzeTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	// SET takes no bind parameters; the literal comes from a constant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", analyzeTimeout.Milliseconds())); err != nil {
		return nil, err
	}
	a := &analysis{Subject: subject.String(), Relation: relation, Object: object.String(), Tracked: true}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT tf"); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL track_functions = 'all'"); err != nil {
		a.Tracked = false
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT tf"); err != nil {
			return nil, err
		}
	}

	var before map[string]funcTime
	if a.Tracked {
		if before, err = s.funcStats(ctx, tx); err != nil {
			return nil, err
		}
	}
	check := "SELECT " + pq.QuoteIdentifier(s.dbSchema) + ".check_permission($1, $2, $3, $4, $5)"
	args := []any{string(subject.Type), subject.ID, relation, string(object.Type), object.ID}
	rows, err := tx.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+check, args...)
	if err != nil {
		return nil, err
	}
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			_ = rows.Close()
			return nil, err
		}
		lines = append(lines, line)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.Plan = strings.Join(lines, "\n")
	a.Exec, a.Planning = planTimes(a.Plan)

	if a.Tracked {
		after, err := s.funcStats(ctx, tx)
		if err != nil {
			return nil, err
		}
		a.Funcs = funcDelta(before, after)
	}
	// Last, so its function calls stay out of the delta above. A failure
	// (often the shared timeout on a slow check) only drops the answer.
	var allowed int
	if tx.QueryRowContext(ctx, check, args...).Scan(&allowed) == nil {
		ok := allowed == 1
		a.Allowed = &ok
	}
	return a, nil
}

// funcStats reads this backend's not-yet-flushed function stats. They can
// still hold earlier transactions' calls on the same pooled connection
// (Postgres flushes them only when the backend idles outside a transaction,
// at most once a second), so analyzeCheck diffs a read before and after.
func (s *Server) funcStats(ctx context.Context, tx *sql.Tx) (map[string]funcTime, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT funcname, calls, total_time, self_time FROM pg_stat_xact_user_functions WHERE schemaname = $1", s.dbSchema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]funcTime{}
	for rows.Next() {
		var f funcTime
		if err := rows.Scan(&f.Name, &f.Calls, &f.Total, &f.Self); err != nil {
			return nil, err
		}
		// ponytail: overloads share a name and are summed; generated names are unique.
		g := out[f.Name]
		out[f.Name] = funcTime{f.Name, g.Calls + f.Calls, g.Total + f.Total, g.Self + f.Self}
	}
	return out, rows.Err()
}

// maxFuncs caps the per-function table; the tail is noise.
const maxFuncs = 15

// funcDelta returns what each function gained between two funcStats reads,
// by self time, most first.
func funcDelta(before, after map[string]funcTime) []funcTime {
	var out []funcTime
	for name, f := range after {
		b := before[name]
		if d := (funcTime{name, f.Calls - b.Calls, f.Total - b.Total, f.Self - b.Self}); d.Calls > 0 {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(x, y funcTime) int {
		return cmp.Or(cmp.Compare(y.Self, x.Self), strings.Compare(x.Name, y.Name))
	})
	return out[:min(maxFuncs, len(out))]
}
