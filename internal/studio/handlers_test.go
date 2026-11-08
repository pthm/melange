package studio

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/pthm/melange/internal/doctor"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// serve sends one request through the full handler of a Server with no
// database. Only paths that reject their input before any query are safe
// to call: a handler reaching the database would panic on the nil *sql.DB,
// which is the point. Validation must come first.
func serve(t *testing.T, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:7070"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(w, r)
	return w
}

func TestRequestValidationNeedsNoDatabase(t *testing.T) {
	for _, c := range []struct {
		name, method, target string
		form                 url.Values
		want                 string
	}{
		{"explain without subject", "POST", "/query", url.Values{"mode": {"explain"}, "relation": {"viewer"}, "object": {"doc:1"}}, "subject: expected <type>:<id>"},
		{"explain without relation", "POST", "/explain", url.Values{"subject": {"user:1"}, "object": {"doc:1"}}, "relation is required"},
		{"unknown mode runs explain", "POST", "/query", url.Values{"mode": {"bogus"}, "subject": {"user"}}, "subject: expected <type>:<id>"},
		{"expand without relation", "POST", "/query", url.Values{"mode": {"expand"}, "object": {"doc:1"}}, "relation is required"},
		{"expand with a bad object", "GET", "/expand?object=doc&relation=viewer", nil, "object: expected <type>:<id>"},
		{"expand pointer without #", "GET", "/expand?userset=doc:1", nil, "expected <type>:<id>#<relation>"},
		{"matrix with a bad object", "POST", "/query", url.Values{"mode": {"matrix"}, "subject": {"user:1"}, "object": {":1"}}, "object: expected <type>:<id>"},
		{"analyze without relation", "POST", "/query", url.Values{"mode": {"analyze"}, "subject": {"user:1"}, "object": {"doc:1"}}, "relation is required"},
		{"entity without id", "GET", "/entity?type=user", nil, "type and id are required"},
		{"group without group", "GET", "/entity/group?type=user&id=1&tab=can", nil, "type, id, tab and group are required"},
		{"group on the direct tab", "GET", "/entity/group?type=user&id=1&tab=direct&group=doc", nil, "type, id, tab and group are required"},
		{"direct with a bad side", "GET", "/entity/direct?type=user&id=1&side=both", nil, "type, id and side are required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := serve(t, c.method, c.target, c.form)
			if got := html.UnescapeString(w.Body.String()); w.Code != http.StatusOK || !strings.Contains(got, c.want) {
				t.Fatalf("%d %q; want 200 containing %q", w.Code, got, c.want)
			}
			if !strings.Contains(w.Body.String(), `class="error"`) {
				t.Errorf("validation errors render in errorBox; got %q", w.Body.String())
			}
		})
	}
}

// The SQL API page checks its parameters before reading the model.
func TestSQLAPIValidationNeedsNoDatabase(t *testing.T) {
	for target, want := range map[string]string{
		"/sql?kind=lists":               `kind "lists" is not a function kind`,
		"/sql?type=doc%23viewer":        `type "doc#viewer" is not a type name`,
		"/sql?type=a+b&kind=check":      `type "a b" is not a type name`,
		"/sql?kind=%3Cscript%3E&type=x": `kind "<script>" is not a function kind`,
		"/sql?kind=dispatcher":          "the dispatchers belong to no relation; the overview lists them",
	} {
		w := serve(t, "GET", target, nil)
		if got := html.UnescapeString(w.Body.String()); w.Code != http.StatusBadRequest || !strings.Contains(got, want) {
			t.Errorf("GET %s = %d %q; want 400 containing %q", target, w.Code, got, want)
		}
		if strings.Contains(w.Body.String(), "<script>") {
			t.Errorf("GET %s echoes markup unescaped", target)
		}
	}
}

func TestEntityCountWithBadParamsIsEmpty(t *testing.T) {
	// A badge request with missing params renders nothing, leaving the "…".
	if w := serve(t, "GET", "/entity/count?type=user&id=1&tab=can", nil); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("%d %q; want an empty 200", w.Code, w.Body.String())
	}
}

