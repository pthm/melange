package studio

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"

	"github.com/lib/pq"
)

// labelColumns are tried in order; the first one a domain table has becomes
// that type's display label.
var labelColumns = []string{"name", "display_name", "title", "label", "username", "email", "slug"}

// labelSource is where a type's human-readable labels live.
type labelSource struct {
	table  string // quoted, schema-qualified
	key    string // quoted; the column tuple ids are values of
	column string // quoted label column
	Desc   string // "table.column", for the UI
	Via    string // how it was found, for the UI
}

type tableRef struct{ schema, name string }

func (t tableRef) quoted() string {
	return pq.QuoteIdentifier(t.schema) + "." + pq.QuoteIdentifier(t.name)
}

// labeler resolves "type:id" refs to display labels. Sources are discovered
// from the melange_tuples view itself (see discoverFromView), falling back to
// a naming convention for types the view doesn't reveal.
//
// ponytail: discovery runs once per process; restart to pick up schema
// changes. Add a config override (type → table, key, label column) when
// neither discovery path finds the right table.
type labeler struct {
	db       *sql.DB
	dbSchema string

	mu       sync.Mutex
	viewDone bool
	sources  map[string]*labelSource // nil entry = looked up, nothing found
}

func newLabeler(db *sql.DB, dbSchema string) *labeler {
	return &labeler{db: db, dbSchema: dbSchema, sources: map[string]*labelSource{}}
}

// source returns the label source for typ, discovering it on first use.
func (l *labeler) source(ctx context.Context, typ string) (*labelSource, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.viewDone {
		found, err := l.discoverFromView(ctx)
		if err != nil {
			return nil, err
		}
		for t, src := range found {
			l.sources[t] = src
		}
		l.viewDone = true
	}
	if src, ok := l.sources[typ]; ok {
		return src, nil
	}
	src, err := l.discoverByName(ctx, typ)
	if err != nil {
		return nil, err
	}
	l.sources[typ] = src
	return src, nil
}

// planNode is the slice of EXPLAIN (FORMAT JSON) output discovery reads.
type planNode struct {
	NodeType string     `json:"Node Type"`
	Relation string     `json:"Relation Name"`
	Schema   string     `json:"Schema"`
	Alias    string     `json:"Alias"`
	Output   []string   `json:"Output"`
	Plans    []planNode `json:"Plans"`
}

var (
	// 'user'::text
	literalExpr = regexp.MustCompile(`^'((?:[^']|'')*)'(?:::text)?$`)
	// (organization_members.user_id)::text, "Odd"."Col", issues_1.id
	columnExpr = regexp.MustCompile(`^\(?("[^"]+"|\w+)\.("[^"]+"|\w+)\)?(?:::\w+)?$`)
)

// colRef is one base-table column that feeds a tuple's type id.
type colRef struct {
	table tableRef
	col   string
}

// discoverFromView finds each type's domain table from the tuples view's own
// definition. EXPLAIN VERBOSE gives the planned view with nested views
// inlined: one scan per UNION ALL branch, its output listing the five tuple
// columns as expressions such as 'user'::text and (team_members.user_id)::text.
// A branch whose type is a literal and whose id is a plain column tells us
// "user ids are values of team_members.user_id"; following that column's
// primary key or foreign key (keyTarget) lands on users.id, the table the
// label lives in. Branches that compute types or ids (a CASE, a type column
// read from the data) are skipped and left to the naming fallback.
func (l *labeler) discoverFromView(ctx context.Context) (map[string]*labelSource, error) {
	var raw []byte
	err := l.db.QueryRowContext(ctx,
		`EXPLAIN (VERBOSE, FORMAT JSON) SELECT * FROM `+pq.QuoteIdentifier(l.dbSchema)+`.melange_tuples`).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) == 0 {
		return nil, errors.Join(errors.New("reading tuples view plan"), err)
	}
	root := plans[0].Plan

	aliases := map[string]tableRef{}
	var collect func(n planNode)
	collect = func(n planNode) {
		if n.Relation != "" {
			aliases[n.Alias] = tableRef{schema: n.Schema, name: n.Relation}
		}
		for _, c := range n.Plans {
			collect(c)
		}
	}
	collect(root)

	branches := []planNode{root}
	if a := findAppend(root); a != nil {
		branches = a.Plans
	}

	// Per type, the candidate id columns in first-seen order.
	var order []string
	cands := map[string][]colRef{}
	for _, b := range branches {
		if len(b.Output) != 5 {
			continue
		}
		for _, p := range [][2]int{{0, 1}, {3, 4}} { // subject_type/id, object_type/id
			typ, okT := parseLiteral(b.Output[p[0]])
			alias, col, okC := parseColumn(b.Output[p[1]])
			t, okA := aliases[alias]
			if !okT || !okC || !okA {
				continue
			}
			if _, seen := cands[typ]; !seen {
				order = append(order, typ)
			}
			cands[typ] = append(cands[typ], colRef{table: t, col: col})
		}
	}

	targets := map[colRef]*colRef{} // memoised keyTarget results
	out := map[string]*labelSource{}
	for _, typ := range order {
		// Every branch votes for the table its id column points at; most votes
		// wins, first seen breaks ties. Branches normally agree.
		votes := map[colRef]int{}
		var best *colRef
		for _, c := range cands[typ] {
			tgt, ok := targets[c]
			if !ok {
				if tgt, err = l.keyTarget(ctx, c); err != nil {
					return nil, err
				}
				targets[c] = tgt
			}
			if tgt == nil {
				continue
			}
			votes[*tgt]++
			if best == nil || votes[*tgt] > votes[*best] {
				best = tgt
			}
		}
		if best == nil {
			continue
		}
		column, err := l.labelColumn(ctx, best.table)
		if err != nil {
			return nil, err
		}
		if column == "" {
			continue
		}
		out[typ] = &labelSource{
			table:  best.table.quoted(),
			key:    pq.QuoteIdentifier(best.col),
			column: pq.QuoteIdentifier(column),
			Desc:   best.table.name + "." + column,
			Via:    "found through the tuples view",
		}
	}
	return out, nil
}

