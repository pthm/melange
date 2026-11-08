package studio

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/a-h/templ"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// sqlAPIData is the SQL API page: the functions the deployed model installs
// under the recorded exposure, checked against pg_proc, for one type or one
// kind, or an overview of the dispatchers and what drifted.
//
// It describes the deployed model, as melange functions --db does. The
// Health page runs melange doctor, which compares the local schema file, so
// the two can disagree while a migration is pending.
type sqlAPIData struct {
	Env, Schema string
	Type, Kind  string // the selection; both empty is the overview
	Err         string
	Report      *sqlapi.Report
	Types       []sqlAPIType
	// Exposed and Internal are the selected relations.
	Exposed, Internal []sqlapi.Relation
	// Drifted are the expected functions whose status is not installed or
	// that have stale overloads; with the report's orphans they are the
	// drift the page counts.
	Drifted []sqlapi.Function
}

// driftCount is the drift the page counts: the drifted functions and the
// orphans.
func (d sqlAPIData) driftCount() int { return len(d.Drifted) + len(d.Report.Orphans) }

// sqlAPIType is a type in the sidebar.
type sqlAPIType struct {
	Name               string
	Relations, Exposed int
}

func (s *Server) sqlAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := sqlAPIData{Env: s.env, Schema: s.dbSchema, Type: q.Get("type"), Kind: q.Get("kind")}
	if err := validSQLAPIQuery(d.Type, d.Kind); err != "" {
		w.WriteHeader(http.StatusBadRequest)
		d.Err = err
		render(w, r, sqlAPIPage(d))
		return
	}
	model, reason := s.loadModel(r.Context())
	if model == nil {
		d.Err = reason
		render(w, r, sqlAPIPage(d))
		return
	}
	// Without types the catalog would hold only dispatchers, and every
	// recorded function would read as an orphan; melange functions --db
	// refuses this record too.
	if model.Types == nil {
		d.Err = unreadableModel
		render(w, r, sqlAPIPage(d))
		return
	}
	if d.Type != "" && !slices.ContainsFunc(model.Types, func(t schema.TypeDefinition) bool { return t.Name == d.Type }) {
		w.WriteHeader(http.StatusNotFound)
		d.Err = d.Type + " is not a type in the deployed model."
		render(w, r, sqlAPIPage(d))
		return
	}
	report, err := s.sqlAPIReport(r, model)
	if err != nil {
		d.Err = err.Error()
		render(w, r, sqlAPIPage(d))
		return
	}
	d.Report = report
	for _, rel := range report.Relations {
		if n := len(d.Types); n == 0 || d.Types[n-1].Name != rel.Type {
			d.Types = append(d.Types, sqlAPIType{Name: rel.Type})
		}
		t := &d.Types[len(d.Types)-1]
		t.Relations++
		if rel.Exposed {
			t.Exposed++
		}
		if (d.Type == "" && d.Kind == "") || (d.Type != "" && rel.Type != d.Type) {
			continue
		}
		if d.Kind != "" {
			rel.Functions = slices.DeleteFunc(slices.Clone(rel.Functions), func(f sqlapi.Function) bool { return string(f.Kind) != d.Kind })
			rel.NotInstalled = slices.DeleteFunc(slices.Clone(rel.NotInstalled), func(n sqlgen.NotInstalled) bool { return string(n.Kind) != d.Kind })
			if len(rel.Functions) == 0 && len(rel.NotInstalled) == 0 {
				continue
			}
		}
		if rel.Exposed {
			d.Exposed = append(d.Exposed, rel)
		} else {
			d.Internal = append(d.Internal, rel)
		}
	}
	for _, f := range allFunctions(report) {
		if f.Status != sqlapi.StatusInstalled || len(f.StaleOverloads) > 0 {
			d.Drifted = append(d.Drifted, f)
		}
	}
	render(w, r, sqlAPIPage(d))
}

