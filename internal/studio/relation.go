package studio

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// relationData is the relation detail page: one relation's definition, its
// dependency edges, the code generator's view of it, and its deployed SQL.
type relationData struct {
	Env            string
	Model          *migrator.DeployedModel
	Type, Relation string
	Err            string // why the model parts are missing (no model, unknown relation)
	Known          bool   // the relation exists, or there is no model to say otherwise
	Define         string
	Analysis       *compiler.RelationAnalysis
	DependsOn      []relEdge
	UsedBy         []relEdge
	Funcs          []sqlFunc
	FuncErr        string
}

// relEdge is a link between two relations; Kinds says how (computed,
// userset, tupleset, from, but not, and), since one pair can be linked twice.
type relEdge struct {
	Type, Relation string
	Kinds          []string
}

type sqlFunc struct{ Name, Label, Def string }

// edgeShow is how many edges a list shows before folding the rest away.
const edgeShow = 20

func (s *Server) relation(w http.ResponseWriter, r *http.Request) {
	d := relationData{Env: s.env, Type: r.URL.Query().Get("type"), Relation: r.URL.Query().Get("relation")}
	d.Model, d.Err = s.loadModel(r.Context())
	if d.Type == "" || d.Relation == "" {
		w.WriteHeader(http.StatusBadRequest)
		d.Err = "type and relation are required"
		render(w, r, relationPage(d))
		return
	}
	if d.Model != nil {
		ix := newRelIndex(d.Model.Types)
		if ix[d.Type][d.Relation] == nil {
			w.WriteHeader(http.StatusNotFound)
			d.Err = d.Type + "#" + d.Relation + " is not a relation in the deployed model."
			render(w, r, relationPage(d))
			return
		}
		d.Define = defineText(d.Model.DSL, d.Type, d.Relation)
		d.DependsOn, d.UsedBy = ix.dependsOn(d.Type, d.Relation), ix.usedBy(d.Type, d.Relation)
		o := newModelOrder(d.Model)
		byModel := func(a, b relEdge) int {
			return cmp.Or(o.compareTypes(a.Type, b.Type), o.compareRels(a.Type, a.Relation, b.Relation))
		}
		slices.SortFunc(d.DependsOn, byModel)
		slices.SortFunc(d.UsedBy, byModel)
		// Same pipeline as dump-sql and doctor; pure Go, ~6ms on 1500 relations.
		an := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(d.Model.Types, schema.ComputeRelationClosure(d.Model.Types)))
		if i := slices.IndexFunc(an, func(a compiler.RelationAnalysis) bool { return a.ObjectType == d.Type && a.Relation == d.Relation }); i >= 0 {
			d.Analysis = &an[i]
			// an is built per request, so sorting in place is safe.
			slices.SortFunc(d.Analysis.SatisfyingRelations, func(a, b string) int { return o.compareRels(d.Type, a, b) })
		}
	}
	d.Known = true
	// The deployed source, not a regeneration: it is what actually runs, and
	// regenerating a large model takes most of a second.
	d.Funcs, d.FuncErr = s.relationFuncs(r, d.Type, d.Relation)
	render(w, r, relationPage(d))
}