func TestRoutingMethodsAndUnknownPaths(t *testing.T) {
	for _, c := range []struct {
		method, target string
		want           int
	}{
		{"GET", "/query", http.StatusMethodNotAllowed},
		{"GET", "/explain", http.StatusMethodNotAllowed},
		{"POST", "/entity", http.StatusMethodNotAllowed},
		{"POST", "/sql", http.StatusMethodNotAllowed},
		{"DELETE", "/assets/" + assetVersion + "/studio.css", http.StatusMethodNotAllowed},
		{"GET", "/nope", http.StatusNotFound},
		{"GET", "/assets/" + assetVersion + "/no-such-file.js", http.StatusNotFound},
	} {
		if w := serve(t, c.method, c.target, nil); w.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.target, w.Code, c.want)
		}
	}
	// Traversal out of the embedded assets never serves a file: the mux
	// cleans "../" with a redirect, and the file server rejects encoded dots.
	for _, target := range []string{"/assets/" + assetVersion + "/../server.go", "/assets/" + assetVersion + "/..%2fserver.go", "/assets/" + assetVersion + "/npm/../../server.go"} {
		if w := serve(t, "GET", target, nil); w.Code == http.StatusOK || strings.Contains(w.Body.String(), "package studio") {
			t.Errorf("GET %s = %d; traversal must not serve a file", target, w.Code)
		}
	}
}

func TestEveryRouteRejectsForeignHosts(t *testing.T) {
	// The Host check wraps the whole mux, so no route, valid or not, can be
	// reached through a DNS-rebinding name.
	for _, target := range []string{"/", "/tuples", "/browse/user", "/entity?type=user&id=1", "/relation?type=a&relation=b", "/sql", "/sql?type=a&kind=check", "/history", "/health", "/assets/" + assetVersion + "/studio.css", "/nope"} {
		r := httptest.NewRequest("GET", target, http.NoBody)
		r.Host = "attacker.example:7070"
		w := httptest.NewRecorder()
		(&Server{}).Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s via a foreign Host = %d, want 403", target, w.Code)
		}
	}
}

func TestErrorResponsesKeepSecurityHeaders(t *testing.T) {
	for _, w := range []*httptest.ResponseRecorder{
		serve(t, "POST", "/query", url.Values{"mode": {"explain"}}),
		serve(t, "GET", "/nope", nil),
		serve(t, "GET", "/query", nil),
	} {
		for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "Cross-Origin-Opener-Policy"} {
			if w.Header().Get(h) == "" {
				t.Errorf("%d response is missing %s", w.Code, h)
			}
		}
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%d response Cache-Control = %q, want no-store", w.Code, got)
		}
	}
}

func TestOffsetAndGroupParams(t *testing.T) {
	for in, want := range map[string]int{"": 0, "200": 200, "-5": 0, "abc": 0, "1e3": 0} {
		r := httptest.NewRequest("GET", "/x?offset="+url.QueryEscape(in), http.NoBody)
		if got := offsetParam(r); got != want {
			t.Errorf("offsetParam(%q) = %d, want %d", in, got, want)
		}
	}
	for q, ok := range map[string]bool{
		"type=user&id=1&tab=can&group=doc": true,
		"type=user&id=1&tab=by&group=doc":  true,
		"type=user&id=1&tab=x&group=doc":   false,
		"type=user&tab=can&group=doc":      false,
		"id=1&tab=can&group=doc":           false,
		"type=user&id=1&tab=can":           false,
	} {
		r := httptest.NewRequest("GET", "/x?"+q, http.NoBody)
		if _, _, _, got := groupParams(r); got != ok {
			t.Errorf("groupParams(%s) ok = %v, want %v", q, got, ok)
		}
	}
}

