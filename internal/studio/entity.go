package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

// Entity page tabs. The two access tabs list one type at a time, chosen in a
// sub-navigation of types, because a full access listing is one list call per
// relation in the model: on a 1,500-relation model that is ~1,500 calls.
const (
	tabCan    = "can"    // what the entity can access, as subject
	tabBy     = "by"     // who can access the entity, as object
	tabDirect = "direct" // raw tuples mentioning the entity
)

type entityData struct {
	Env    string
	Entity entityRef
	Tab    string
	Group  string   // selected type on the access tabs
	Groups []string // types in the access tab's sub-navigation, model order
	// Direct tuples: the first page of each side and the Direct tab's
	// filter (?q=).
	AsSubject, AsObject       directPage
	SubjectTotal, ObjectTotal int // all tuples per side
	SubjectMatch, ObjectMatch int // those passing Query
	Query                     string
	ModelErr                  string
	Labels                    labels
	Types                     []typeCount
}

func (s *Server) entity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ent := entityRef{Type: r.FormValue("type"), ID: r.FormValue("id")}
	if ent.Type == "" || ent.ID == "" {
		render(w, r, errorBox("type and id are required"))
		return
	}
	d := entityData{Env: s.env, Entity: ent, Tab: r.FormValue("tab"), Group: r.FormValue("group")}
	if d.Tab != tabBy && d.Tab != tabDirect {
		d.Tab = tabCan
	}
	model, modelErr := s.loadModel(ctx)
	order := newModelOrder(model)

	if d.Tab == tabDirect {
		d.Query = strings.TrimSpace(r.FormValue("q"))
	}
	var err error
	if d.Types, err = s.typeCounts(ctx, order); err == nil {
		d.SubjectTotal, d.ObjectTotal, err = s.directCounts(ctx, ent, "")
		d.SubjectMatch, d.ObjectMatch = d.SubjectTotal, d.ObjectTotal
	}
	if err == nil && d.Query != "" {
		d.SubjectMatch, d.ObjectMatch, err = s.directCounts(ctx, ent, d.Query)
	}
	if err == nil {
		d.AsSubject, err = s.directTuples(ctx, order, sideSubject, ent, d.Query, 0)
	}
	if err == nil {
		d.AsObject, err = s.directTuples(ctx, order, sideObject, ent, d.Query, 0)
	}
	switch {
	case err != nil:
	case model == nil:
		d.ModelErr = modelErr
	case d.Tab == tabCan:
		for _, t := range model.Types {
			if len(t.Relations) > 0 {
				d.Groups = append(d.Groups, t.Name)
			}
		}
	case d.Tab == tabBy && len(relationsOf(model.Types, ent.Type)) > 0:
		d.Groups, err = s.subjectTypes(ctx, order)
	}
	if err == nil {
		refs := append(tupleRefs(append(slices.Clone(d.AsSubject.Tuples), d.AsObject.Tuples...)), d.Entity)
		d.Labels, err = s.labels.resolve(ctx, refs)
	}
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	if d.Group == "" && len(d.Groups) > 0 {
		d.Group = d.Groups[0]
	}
	render(w, r, entityPage(d))
}

// groupParams reads the (entity, tab, type) a group request is about.
func groupParams(r *http.Request) (self entityRef, tab, group string, ok bool) {
	self = entityRef{Type: r.FormValue("type"), ID: r.FormValue("id")}
	tab, group = r.FormValue("tab"), r.FormValue("group")
	return self, tab, group, self.Type != "" && self.ID != "" && group != "" && (tab == tabCan || tab == tabBy)
}

// entityGroup serves one page of one type's entities on an access tab,
// filtered by ?q= (id, name or relation, case-insensitive). The group is
// computed once and cached (see group), so later pages and filtering are
// in-memory; only a filter needs every entity's label.
func (s *Server) entityGroup(w http.ResponseWriter, r *http.Request) {
	self, tab, group, ok := groupParams(r)
	if !ok {
		render(w, r, errorBox("type, id, tab and group are required"))
		return
	}
	q, offset := strings.TrimSpace(r.FormValue("q")), offsetParam(r)
	g := s.group(r.Context(), self, tab, group)
	// A filter matches names, so it needs every entity's label; without
	// one, only the page's.
	need := g.Entities
	if q == "" {
		need, _ = pageOf(g.Entities, offset)
	}
	ls, err := s.labels.resolve(r.Context(), groupRefs(g.Type, need))
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	ents := g.Entities
	if q != "" {
		ents = filterGroup(g.Type, ents, ls, q)
	}
	rows, more := pageOf(ents, offset)
	render(w, r, accessList(g, len(ents), rows, ls, self, tab == tabCan, listPage{
		First: offset == 0, More: more, Next: groupPageURL(self, tab, group, q, offset+pageSize),
	}))
}

