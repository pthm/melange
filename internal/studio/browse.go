package studio

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lib/pq"
)

// ponytail: every list is capped at listLimit with no paging; add cursors
// (ListObjects/ListSubjects already return one) when real datasets need them.
const listLimit = 200

type entityRef struct {
	Type, ID string
	Count    int // tuples mentioning it (browse list only)
}

// accessRow is one (relation, other type) pair on an entity's detail page.
type accessRow struct {
	Relation, Type string
	IDs            []string
	Err            string
}

type typeCount struct {
	Type  string
	Count int // distinct ids
}

// countsTTL bounds how stale the Tuples sidebar's counts can be.
const countsTTL = 30 * time.Second

// typeCounts lists every type in the tuples with its number of distinct ids,
// in model order. The scan is a full pass over melange_tuples (0.4s at 200k
// tuples), and it backs the sidebar of every Tuples page, so results are
// cached for countsTTL.
//
// ponytail: one process-wide cache; per-type estimates (pg_stats) if a view
// gets big enough that even one scan per TTL hurts.
func (s *Server) typeCounts(ctx context.Context, o modelOrder) ([]typeCount, error) {
	s.countsMu.Lock()
	defer s.countsMu.Unlock()
	if time.Since(s.countsAt) > countsTTL {
		counts, err := s.queryTypeCounts(ctx)
		if err != nil {
			return nil, err
		}
		s.counts, s.countsAt = counts, time.Now()
	}
	out := slices.Clone(s.counts)
	slices.SortStableFunc(out, func(a, b typeCount) int { return o.compareTypes(a.Type, b.Type) })
	return out, nil
}

func (s *Server) queryTypeCounts(ctx context.Context) ([]typeCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT type, count(DISTINCT id) FROM (
			SELECT subject_type AS type, split_part(subject_id, '#', 1) AS id FROM `+s.tuples()+`
			UNION ALL
			SELECT object_type, object_id FROM `+s.tuples()+`
		) e WHERE id <> '*' GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []typeCount
	for rows.Next() {
		var t typeCount
		if err := rows.Scan(&t.Type, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Server) tuplesIndex(w http.ResponseWriter, r *http.Request) {
	model, _ := s.loadModel(r.Context())
	types, err := s.typeCounts(r.Context(), newModelOrder(model))
	render(w, r, tuplesPage(s.env, types, errString(err)))
}

func (s *Server) tuples() string {
	return pq.QuoteIdentifier(s.dbSchema) + ".melange_tuples"
}

func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	d := browseData{Env: s.env, Type: typ}
	var err error
	if d.Ents, d.Labels, err = s.listEntities(r.Context(), typ, ""); err == nil {
		model, _ := s.loadModel(r.Context())
		d.Types, err = s.typeCounts(r.Context(), newModelOrder(model))
	}
	d.Err = errString(err)
	d.Source, _ = s.labels.source(r.Context(), typ)
	render(w, r, browsePage(d))
}

func (s *Server) browseList(w http.ResponseWriter, r *http.Request) {
	ents, ls, err := s.listEntities(r.Context(), r.PathValue("type"), strings.TrimSpace(r.FormValue("q")))
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	render(w, r, entityList(ents, ls))
}

// listEntities returns the distinct ids of typ that appear in any tuple, as
// subject or object, optionally filtered by a case-insensitive substring of
// the id or its label. Userset subjects (group:eng#member) count toward their
// object (group:eng).
func (s *Server) listEntities(ctx context.Context, typ, q string) ([]entityRef, labels, error) {
	labelMatch, err := s.labels.searchClause(ctx, typ)
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, count(*) FROM (
			SELECT split_part(subject_id, '#', 1) AS id FROM `+s.tuples()+` WHERE subject_type = $1
			UNION ALL
			SELECT object_id FROM `+s.tuples()+` WHERE object_type = $1
		) e
		WHERE id <> '*' AND ($2 = '' OR id ILIKE '%' || $2 || '%'`+labelMatch+`)
		GROUP BY id
		ORDER BY CASE WHEN id ~ '^[0-9]{1,18}$' THEN lpad(id, 18, '0') ELSE id END
		LIMIT $3`, typ, q, listLimit)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []entityRef
	for rows.Next() {
		e := entityRef{Type: typ}
		if err := rows.Scan(&e.ID, &e.Count); err != nil {
			return nil, nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	ls, err := s.labels.resolve(ctx, out)
	return out, ls, err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