func TestURLHelpersEscape(t *testing.T) {
	odd := `a b&c=d/"e"`
	for name, got := range map[string]string{
		"entityURL":     string(entityURL("user", odd)),
		"relationURL":   string(relationURL("doc", odd)),
		"browseNextURL": browseNextURL("user", odd, 100),
		"groupPageURL":  groupPageURL(entityRef{Type: "user", ID: odd}, tabCan, "doc", odd, 100),
	} {
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("%s: %q does not parse: %v", name, got, err)
		}
		found := false
		for _, vs := range u.Query() {
			for _, v := range vs {
				found = found || v == odd
			}
		}
		if !found {
			t.Errorf("%s(%q) = %q; the value does not round-trip", name, odd, got)
		}
	}
	// The filter input adds its own q, so an unfiltered first page leaves q
	// and offset out instead of sending an empty q that would win.
	if got := groupPageURL(entityRef{Type: "user", ID: "1"}, tabCan, "doc", "", 0); strings.Contains(got, "q=") || strings.Contains(got, "offset=") {
		t.Errorf("groupPageURL without q or offset = %q", got)
	}
	if got := explainVals(`user:"x"`, "viewer", "doc:</script>"); got != `{"object":"doc:\u003c/script\u003e","relation":"viewer","subject":"user:\"x\""}` {
		t.Errorf("explainVals = %s", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct{ got, want string }{
		{resultClass(nil), ""},
		{resultClass(&yes), "ok"},
		{resultClass(&no), "no"},
		{short("0123456789abcdef"), "0123456789ab"},
		{short("abc"), "abc"},
		{prefill(""), ""},
		{prefill("doc"), "doc:"},
		{anchor("Schema File"), "schema-file"},
		{matchLabel(101, 101), "101"},
		{matchLabel(12, 1500), "12 of 1,500"},
		{pageTitle(page{Active: "health"}), "Health · melange studio"},
		{pageTitle(page{Active: "tuples", Title: "carol"}), "carol · melange studio"},
		{statusClass(doctor.StatusPass) + statusClass(doctor.StatusWarn) + statusClass(doctor.StatusFail), "okwarnfail"},
		{statusLabel(doctor.StatusPass) + statusLabel(doctor.StatusWarn) + statusLabel(doctor.StatusFail), "passwarnfail"},
		{dependsOnHelp([]relEdge{{Kinds: []string{"computed"}}}), "Relations this one reads."},
		{dependsOnHelp([]relEdge{{Kinds: []string{"tupleset"}}}), "Relations this one reads. A tupleset is the relation that links to the parent object."},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestShowNodeName(t *testing.T) {
	ptr := func(us string) *melange.UsersetTreeNode {
		return &melange.UsersetTreeNode{Name: "doc:1#banned", Leaf: &melange.Leaf{Computed: &melange.Computed{Userset: us}}}
	}
	for _, c := range []struct {
		n      *melange.UsersetTreeNode
		parent string
		want   bool
	}{
		{&melange.UsersetTreeNode{Name: "doc:1#viewer"}, "doc:1#viewer", false}, // repeats its parent
		{&melange.UsersetTreeNode{Name: "doc:1#editor"}, "doc:1#viewer", true},
		{ptr("doc:1#banned"), "doc:1#viewer", false}, // a pointer to itself
		{ptr("doc:1#owner"), "doc:1#viewer", true},
	} {
		if got := showNodeName(c.n, c.parent); got != c.want {
			t.Errorf("showNodeName(%s under %s) = %v, want %v", c.n.Name, c.parent, got, c.want)
		}
	}
}

func TestModelHelpers(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{Name: "doc", Relations: []schema.RelationDefinition{{Name: "owner"}, {Name: "viewer"}}},
		{Name: "folder", Relations: []schema.RelationDefinition{{Name: "viewer"}, {Name: "parent"}}},
	}
	if got := strings.Join(relationNames(&migrator.DeployedModel{Types: types}), " "); got != "owner viewer parent" {
		t.Errorf("relationNames = %q; want each name once, in model order", got)
	}
	if got := strings.Join(relationsOf(types, "folder"), " "); got != "viewer parent" {
		t.Errorf("relationsOf(folder) = %q", got)
	}
	if got := relationsOf(types, "nope"); got != nil {
		t.Errorf("relationsOf(nope) = %v, want nil", got)
	}
}

// Analyze links every catalog name: per-relation functions of every kind to
// their relation, dispatchers to the SQL API page, and a name two functions
// share to the collisions.
func TestFuncLinks(t *testing.T) {
	types, err := parser.ParseSchemaString(`model
  schema 1.1
type user
type folder
  relations
    define viewer: [user]
type doc
  relations
    define parent: [folder]
    define viewer: [user] or viewer from parent
    define can_read_security_advisories_and_vulnerability_reports: viewer from parent
    define x_y: [user]
type doc_x
  relations
    define y: [user]
`)
	if err != nil {
		t.Fatal(err)
	}
	r, err := sqlapi.Describe(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	links := funcLinks(r)
	long := r.Find("doc", "can_read_security_advisories_and_vulnerability_reports")
	for _, f := range long.Functions {
		if links[f.Name] != relationURL("doc", "can_read_security_advisories_and_vulnerability_reports") {
			t.Errorf("%s (%s) links to %q", f.Name, f.Kind, links[f.Name])
		}
	}
	for name, want := range map[string]templ.SafeURL{
		"list_doc_viewer_obj":       relationURL("doc", "viewer"),
		"accessible_doc_viewer":     relationURL("doc", "viewer"),
		"explain_doc_viewer":        relationURL("doc", "viewer"),
		"check_permission_internal": "/sql#dispatchers",
		"list_accessible_subjects":  "/sql#dispatchers",
	} {
		if links[name] != want {
			t.Errorf("%s links to %q, want %q", name, links[name], want)
		}
	}
	if len(r.Collisions) == 0 {
		t.Fatal("the model has no colliding names")
	}
	for _, c := range r.Collisions {
		if links[c.Name] != "/sql#collisions" {
			t.Errorf("colliding %s links to %q", c.Name, links[c.Name])
		}
	}

	// A relation function named like a dispatcher collides with it.
	types, err = parser.ParseSchemaString(`model
  schema 1.1
type user
type permission
  relations
    define internal: [user]
    define nw: [user]
`)
	if err != nil {
		t.Fatal(err)
	}
	if r, err = sqlapi.Describe(types, sqlgen.Exposure{}, "public"); err != nil {
		t.Fatal(err)
	}
	links = funcLinks(r)
	for _, name := range []string{"check_permission_internal", "check_permission_nw"} {
		if links[name] != "/sql#collisions" {
			t.Errorf("%s, a relation's and a dispatcher's, links to %q", name, links[name])
		}
	}
}

func TestGroupChecks(t *testing.T) {
	cats := groupChecks(&doctor.Report{Checks: []doctor.CheckResult{
		{Category: "Schema File", Status: doctor.StatusPass},
		{Category: "Performance", Status: doctor.StatusWarn},
		{Category: "Schema File", Status: doctor.StatusFail},
		{Category: "Performance", Status: doctor.StatusPass},
	}})
	if len(cats) != 2 || cats[0].Name != "Schema File" || cats[1].Name != "Performance" {
		t.Fatalf("categories = %+v; want first-seen order", cats)
	}
	if cats[0].Worst != doctor.StatusFail || cats[1].Worst != doctor.StatusWarn || len(cats[0].Checks) != 2 {
		t.Errorf("worst statuses = %v, %v", cats[0].Worst, cats[1].Worst)
	}
	if groupChecks(nil) != nil {
		t.Error("groupChecks(nil) should be nil")
	}
}

func TestWhatIfAndMatrixHelpers(t *testing.T) {
	yes, no := true, false
	allow, deny := &melange.Trace{Result: &yes}, &melange.Trace{Result: &no}
	for _, c := range []struct {
		base, with *melange.Trace
		want       string
	}{{deny, allow, "the what-if tuples grant it"}, {allow, deny, "the what-if tuples revoke it"}, {allow, allow, "no change"}, {deny, &melange.Trace{}, "no change"}} {
		if got := whatIfChange(c.base, c.with); got != c.want {
			t.Errorf("whatIfChange = %q, want %q", got, c.want)
		}
	}
	added := []melange.ContextualTuple{{Subject: melange.Object{Type: "user", ID: "bob"}, Relation: "viewer", Object: melange.Object{Type: "doc", ID: "1"}}}
	if !isAdded(melange.TupleRef{SubjectType: "user", SubjectID: "bob", Relation: "viewer", ObjectType: "doc", ObjectID: "1"}, added) {
		t.Error("isAdded should match the same tuple")
	}
	if isAdded(melange.TupleRef{SubjectType: "user", SubjectID: "bob", Relation: "editor", ObjectType: "doc", ObjectID: "1"}, added) {
		t.Error("isAdded should not match another relation")
	}
	if a, e, s := matrixCounts([]matrixCell{{Allowed: true}, {Allowed: false}, {Err: "timed out", Allowed: true}, {Allowed: true}, {Skip: &sqlapi.Unavailable{}}}); a != 2 || e != 1 || s != 1 {
		t.Errorf("matrixCounts = %d allowed, %d errors, %d skipped; want 2, 1, 1 (an errored cell counts as neither)", a, e, s)
	}
}
