package studio

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

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
	// Direct tuples are always loaded: they are cheap and the tab shows a count.
	AsSubject    []melange.TupleRef
	AsObject     []melange.TupleRef
	DirectCapped bool
	ModelErr     string
	Labels       labels
	Types        []typeCount
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

	var err error
	if d.Types, err = s.typeCounts(ctx, order); err == nil {
		d.AsSubject, err = s.directTuples(ctx, order, "subject_type = $1 AND split_part(subject_id, '#', 1) = $2", ent)
	}
	if err == nil {
		d.AsObject, err = s.directTuples(ctx, order, "object_type = $1 AND object_id = $2", ent)
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
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	d.DirectCapped = len(d.AsSubject) == listLimit || len(d.AsObject) == listLimit
	if d.Group == "" && len(d.Groups) > 0 {
		d.Group = d.Groups[0]
	}

	refs := make([]entityRef, 0, 1+2*(len(d.AsSubject)+len(d.AsObject)))
	refs = append(refs, d.Entity)
	for _, t := range append(d.AsSubject, d.AsObject...) {
		refs = append(refs,
			entityRef{Type: t.SubjectType, ID: strings.SplitN(t.SubjectID, "#", 2)[0]},
			entityRef{Type: t.ObjectType, ID: t.ObjectID})
	}
	if d.Labels, err = s.labels.resolve(ctx, refs); err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	render(w, r, entityPage(d))
}

// groupParams reads the (entity, tab, type) a group request is about.
func groupParams(r *http.Request) (self entityRef, tab, group string, ok bool) {
	self = entityRef{Type: r.FormValue("type"), ID: r.FormValue("id")}
	tab, group = r.FormValue("tab"), r.FormValue("group")
	return self, tab, group, self.Type != "" && self.ID != "" && group != "" && (tab == tabCan || tab == tabBy)
}

// entityGroup serves the entity list of one type on an access tab.
func (s *Server) entityGroup(w http.ResponseWriter, r *http.Request) {
	self, tab, group, ok := groupParams(r)
	if !ok {
		render(w, r, errorBox("type, id, tab and group are required"))
		return
	}
	g := s.group(r.Context(), self, tab, group)
	refs := make([]entityRef, len(g.Entities))
	for i, e := range g.Entities {
		refs[i] = entityRef{Type: g.Type, ID: e.ID}
	}
	ls, err := s.labels.resolve(r.Context(), refs)
	if err != nil {
		render(w, r, errorBox(err.Error()))
		return
	}
	render(w, r, accessList(g, ls, self, tab == tabCan))
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
	groupCacheMax = 512 // entries; the cache is dropped wholesale past this
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
	page := melange.PageOptions{Limit: listLimit}
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
		g.Capped = g.Capped || len(row.IDs) == listLimit
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

// directTuples returns raw tuples matching where ($1 = type, $2 = id), in
// model order: subject type, object type, then the relation's place in the
// object type, with ids breaking ties.
func (s *Server) directTuples(ctx context.Context, o modelOrder, where string, e entityRef) ([]melange.TupleRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT subject_type, subject_id, relation, object_type, object_id
		FROM `+s.tuples()+` WHERE `+where+`
		ORDER BY 1, 2, 3, 4, 5 LIMIT $3`, e.Type, e.ID, listLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []melange.TupleRef
	for rows.Next() {
		var t melange.TupleRef
		if err := rows.Scan(&t.SubjectType, &t.SubjectID, &t.Relation, &t.ObjectType, &t.ObjectID); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b melange.TupleRef) int {
		return cmp.Or(
			o.compareTypes(a.SubjectType, b.SubjectType),
			o.compareTypes(a.ObjectType, b.ObjectType),
			o.compareRels(a.ObjectType, a.Relation, b.Relation),
			strings.Compare(a.SubjectID, b.SubjectID),
			strings.Compare(a.ObjectID, b.ObjectID),
		)
	})
	return out, nil
}