func findAppend(n planNode) *planNode {
	if n.NodeType == "Append" {
		return &n
	}
	for _, c := range n.Plans {
		if a := findAppend(c); a != nil {
			return a
		}
	}
	return nil
}

func parseLiteral(expr string) (string, bool) {
	m := literalExpr.FindStringSubmatch(expr)
	if m == nil {
		return "", false
	}
	return strings.ReplaceAll(m[1], "''", "'"), true
}

func parseColumn(expr string) (alias, col string, ok bool) {
	m := columnExpr.FindStringSubmatch(expr)
	if m == nil {
		return "", "", false
	}
	unquote := func(s string) string { return strings.ReplaceAll(strings.Trim(s, `"`), `""`, `"`) }
	return unquote(m[1]), unquote(m[2]), true
}

// keyTarget follows c to the column its values identify: c itself when it is
// its table's single-column primary key or unique key, else the column a
// single-column foreign key on c references. nil when c is neither.
func (l *labeler) keyTarget(ctx context.Context, c colRef) (*colRef, error) {
	var contype string
	var fSchema, fTable, fCol sql.NullString
	err := l.db.QueryRowContext(ctx, `
		SELECT c.contype, fn.nspname, fc.relname, fa.attname
		FROM pg_constraint c
		JOIN pg_class rc ON rc.oid = c.conrelid
		JOIN pg_namespace rn ON rn.oid = rc.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		LEFT JOIN pg_class fc ON fc.oid = c.confrelid
		LEFT JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		LEFT JOIN pg_attribute fa ON fa.attrelid = c.confrelid AND fa.attnum = c.confkey[1]
		WHERE rn.nspname = $1 AND rc.relname = $2 AND a.attname = $3
		  AND c.contype IN ('p', 'u', 'f') AND cardinality(c.conkey) = 1
		ORDER BY c.contype = 'p' DESC, c.contype = 'u' DESC
		LIMIT 1`, c.table.schema, c.table.name, c.col).Scan(&contype, &fSchema, &fTable, &fCol)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, err
	case contype == "f":
		return &colRef{table: tableRef{schema: fSchema.String, name: fTable.String}, col: fCol.String}, nil
	default:
		return &c, nil
	}
}

// labelColumn returns the first preset label column t has, or "".
func (l *labeler) labelColumn(ctx context.Context, t tableRef) (string, error) {
	var col string
	err := l.db.QueryRowContext(ctx, `
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name::text = ANY($3::text[])
		ORDER BY array_position($3::text[], column_name::text) LIMIT 1`,
		t.schema, t.name, pq.Array(labelColumns)).Scan(&col)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return col, err
}

// discoverByName is the fallback: type T is backed by table T or T+"s" in the
// database schema, keyed by an "id" column.
func (l *labeler) discoverByName(ctx context.Context, typ string) (*labelSource, error) {
	var table string
	err := l.db.QueryRowContext(ctx, `
		SELECT c.table_name FROM information_schema.columns c
		WHERE c.table_schema = $1 AND c.column_name = 'id'
		  AND c.table_name::text = ANY(ARRAY[$2::text, $2::text || 's'])
		ORDER BY c.table_name::text = $2::text DESC LIMIT 1`, l.dbSchema, typ).Scan(&table)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t := tableRef{schema: l.dbSchema, name: table}
	column, err := l.labelColumn(ctx, t)
	if err != nil || column == "" {
		return nil, err
	}
	return &labelSource{
		table:  t.quoted(),
		key:    pq.QuoteIdentifier("id"),
		column: pq.QuoteIdentifier(column),
		Desc:   table + "." + column,
		Via:    "matched by table name",
	}, nil
}

// labels holds resolved labels keyed by "type:id".
type labels map[string]string

func (ls labels) of(typ, id string) string { return ls[typ+":"+id] }

// resolve fetches labels for refs, one query per type. Types without a source,
// ids without a row, and NULL labels are simply absent: the UI falls back to
// the raw id.
func (l *labeler) resolve(ctx context.Context, refs []entityRef) (labels, error) {
	byType := map[string][]string{}
	for _, r := range refs {
		byType[r.Type] = append(byType[r.Type], r.ID)
	}
	out := labels{}
	for typ, ids := range byType {
		src, err := l.source(ctx, typ)
		if err != nil {
			return nil, err
		}
		if src == nil {
			continue
		}
		rows, err := l.db.QueryContext(ctx,
			`SELECT `+src.key+`::text, `+src.column+`::text FROM `+src.table+
				` WHERE `+src.key+`::text = ANY($1) AND `+src.column+` IS NOT NULL`, pq.Array(ids))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, label string
			if err := rows.Scan(&id, &label); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[typ+":"+id] = label
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// searchClause returns an extra SQL predicate matching ids whose label
// contains q (bound as $2), so browse search finds "Alice" as well as "alice".
// Empty when typ has no label source.
func (l *labeler) searchClause(ctx context.Context, typ string) (string, error) {
	src, err := l.source(ctx, typ)
	if err != nil || src == nil {
		return "", err
	}
	return ` OR id IN (SELECT ` + src.key + `::text FROM ` + src.table + ` WHERE ` + src.column + `::text ILIKE '%' || $2 || '%')`, nil
}