// sqlAPIReport builds the deployed model's report and checks it against
// the database, bodies included (compiled once per model and cached). The
// bodies are compiled from the types as recorded, in the order migrate saw
// them, since the dispatchers route in that order; the page then lists
// them in model order.
func (s *Server) sqlAPIReport(r *http.Request, model *migrator.DeployedModel) (*sqlapi.Report, error) {
	m := migrator.NewMigrator(s.db, "")
	m.SetDatabaseSchema(s.dbSchema)
	rec, err := m.GetLastMigration(r.Context())
	if err != nil {
		return nil, err
	}
	types, err := migrator.DeployedTypes(rec)
	if err != nil || types == nil {
		return nil, errors.New(unreadableModel)
	}
	report, err := sqlapi.Describe(types, model.Exposure, s.dbSchema)
	if err != nil {
		return nil, err
	}
	report.Source = "database"
	if err := report.Check(r.Context(), s.db, rec, sqlapi.CheckOptions{Bodies: true}); err != nil {
		return nil, err
	}
	o := newModelOrder(model)
	slices.SortStableFunc(report.Relations, func(a, b sqlapi.Relation) int {
		return cmp.Or(o.compareTypes(a.Type, b.Type), o.compareRels(a.Type, a.Relation, b.Relation))
	})
	return report, nil
}

// unreadableModel is the error for a record whose model this melange cannot
// read: model_json and the DSL both fail, as for a newer melange's record.
const unreadableModel = "The last migration recorded a model this melange cannot read, so its SQL API cannot be described. Use the melange version that migrated the database, or re-run `melange migrate` with this one."

// validSQLAPIQuery checks the page's parameters before any query: a kind
// must be a relation function's kind, and a type a name the schema language
// can hold. The kind selects relations, which have no dispatchers.
func validSQLAPIQuery(typ, kind string) string {
	if kind == string(sqlgen.KindDispatcher) {
		return "the dispatchers belong to no relation; the overview lists them"
	}
	if kind != "" && !slices.Contains(sqlgen.FunctionKinds, sqlgen.FunctionKind(kind)) {
		return fmt.Sprintf("kind %q is not a function kind", kind)
	}
	if strings.ContainsAny(typ, "# \t\r\n:@") || len(typ) > 512 {
		return fmt.Sprintf("type %q is not a type name", typ)
	}
	return ""
}

// allFunctions returns every function of the report, dispatchers last.
func allFunctions(r *sqlapi.Report) []sqlapi.Function {
	var out []sqlapi.Function
	for _, rel := range r.Relations {
		out = append(out, rel.Functions...)
	}
	return append(out, r.Dispatchers...)
}

func sqlAPIURL(typ, kind string) templ.SafeURL {
	v := url.Values{}
	if typ != "" {
		v.Set("type", typ)
	}
	if kind != "" {
		v.Set("kind", kind)
	}
	if len(v) == 0 {
		return "/sql"
	}
	return templ.URL("/sql?" + v.Encode())
}

// sqlRelationURL is the SQL API page of a relation's type, at the relation.
func sqlRelationURL(typ, rel string) templ.SafeURL {
	return templ.URL("/sql?" + url.Values{"type": {typ}}.Encode() + "#" + relAnchor(typ, rel))
}

// relAnchor is the fragment id of a relation on the SQL API page.
func relAnchor(typ, rel string) string { return "rel-" + typ + "-" + rel }

// statusBadge is a function status's badge tone and label.
func statusBadge(st sqlapi.Status) (class, label string) {
	label = strings.ReplaceAll(string(st), "_", " ")
	switch st {
	case sqlapi.StatusInstalled:
		return "ok", label
	case sqlapi.StatusMissing:
		return "fail", label
	case sqlapi.StatusNotChecked:
		return "", label
	default:
		return "warn", label
	}
}

// kindLabel names a function kind for display.
func kindLabel(f sqlgen.CatalogFunction) string {
	switch f.Kind {
	case sqlgen.KindCheckNoWildcard:
		return "check, no wildcard"
	case sqlgen.KindListObjects:
		return "list objects"
	case sqlgen.KindListSubjects:
		return "list subjects"
	case sqlgen.KindAccessible:
		return "accessible (inlinable)"
	case sqlgen.KindParentKey:
		return "accessible by " + f.Link + " (" + f.ParentType + " ids)"
	}
	return string(f.Kind)
}

func visibility(public bool) string {
	if public {
		return "public"
	}
	return "internal"
}

func reasonLabel(r sqlgen.NotInstalledReason) string { return strings.ReplaceAll(string(r), "_", " ") }

// exposureText is the recorded exposure for display.
func exposureText(e sqlgen.Exposure) string {
	if e.IsDefault() {
		return "default: every relation, every function family"
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err.Error()
	}
	return string(data)
}