func groupRefs(typ string, ents []entityGroup) []entityRef {
	refs := make([]entityRef, len(ents))
	for i, e := range ents {
		refs[i] = entityRef{Type: typ, ID: e.ID}
	}
	return refs
}

// filterGroup keeps the entities whose id, label or a relation contains q.
func filterGroup(typ string, ents []entityGroup, ls labels, q string) []entityGroup {
	q = strings.ToLower(q)
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), q) }
	var out []entityGroup
	for _, e := range ents {
		if has(e.ID) || has(ls.of(typ, e.ID)) || slices.ContainsFunc(e.Relations, has) {
			out = append(out, e)
		}
	}
	return out
}

// entityDirect serves a later page of one side of the Direct tuples tab.
func (s *Server) entityDirect(w http.ResponseWriter, r *http.Request) {
	ent := entityRef{Type: r.FormValue("type"), ID: r.FormValue("id")}
	side := r.FormValue("side")
	if ent.Type == "" || ent.ID == "" || (side != sideSubject && side != sideObject) {
		render(w, r, errorBox("type, id and side are required"))
		return
	}
	model, _ := s.loadModel(r.Context())
	p, err := s.directTuples(r.Context(), newModelOrder(model), side, ent, strings.TrimSpace(r.FormValue("q")), offsetParam(r))
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	ls, err := s.labels.resolve(r.Context(), tupleRefs(p.Tuples))
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	render(w, r, directRows(p, ls))
}

// tupleRefs lists both ends of each tuple, for label lookup.
func tupleRefs(ts []melange.TupleRef) []entityRef {
	refs := make([]entityRef, 0, 2*len(ts))
	for _, t := range ts {
		refs = append(refs,
			entityRef{Type: t.SubjectType, ID: strings.SplitN(t.SubjectID, "#", 2)[0]},
			entityRef{Type: t.ObjectType, ID: t.ObjectID})
	}
	return refs
}

// entityCount serves the count badge for one type in the sub-navigation.
// It shares group's cache, so opening a counted type is instant. Badges are
// a convenience next to the selected list, so they compute one group at a
// time (countSlot) and a badge scrolled past or navigated away from drops
// out of the queue.
func (s *Server) entityCount(w http.ResponseWriter, r *http.Request) {
	self, tab, group, ok := groupParams(r)
	if !ok {
		return
	}
	select {
	case countSlot <- struct{}{}:
		defer func() { <-countSlot }()
	case <-r.Context().Done():
		return
	}
	render(w, r, groupBadge(s.group(r.Context(), self, tab, group)))
}

// Bounds on the list queries behind access groups: each call gets
// listTimeout, at most listParallel run at once across the whole server
// (listSlots), and count badges compute one group at a time (countSlot).
const (
	listTimeout   = 10 * time.Second
	listParallel  = 4
	groupTTL      = time.Minute
	groupCacheMax = 128 // entries; the cache is dropped wholesale past this
	// groupLimit caps each relation's list in a group. The list functions
	// compute their whole result before paging it, so a high cap costs the
	// database little; the group is held in memory and served pageSize rows
	// at a time.
	groupLimit = 5000
)

// ponytail: package-level, since a process serves one studio; per-Server if
// that ever changes.
var (
	listSlots = make(chan struct{}, listParallel)
	countSlot = make(chan struct{}, 1)
)

// groupCall is one computation of a group, shared by concurrent requests.
type groupCall struct {
	done    chan struct{}
	g       typeGroup
	at      time.Time // zero when the result must not be reused (errors)
	waiters int       // requests waiting on it; guarded by groupsMu
	cancel  context.CancelFunc
}

// group computes one type group of an entity's access; see shared.
func (s *Server) group(ctx context.Context, self entityRef, tab, other string) typeGroup {
	key := strings.Join([]string{tab, self.Type, self.ID, other}, "\x00")
	return s.shared(ctx, key, other, func(ctx context.Context) typeGroup {
		return s.computeGroup(ctx, self, tab, other)
	})
}