// relationFuncs reads the relation's specialized functions from pg_proc,
// in the order check, check_nw, list objects, list subjects, explain, expand.
// Names go through SafeIdentifier so long names hash exactly as codegen did.
func (s *Server) relationFuncs(r *http.Request, typ, rel string) (funcs []sqlFunc, errMsg string) {
	names := []string{
		sqldsl.SafeIdentifier("check_", typ, rel, ""),
		sqldsl.SafeIdentifier("check_", typ, rel, "_nw"),
		sqldsl.ListObjectsFunctionName(typ, rel),
		sqldsl.ListSubjectsFunctionName(typ, rel),
		sqldsl.SafeIdentifier("explain_", typ, rel, ""),
		sqldsl.SafeIdentifier("expand_", typ, rel, ""),
	}
	// Parallel to names: the role is known from position, not guessed from
	// the name (a relation called foo_nw would look like a check_nw).
	labels := []string{"check", "check, no wildcard", "list objects", "list subjects", "explain", "expand"}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT p.proname, pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND p.proname = ANY($2)
		ORDER BY array_position($2, p.proname::text), p.oid`, s.dbSchema, pq.Array(names))
	if err != nil {
		return nil, err.Error()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f sqlFunc
		if err := rows.Scan(&f.Name, &f.Def); err != nil {
			return nil, err.Error()
		}
		f.Label = labels[slices.Index(names, f.Name)]
		funcs = append(funcs, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err.Error()
	}
	return funcs, ""
}

// defineText extracts a relation's define line from the DSL, wrapped in its
// type and relations header so it highlights like the full schema. It scans
// the same way newModelOrder does, so modular bundles and extend type work.
func defineText(dsl, typ, rel string) string {
	cur := ""
	for line := range strings.SplitSeq(dsl, "\n") {
		if t := typeLine.FindStringSubmatch(line); t != nil {
			cur = t[1]
			continue
		}
		if d := defineLine.FindStringSubmatch(line); d != nil && cur == typ && d[1] == rel {
			return "type " + typ + "\n  relations\n    " + strings.TrimSpace(line)
		}
	}
	return ""
}

// relIndex looks relations up by type and name.
type relIndex map[string]map[string]*schema.RelationDefinition

func newRelIndex(types []schema.TypeDefinition) relIndex {
	ix := relIndex{}
	for i := range types {
		t := &types[i]
		ix[t.Name] = map[string]*schema.RelationDefinition{}
		for j := range t.Relations {
			ix[t.Name][t.Relations[j].Name] = &t.Relations[j]
		}
	}
	return ix
}

// dependsOn lists the relations typ#rel reads directly. A TTU's target types
// come from the linking relation's direct (non-userset, non-wildcard) refs,
// as schema validation resolves them. An intersection's own name is its
// direct "[this]" part, not an edge.
func (ix relIndex) dependsOn(typ, rel string) []relEdge {
	r := ix[typ][rel]
	if r == nil {
		return nil
	}
	var out []relEdge
	add := func(kind, t, x string) {
		i := slices.IndexFunc(out, func(e relEdge) bool { return e.Type == t && e.Relation == x })
		if i < 0 {
			out = append(out, relEdge{Type: t, Relation: x})
			i = len(out) - 1
		}
		if !slices.Contains(out[i].Kinds, kind) {
			out[i].Kinds = append(out[i].Kinds, kind)
		}
	}
	ttu := func(kind string, p schema.ParentRelationCheck) {
		add("tupleset", typ, p.LinkingRelation)
		if link := ix[typ][p.LinkingRelation]; link != nil {
			for _, ref := range link.SubjectTypeRefs {
				if ref.Relation == "" && !ref.Wildcard && ix[ref.Type][p.Relation] != nil {
					add(kind, ref.Type, p.Relation)
				}
			}
		}
	}
	groups := func(kind string, gs []schema.IntersectionGroup) {
		for _, g := range gs {
			for _, x := range g.Relations {
				if x != rel {
					add(kind, typ, x)
				}
			}
			for _, p := range g.ParentRelations {
				ttu(kind, p)
			}
			for _, xs := range g.Exclusions {
				for _, x := range xs {
					add("but not", typ, x)
				}
			}
		}
	}
	for _, ref := range r.SubjectTypeRefs {
		if ref.Relation != "" {
			add("userset", ref.Type, ref.Relation)
		}
	}
	for _, x := range r.ImpliedBy {
		add("computed", typ, x)
	}
	for _, p := range r.ParentRelations {
		ttu("from", p)
	}
	for _, x := range r.ExcludedRelations {
		add("but not", typ, x)
	}
	for _, p := range r.ExcludedParentRelations {
		ttu("but not", p)
	}
	groups("and", r.IntersectionGroups)
	groups("but not", r.ExcludedIntersectionGroups)
	return out
}

// usedBy is the reverse of dependsOn: every relation with an edge to typ#rel.
// ponytail: a full scan per request, O(relations); fine at 1500 relations.
func (ix relIndex) usedBy(typ, rel string) []relEdge {
	var out []relEdge
	for t, rels := range ix {
		for name := range rels {
			for _, e := range ix.dependsOn(t, name) {
				if e.Type == typ && e.Relation == rel {
					out = append(out, relEdge{Type: t, Relation: name, Kinds: e.Kinds})
				}
			}
		}
	}
	return out
}

// features lists the codegen's feature flags that are set, in its own order,
// lowercased to match the page's other labels.
func features(a *compiler.RelationAnalysis) []string {
	if s := a.Features.String(); s != "None" {
		return strings.Split(strings.ToLower(s), "+")
	}
	return nil
}

// dependsOnHelp explains tupleset only when one of the edges is one.
func dependsOnHelp(edges []relEdge) string {
	if slices.ContainsFunc(edges, func(e relEdge) bool { return slices.Contains(e.Kinds, "tupleset") }) {
		return "Relations this one reads. A tupleset is the relation that links to the parent object."
	}
	return "Relations this one reads."
}
