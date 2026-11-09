package studio

import (
	"cmp"
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

// hasDrift reports whether the overview has a Drift section: drift, an
// unmanaged function, or a note to show.
func (d sqlAPIData) hasDrift() bool {
	return d.driftCount() > 0 || len(d.Report.Unmanaged) > 0 || len(d.Report.Notes) > 0
}

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
		d.Err = unreadableModel(model.CodegenVersion)
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
		if drifted(f) {
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
		return nil, errors.New(unreadableModel(model.CodegenVersion))
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
// codegen is the version that recorded it. A plain migrate does not read the
// recorded model, so under that same version it skips, and only --force
// records the model again; a newer version's record is refused.
func unreadableModel(codegen string) string {
	const prefix = "The last migration recorded a model this melange cannot read, so its SQL API cannot be described. "
	switch {
	case migrator.CodegenVersionAhead(codegen):
		return prefix + fmt.Sprintf("Use melange %s or later, which migrated the database.", codegen)
	case migrator.SameCodegenVersion(codegen, migrator.CodegenVersion()):
		return prefix + "Re-run `melange migrate --force` to record it again: a plain migrate skips while the schema, exposure and codegen version are unchanged."
	}
	return prefix + "Re-run `melange migrate` with this melange to record it again."
}

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

func reasonLabel(r sqlgen.NotInstalledReason) string { return strings.ReplaceAll(string(r), "_", " ") }

// kindTitle names a function kind in the sidebar and in headings.
func kindTitle(k sqlgen.FunctionKind) string {
	switch k {
	case sqlgen.KindCheck:
		return "Check"
	case sqlgen.KindCheckNoWildcard:
		return "Check, no wildcard"
	case sqlgen.KindListObjects:
		return "List objects"
	case sqlgen.KindListSubjects:
		return "List subjects"
	case sqlgen.KindAccessible:
		return "Accessible"
	case sqlgen.KindParentKey:
		return "Accessible by parent"
	case sqlgen.KindExpand:
		return "Expand"
	case sqlgen.KindExplain:
		return "Explain"
	}
	return string(k)
}

// kvRow is one row of a definition list.
type kvRow struct{ Label, Value string }

// exposureRows describes the recorded exposure: which relations, and which
// function families are off.
func exposureRows(e sqlgen.Exposure) []kvRow {
	rels := "every relation"
	if len(e.Relations.Include) > 0 {
		rels = strings.Join(e.Relations.Include, ", ")
	}
	if len(e.Relations.Exclude) > 0 {
		rels += ", except " + strings.Join(e.Relations.Exclude, ", ")
	}
	var off []string
	f := e.Functions
	for _, fam := range []struct {
		name string
		flag *bool
	}{
		{"list_objects", f.ListObjects},
		{"list_subjects", f.ListSubjects},
		{"accessible", f.Accessible},
		{"parent_key", f.ParentKey},
		{"expand", f.Expand},
		{"explain", f.Explain},
	} {
		if fam.flag != nil && !*fam.flag {
			off = append(off, fam.name)
		}
	}
	families := "every family"
	if len(off) > 0 {
		families = "every family except " + strings.Join(off, ", ")
	}
	return []kvRow{{"Exposes", rels}, {"Installs", families}}
}

// splitDispatchers splits the dispatchers into the public entry points and
// the internal ones.
func splitDispatchers(fs []sqlapi.Function) (public, internal []sqlapi.Function) {
	for _, f := range fs {
		if f.Public {
			public = append(public, f)
		} else {
			internal = append(internal, f)
		}
	}
	return public, internal
}

// dispatcherPurpose says in a line what a dispatcher answers.
func dispatcherPurpose(name string) string {
	switch name {
	case "check_permission":
		return "Does the subject have the relation on the object? 1 or 0."
	case "check_permission_nw":
		return "check_permission, ignoring wildcard (type:*) grants."
	case "check_permission_bulk":
		return "Many checks in one call."
	case "list_accessible_objects":
		return "The objects a subject has the relation on, a page at a time."
	case "accessible_objects":
		return "The same objects as one set, to filter your own queries by."
	case "list_accessible_subjects":
		return "The subjects that have the relation on an object, a page at a time."
	case "expand_permission":
		return "Who has the relation on an object, as a tree (OpenFGA Expand)."
	case "explain_permission":
		return "Why a check is allowed or denied, as a trace."
	case sqlgen.UnexposedFunctionName:
		return "Raises " + sqlgen.NotExposedErrCode + " for a relation the model defines but does not expose."
	}
	if strings.HasSuffix(name, "_internal") {
		return "The routing behind " + strings.TrimSuffix(name, "_internal") + ", for generated functions."
	}
	return ""
}

// internalNote says why an unexposed relation is listed, and which entry
// point still answers for it.
func internalNote(rel sqlapi.Relation) string {
	if len(rel.Functions) == 0 {
		if slices.ContainsFunc(rel.NotInstalled, func(n sqlgen.NotInstalled) bool { return n.Reason == sqlgen.ReasonNoAccessPath }) {
			return "Not exposed, and it has no access path, so none of its functions are generated."
		}
		return "Not exposed, and no exposed relation reads it, so none of its functions are installed."
	}
	note := "Not exposed: installed because an exposed relation reads it. The entry points raise " + sqlgen.NotExposedErrCode + " for it"
	if len(rel.Dispatchers) > 0 {
		note += ", except " + strings.Join(rel.Dispatchers, ", ")
	}
	return note + "."
}

// apiCall is one row of a relation's calls: a ready-to-run call, or why the
// application cannot make it.
type apiCall struct {
	Label, SQL, Note string
	Off              string
}

// callKinds are the rows of a relation's calls, in order: the function kind
// a row stands for, and the dispatcher that answers it.
var callKinds = []struct {
	kind       sqlgen.FunctionKind
	label      string
	dispatcher string
}{
	{sqlgen.KindCheck, "Check", "check_permission"},
	{sqlgen.KindListObjects, "List objects", "list_accessible_objects"},
	{sqlgen.KindListSubjects, "List subjects", "list_accessible_subjects"},
	{sqlgen.KindAccessible, "Filter a query", "accessible_objects"},
	{sqlgen.KindParentKey, "", ""},
	{sqlgen.KindExpand, "Expand", "expand_permission"},
	{sqlgen.KindExplain, "Explain", "explain_permission"},
}

// relationCalls is how an application calls the relation: the dispatcher
// call for each kind, or the public function of the kind (the inlinable
// accessible function, the parent-key forms), or why there is neither.
// A kind the relation cannot have (not eligible, no access path) has no row.
func relationCalls(rel sqlapi.Relation) []apiCall {
	var out []apiCall
	for _, ck := range callKinds {
		n := len(out)
		for _, f := range rel.Functions {
			if f.Kind != ck.kind || !f.Public {
				continue
			}
			label := ck.label
			if f.Kind == sqlgen.KindParentKey {
				label = "Filter by " + f.Link
			}
			note := f.Note
			if f.Kind == sqlgen.KindAccessible {
				note = accessibleCallNote
			}
			out = append(out, apiCall{Label: label, SQL: cmp.Or(f.Usage, f.Example), Note: note})
		}
		if len(out) > n {
			continue
		}
		if i := slices.IndexFunc(rel.Examples, func(e sqlgen.DispatcherCall) bool { return e.Dispatcher == ck.dispatcher }); i >= 0 {
			e := rel.Examples[i]
			out = append(out, apiCall{Label: ck.label, SQL: cmp.Or(e.Usage, e.Example)})
			continue
		}
		if !rel.Exposed {
			continue
		}
		if i := slices.IndexFunc(rel.NotInstalled, func(ni sqlgen.NotInstalled) bool { return ni.Kind == ck.kind }); i >= 0 && callable(rel.NotInstalled[i]) {
			ni := rel.NotInstalled[i]
			out = append(out, apiCall{Label: cmp.Or(ck.label, kindTitle(ck.kind)), Off: "Not installed: " + ni.Detail + "."})
			continue
		}
		// Installed but not public: another family needs the function (the
		// list function backs the accessible and parent-key ones), while this
		// family is off, so its entry point does not answer.
		if ck.dispatcher != "" && slices.ContainsFunc(rel.Functions, func(f sqlapi.Function) bool { return f.Kind == ck.kind }) {
			out = append(out, apiCall{Label: ck.label, Off: fmt.Sprintf("Not public: sql.functions.%s is off, so %s does not answer for it. Its function is installed because another family needs it.", ck.kind, ck.dispatcher)})
		}
	}
	return out
}

// accessibleCallNote is the accessible function's note, short enough to
// repeat under every relation; the function's own details have it in full.
const accessibleCallNote = "Inlined into your query, so the schema holding melange_tuples must be on the search_path."

// callable reports whether a missing function is one the application could
// call under another exposure: the family is off or compiled on demand, not
// a function the relation can never have.
func callable(n sqlgen.NotInstalled) bool {
	return n.Reason == sqlgen.ReasonFamilyOff || n.Reason == sqlgen.ReasonOnDemand
}

// skipCalled drops the missing functions an exposed relation's calls
// already explain.
func skipCalled(ns []sqlgen.NotInstalled, exposed bool) []sqlgen.NotInstalled {
	if !exposed {
		return ns
	}
	return slices.DeleteFunc(slices.Clone(ns), callable)
}

// drifted reports whether a function is not installed as expected: its
// status is not installed, or it has stale overloads.
func drifted(f sqlapi.Function) bool {
	return f.Status != sqlapi.StatusInstalled || len(f.StaleOverloads) > 0
}

// functionsDrift summarizes the functions that are not installed as
// expected; "" when every one is.
func functionsDrift(fs []sqlapi.Function) string {
	n := 0
	for _, f := range fs {
		if drifted(f) {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return "· " + num(n) + " drifted"
}

// signatureSQL is a function's signature as SQL, one argument per line.
func signatureSQL(name string, s sqlgen.Signature) string {
	if len(s.Args) == 0 {
		return name + "() RETURNS " + s.Returns
	}
	var b strings.Builder
	b.WriteString(name + "(\n")
	for i, a := range s.Args {
		b.WriteString("    " + a.Name + " " + a.Type)
		if a.Default != "" {
			b.WriteString(" DEFAULT " + a.Default)
		}
		if i < len(s.Args)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(") RETURNS " + s.Returns)
	return b.String()
}

func pagedLabel(s sqlgen.Signature) string {
	if s.Paged {
		return " · paged"
	}
	return ""
}