// shared runs compute once per key for all concurrent requests (the count
// badge and the list ask at once) and caches clean results for groupTTL.
//
// The computation outlives any one request, so a second waiter isn't failed
// by the first going away, but it is canceled once no request is waiting
// on it; a canceled result has errors and so is never cached.
//
// ponytail: whole-map reset at groupCacheMax; an LRU if studio ever serves
// many users.
func (s *Server) shared(ctx context.Context, key, other string, compute func(context.Context) typeGroup) typeGroup {
	s.groupsMu.Lock()
	c, ok := s.groups[key]
	if ok {
		select {
		case <-c.done:
			if !c.at.IsZero() && time.Since(c.at) < groupTTL {
				s.groupsMu.Unlock()
				return c.g
			}
			ok = false // stale or failed: compute again
		default: // in flight: join it
		}
	}
	if !ok {
		if s.groups == nil || len(s.groups) >= groupCacheMax {
			s.groups = map[string]*groupCall{}
		}
		cctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c = &groupCall{done: make(chan struct{}), cancel: cancel}
		s.groups[key] = c
		go func() {
			defer cancel()
			c.g = compute(cctx)
			if len(c.g.Errs) == 0 {
				c.at = time.Now()
			}
			close(c.done)
		}()
	}
	c.waiters++
	s.groupsMu.Unlock()

	select {
	case <-c.done:
		return c.g
	case <-ctx.Done():
		s.groupsMu.Lock()
		defer s.groupsMu.Unlock()
		c.waiters--
		select {
		case <-c.done: // finished meanwhile: keep and return the result
			return c.g
		default:
		}
		if c.waiters == 0 {
			c.cancel()
			if s.groups[key] == c {
				delete(s.groups, key) // a new request starts afresh
			}
		}
		return typeGroup{Type: other, Errs: []string{ctx.Err().Error()}}
	}
}

func (s *Server) computeGroup(ctx context.Context, self entityRef, tab, other string) typeGroup {
	empty := typeGroup{Type: other}
	model, reason := s.loadModel(ctx)
	if model == nil {
		empty.Errs = []string{reason}
		return empty
	}
	obj := melange.Object{Type: melange.ObjectType(self.Type), ID: self.ID}
	page := melange.PageOptions{Limit: groupLimit}
	var rows []accessRow
	if tab == tabCan {
		rows = listRelations(ctx, relationsOf(model.Types, other), other, func(ctx context.Context, rel string) ([]string, error) {
			ids, _, err := s.checker.ListObjects(ctx, obj, melange.Relation(rel), melange.ObjectType(other), page)
			return ids, err
		})
	} else {
		rows = listRelations(ctx, relationsOf(model.Types, self.Type), other, func(ctx context.Context, rel string) ([]string, error) {
			ids, _, err := s.checker.ListSubjects(ctx, obj, melange.Relation(rel), melange.ObjectType(other), page)
			return ids, err
		})
	}
	if groups := groupByType(rows); len(groups) > 0 {
		return groups[0]
	}
	return empty
}

// listRelations runs list once per relation, sharing listSlots, each
// bounded by listTimeout, and keeps the rows worth showing: some ids, or an
// error. Rows stay in rels order.
func listRelations(ctx context.Context, rels []string, typ string, list func(context.Context, string) ([]string, error)) []accessRow {
	rows := make([]accessRow, len(rels))
	errs := eachRelation(ctx, rels, func(ctx context.Context, i int, rel string) (err error) {
		rows[i].IDs, err = list(ctx, rel)
		return err
	})
	for i, rel := range rels {
		rows[i].Relation, rows[i].Type, rows[i].Err = rel, typ, errString(errs[i])
	}
	return slices.DeleteFunc(rows, func(r accessRow) bool { return r.Err == "" && len(r.IDs) == 0 })
}

// eachRelation calls fn once per relation, sharing listSlots with every
// other list call in the server, each bounded by listTimeout, and returns
// each call's error by index (a timeout reads "timed out after 10s" rather
// than "context deadline exceeded").
func eachRelation(ctx context.Context, rels []string, fn func(ctx context.Context, i int, rel string) error) []error {
	errs := make([]error, len(rels))
	var wg sync.WaitGroup
	for i, rel := range rels {
		wg.Go(func() {
			select {
			case listSlots <- struct{}{}:
				defer func() { <-listSlots }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			cctx, cancel := context.WithTimeout(ctx, listTimeout)
			defer cancel()
			err := fn(cctx, i, rel)
			if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
				err = fmt.Errorf("timed out after %s", listTimeout)
			}
			errs[i] = err
		})
	}
	wg.Wait()
	return errs
}

// typeGroup is one type in an entity's access tree: the entities of that type
// and, for each, the relations linking it to the page's entity.
type typeGroup struct {
	Type     string
	Entities []entityGroup
	Errs     []string
	Capped   bool
}

type entityGroup struct {
	ID        string
	Relations []string
}

// groupByType pivots per-relation rows into type → entity → relations,
// keeping first-seen order (model order for types and relations).
func groupByType(rows []accessRow) []typeGroup {
	var out []typeGroup
	typeIdx := map[string]int{}
	entIdx := map[string]int{} // "type:id" → index within its group
	for _, row := range rows {
		ti, ok := typeIdx[row.Type]
		if !ok {
			ti = len(out)
			typeIdx[row.Type] = ti
			out = append(out, typeGroup{Type: row.Type})
		}
		g := &out[ti]
		if row.Err != "" {
			g.Errs = append(g.Errs, row.Relation+": "+row.Err)
		}
		g.Capped = g.Capped || len(row.IDs) == groupLimit
		for _, id := range row.IDs {
			key := row.Type + ":" + id
			ei, ok := entIdx[key]
			if !ok {
				ei = len(g.Entities)
				entIdx[key] = ei
				g.Entities = append(g.Entities, entityGroup{ID: id})
			}
			g.Entities[ei].Relations = append(g.Entities[ei].Relations, row.Relation)
		}
	}
	for i := range out {
		slices.SortStableFunc(out[i].Entities, func(a, b entityGroup) int { return naturalCompare(a.ID, b.ID) })
	}
	return out
}

func relationsOf(types []schema.TypeDefinition, name string) []string {
	for _, t := range types {
		if t.Name == name {
			out := make([]string, len(t.Relations))
			for i, r := range t.Relations {
				out[i] = r.Name
			}
			return out
		}
	}
	return nil
}

// subjectTypes are the types that actually appear as tuple subjects; asking
// ListSubjects for any other type can only return nothing.
func (s *Server) subjectTypes(ctx context.Context, o modelOrder) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT subject_type FROM `+s.tuples())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, o.compareTypes)
	return out, nil
}

// Direct tuple sides: the page's entity as the subject or as the object.
const (
	sideSubject = "subject"
	sideObject  = "object"
)

var sideWhere = map[string]string{
	sideSubject: "subject_type = $1 AND split_part(subject_id, '#', 1) = $2",
	sideObject:  "object_type = $1 AND object_id = $2",
}

// directPage is one page of one side's tuples.
type directPage struct {
	Side   string
	Tuples []melange.TupleRef
	More   bool
	Next   string // the next page's request, when More
}

// directTuples returns a page of the tuples on one side of e, in model order
// (types, then relations, as ranked by array_position over the model's
// lists; unknown ones last), filtered by q across both ends and the relation.
func (s *Server) directTuples(ctx context.Context, o modelOrder, side string, e entityRef, q string, offset int) (directPage, error) {
	p := directPage{Side: side}
	rows, err := s.db.QueryContext(ctx, `
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM `+s.tuples()+` WHERE `+sideWhere[side]+` AND `+directMatch+`
		ORDER BY array_position($4::text[], subject_type), subject_type,
		         array_position($4::text[], object_type), object_type,
		         array_position($5::text[], object_type || '#' || relation), relation,
		         subject_id, object_id
		LIMIT $6 OFFSET $7`, e.Type, e.ID, q, pq.Array(o.typeList()), pq.Array(o.relList()), pageSize+1, offset)
	if err != nil {
		return p, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t melange.TupleRef
		if err := rows.Scan(&t.SubjectType, &t.SubjectID, &t.Relation, &t.ObjectType, &t.ObjectID); err != nil {
			return p, err
		}
		p.Tuples = append(p.Tuples, t)
	}
	if p.More = len(p.Tuples) > pageSize; p.More {
		p.Tuples = p.Tuples[:pageSize]
		p.Next = "/entity/direct?" + url.Values{
			"type": {e.Type}, "id": {e.ID}, "side": {side}, "q": {q}, "offset": {strconv.Itoa(offset + pageSize)},
		}.Encode()
	}
	return p, rows.Err()
}

// directMatch is the Direct tab's filter on $3: a substring of either end
// or the relation, case-insensitive; empty matches everything.
const directMatch = `($3 = '' OR concat_ws(' ', subject_type || ':' || subject_id, relation, object_type || ':' || object_id) ILIKE '%' || $3 || '%')`

// directCounts counts the tuples on each side of e that match q.
func (s *Server) directCounts(ctx context.Context, e entityRef, q string) (asSubject, asObject int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE `+sideWhere[sideSubject]+`),
		       count(*) FILTER (WHERE `+sideWhere[sideObject]+`)
		FROM `+s.tuples()+`
		WHERE ((`+sideWhere[sideSubject]+`) OR (`+sideWhere[sideObject]+`)) AND `+directMatch, e.Type, e.ID, q).Scan(&asSubject, &asObject)
	return asSubject, asObject, err
}
