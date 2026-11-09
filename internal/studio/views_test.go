package studio

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

func renderHTML(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	if err := c.Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// hostile is text a database could hand studio: a domain-table label, an
// id, a relation name, a check error. It must never reach the page as markup.
const hostile = `<img src=x onerror="alert(1)">`

var testModel = &migrator.DeployedModel{
	DSL: "model\n  schema 1.1\ntype user\ntype doc\n  relations\n    define owner: [user]\n    define viewer: [user] or owner\n",
	Types: []schema.TypeDefinition{
		{Name: "user"},
		{Name: "doc", Relations: []schema.RelationDefinition{{Name: "owner"}, {Name: "viewer"}}},
	},
}

// hostileReport is a SQL API report with hostile text in every field a
// recorded model or pg_proc can supply.
func hostileReport() *sqlapi.Report {
	fn := sqlapi.Function{
		CatalogFunction: sqlgen.CatalogFunction{
			Name: hostile, Kind: sqlgen.KindParentKey, Public: true, Type: hostile, Relation: hostile, Truncated: true,
			Link: hostile, ParentType: hostile, ParentRelations: []string{hostile},
			Signature: sqlgen.Signature{Args: []sqlgen.Arg{{Name: hostile, Type: hostile, Default: hostile}}, Returns: hostile, Language: hostile, Paged: true},
			Example:   hostile, Usage: hostile, Calls: []string{hostile}, Note: hostile, RenderedWith: hostile,
		},
		Status: sqlapi.StatusSignatureDiffers, Detail: hostile, StaleOverloads: []string{hostile},
	}
	disp := fn
	disp.Kind, disp.Type, disp.Relation, disp.Answers = sqlgen.KindDispatcher, "", "", []string{hostile}
	rel := sqlapi.Relation{
		Type: hostile, Relation: hostile, Dispatchers: []string{hostile}, Functions: []sqlapi.Function{fn},
		NotInstalled: []sqlgen.NotInstalled{{Kind: sqlgen.KindExplain, Reason: sqlgen.ReasonNotExposed, Detail: hostile}},
	}
	return &sqlapi.Report{
		Exposure:    sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{hostile}}},
		Relations:   []sqlapi.Relation{rel},
		Dispatchers: []sqlapi.Function{disp},
		Orphans:     []sqlapi.InstalledFunction{{Name: hostile, Arguments: hostile}},
		Unmanaged:   []sqlapi.InstalledFunction{{Name: hostile, Arguments: hostile}},
		Collisions:  []sqlgen.NameCollision{{Name: hostile, Owners: []string{hostile}}},
		Notes:       []string{hostile},
	}
}

func hostileSQLAPIData(typ string) sqlAPIData {
	r := hostileReport()
	d := sqlAPIData{
		Env: hostile, Schema: hostile, Type: typ, Report: r, Types: []sqlAPIType{{Name: hostile, Relations: 1}},
		Exposed: r.Relations, Internal: r.Relations, Drifted: r.Dispatchers,
	}
	return d
}

func TestViewsEscapeDatabaseText(t *testing.T) {
	yes := true
	ls := labels{"doc:1": hostile, "user:" + hostile: hostile}
	pg := listPage{First: true, More: true, Next: "/x?q=" + hostile}
	for name, c := range map[string]templ.Component{
		"entity rows": entityList([]entityRef{{Type: "doc", ID: "1"}, {Type: "user", ID: hostile}}, ls, pg),
		"access list": accessList(typeGroup{Type: "doc", Entities: []entityGroup{{ID: hostile, Relations: []string{hostile}}}, Errs: []string{hostile}},
			1, []entityGroup{{ID: hostile, Relations: []string{hostile}}}, ls, entityRef{Type: "user", ID: hostile}, true, pg),
		"direct rows": directRows(directPage{Side: sideObject, More: true, Next: "/x?" + hostile, Tuples: []melange.TupleRef{
			{SubjectType: "user", SubjectID: hostile + "#" + hostile, Relation: hostile, ObjectType: "doc", ObjectID: "1"},
		}}, ls),
		"trace":            traceView(&melange.Trace{Result: &yes, Subject: hostile, Relation: hostile, Object: hostile, Root: &melange.Node{Type: "direct", Label: hostile}}, time.Millisecond),
		"fga":              fgaSource("type " + hostile + "\n  relations\n    define " + hostile + ": [user] # " + hostile),
		"sql":              sqlSource("SELECT '" + hostile + "' -- " + hostile),
		"error":            errorBox(hostile),
		"matrix":           matrixView(hostile, hostile, []matrixCell{{Relation: hostile, Err: hostile}}, []string{hostile}, time.Millisecond),
		"shell":            shell(page{Env: hostile, Active: "model", Title: hostile}, errorBox(hostile)),
		"filter":           filterBar(templ.Attributes{"placeholder": hostile, "value": hostile}),
		"section":          secHead(hostile, hostile),
		"sql api overview": sqlAPIPage(hostileSQLAPIData("")),
		"sql api type":     sqlAPIPage(hostileSQLAPIData(hostile)),
		"sql api error":    sqlAPIPage(sqlAPIData{Err: hostile, Type: hostile, Kind: hostile}),
		"relation catalog": relationPage(relationData{
			Model: testModel, Type: hostile, Relation: hostile, Known: true,
			API: &hostileReport().Relations[0], Funcs: []sqlFunc{{Name: hostile, Label: hostile, Def: hostile}},
		}),
	} {
		got := renderHTML(t, c)
		if strings.Contains(got, "<img src=x") || strings.Contains(got, `onerror="alert`) {
			t.Errorf("%s: hostile text rendered as markup:\n%s", name, got)
		}
		if !strings.Contains(got, "&lt;img") {
			t.Errorf("%s: hostile text missing (want it escaped, not dropped):\n%s", name, got)
		}
	}
}

// csp-incompatible markup: the CSP allows only same-origin scripts and
// styles and htmx runs with allowEval off, so pages must carry no inline
// scripts, style attributes, inline event handlers or hx-on handlers.
var (
	scriptTag    = regexp.MustCompile(`<script\b[^>]*>`)
	inlineStyle  = regexp.MustCompile(`<style\b|\sstyle="`)
	inlineHandle = regexp.MustCompile(`\s(on[a-z]+|hx-on[:\w-]*)="`)
)

func TestRenderedPagesSuitTheCSP(t *testing.T) {
	report := &doctor.Report{Checks: []doctor.CheckResult{{Category: "Schema File", Status: doctor.StatusWarn, Message: "m", Details: "d", FixHint: "f"}}, Warnings: 1}
	ent := entityRef{Type: "user", ID: "1"}
	for name, c := range map[string]templ.Component{
		"model":    indexPage(indexData{Model: testModel, Type: "doc", Relation: "viewer", Tab: modelTabQuery}),
		"schema":   indexPage(indexData{Model: testModel, Tab: modelTabSchema}),
		"no model": indexPage(indexData{ModelErr: "none"}),
		"tuples":   tuplesPage("", []typeCount{{Type: "doc", Count: 3}}, ""),
		"browse":   browsePage(browseData{Type: "doc", Ents: []entityRef{{Type: "doc", ID: "1"}}, More: true, Total: 300}),
		"entity":   entityPage(entityData{Entity: ent, Tab: tabCan, Group: "doc", Groups: []string{"doc"}}),
		"direct": entityPage(entityData{
			Entity: ent, Tab: tabDirect, SubjectTotal: 1, SubjectMatch: 1,
			AsSubject: directPage{Side: sideSubject, Tuples: []melange.TupleRef{{SubjectType: "user", SubjectID: "1", Relation: "owner", ObjectType: "doc", ObjectID: "1"}}},
		}),
		"health":  healthPage("", "schemas/schema.fga", report, ""),
		"history": historyPage(historyData{Sel: -1, Err: "none"}),
		"relation": relationPage(relationData{Model: testModel, Type: "doc", Relation: "viewer", Known: true, Funcs: []sqlFunc{
			{Name: "check_doc_viewer", Label: "check", Def: "SELECT 1"},
			{Name: "accessible_doc_viewer", Label: "accessible (inlinable)", Def: "SELECT 1"},
		}}),
		"sql api overview": sqlAPIPage(hostileSQLAPIData("")),
		"sql api type":     sqlAPIPage(hostileSQLAPIData("doc")),
		"sql api no model": sqlAPIPage(sqlAPIData{Err: "No model recorded."}),
	} {
		got := renderHTML(t, c)
		for _, tag := range scriptTag.FindAllString(got, -1) {
			if !strings.Contains(tag, ` src="`) {
				t.Errorf("%s: inline script %s", name, tag)
			}
		}
		if m := inlineStyle.FindString(got); m != "" {
			t.Errorf("%s: inline style %q", name, m)
		}
		if m := inlineHandle.FindString(got); m != "" {
			t.Errorf("%s: inline handler %q", name, m)
		}
		if !strings.Contains(got, `<meta name="htmx-config" content='{"allowEval":false,"includeIndicatorStyles":false}'>`) {
			t.Errorf("%s: htmx-config meta missing or changed", name)
		}
	}
}

func TestInfiniteScrollMarkup(t *testing.T) {
	ents := []entityRef{{Type: "doc", ID: "1"}, {Type: "doc", ID: "2"}}
	sentinel := `<li class="more-rows" hx-get="/browse/doc/list?offset=100&amp;q=" hx-trigger="intersect once" hx-swap="outerHTML">`

	first := renderHTML(t, entityList(ents, nil, listPage{First: true, More: true, Next: browseNextURL("doc", "", 100)}))
	if !strings.HasPrefix(strings.TrimSpace(first), `<ul class="rows">`) || !strings.Contains(first, sentinel) || strings.Count(first, `class="row"`) != 2 {
		t.Errorf("first page with more: want the list, 2 rows and the sentinel:\n%s", first)
	}
	later := renderHTML(t, entityList(ents, nil, listPage{More: true, Next: browseNextURL("doc", "", 100)}))
	if strings.Contains(later, "<ul") || !strings.Contains(later, sentinel) {
		t.Errorf("later page: want bare rows plus sentinel (it replaces the old sentinel in place):\n%s", later)
	}
	last := renderHTML(t, entityList(ents, nil, listPage{First: true}))
	if strings.Contains(last, "more-rows") {
		t.Errorf("last page must not fetch more:\n%s", last)
	}
	if got := renderHTML(t, entityList(nil, nil, listPage{First: true})); !strings.Contains(got, "No matches.") {
		t.Errorf("empty list: %s", got)
	}

	g := typeGroup{Type: "doc", Capped: true, Entities: make([]entityGroup, 5000)}
	self := entityRef{Type: "user", ID: "1"}
	for name, c := range map[string]struct {
		matched int
		want    string
	}{
		"unfiltered": {5000, "5,000+ doc entities"},
		"filtered":   {2, "2 of 5,000+ doc entities"},
		"no match":   {0, "Nothing matches the filter."},
	} {
		got := renderHTML(t, accessList(g, c.matched, []entityGroup{{ID: "1"}}, nil, self, true, listPage{First: true}))
		if !strings.Contains(strings.Join(strings.Fields(got), " "), c.want) {
			t.Errorf("access list %s: want %q in\n%s", name, c.want, got)
		}
	}
	if got := renderHTML(t, accessList(typeGroup{Type: "doc"}, 0, nil, nil, self, true, listPage{First: true})); !strings.Contains(got, "None.") {
		t.Errorf("empty group: %s", got)
	}

	rows := renderHTML(t, directRows(directPage{Side: sideObject, More: true, Next: "/entity/direct?offset=100", Tuples: []melange.TupleRef{
		{SubjectType: "group", SubjectID: "eng#member", Relation: "viewer", ObjectType: "doc", ObjectID: "1"},
	}}, nil))
	if !strings.Contains(rows, `<tr class="more-rows" hx-get="/entity/direct?offset=100" hx-trigger="intersect once" hx-swap="outerHTML"><td colspan="4"`) {
		t.Errorf("direct rows sentinel:\n%s", rows)
	}
	if i, j := strings.Index(rows, ">viewer<"), strings.Index(rows, ">group<"); i < 0 || j < 0 || i > j || !strings.Contains(rows, ">eng<") || !strings.Contains(rows, "#member") {
		t.Errorf("object side shows relation, then the subject's type and id (with #relation):\n%s", rows)
	}
}

func TestQueryTabModes(t *testing.T) {
	got := renderHTML(t, queryTab(indexData{Model: testModel, Type: "doc", Relation: "viewer"}))
	for _, m := range queryModes {
		if !strings.Contains(got, `value="`+m.ID+`"`) {
			t.Errorf("mode %s has no radio", m.ID)
		}
	}
	for _, want := range []string{
		`<input type="radio" name="mode" value="explain" checked>`,
		`data-for="explain matrix analyze">Subject`,
		`data-for="explain expand analyze">Relation`,
		`<label class="field">Object`, // every mode reads the object
		`<details class="disclose whatif" data-for="explain">`,
		`name="relation" placeholder="viewer" value="viewer"`,
		`name="object" placeholder="document:1" value="doc:"`,
		`<option value="doc:">`,
		`hx-post="/query"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("query tab missing %q", want)
		}
	}
	// The form hides fields per mode in CSS; a mode added in Go without its
	// rule would show every field. Keep the two in step.
	css, err := fs.ReadFile(assets, "assets/studio.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range queryModes {
		rule := `.query:has([value="` + m.ID + `"]:checked) [data-for]:not([data-for~="` + m.ID + `"])`
		if !strings.Contains(string(css), rule) {
			t.Errorf("studio.css has no rule for mode %s: %s", m.ID, rule)
		}
	}
}

func TestTopLinks(t *testing.T) {
	saved := version.Version
	t.Cleanup(func() { version.Version = saved; latestRelease.Store(nil) })

	version.Version = "0.9.6"
	latestRelease.Store(nil)
	got := renderHTML(t, topLinks())
	if !strings.Contains(got, `href="`+releasesURL+`/tag/v0.9.6"`) || strings.Contains(got, "available") {
		t.Errorf("release build: want a release-notes link and no update badge:\n%s", got)
	}
	for _, link := range []string{docsURL, repoURL} {
		if !strings.Contains(got, `href="`+link+`" target="_blank" rel="noopener noreferrer"`) {
			t.Errorf("missing outbound link %s with noopener", link)
		}
	}

	SetLatestRelease("0.9.7")
	got = renderHTML(t, topLinks())
	if !strings.Contains(got, `href="`+releasesURL+`/latest"`) || !strings.Contains(got, "v0.9.7 available") || !strings.Contains(got, upgradeHint) {
		t.Errorf("update known: want the badge with the upgrade hint:\n%s", got)
	}

	version.Version = "v0.9.6-0.20261018190901-38c4ca1c55f9+dirty"
	got = renderHTML(t, topLinks())
	if !strings.Contains(got, `<span class="t-mono" title="melange v0.9.6-0.20261018190901-38c4ca1c55f9+dirty">dev 38c4ca1</span>`) {
		t.Errorf("dev build: want an unlinked short version with the full one in the title:\n%s", got)
	}
}

func TestVerdictsAndSummaries(t *testing.T) {
	yes, no := true, false
	if got := renderHTML(t, verdictBadge(&melange.Trace{Result: &yes})); !strings.Contains(got, "badge ok") || !strings.Contains(got, "allowed") {
		t.Errorf("allowed badge: %s", got)
	}
	if got := renderHTML(t, verdictBadge(&melange.Trace{Result: &no})); !strings.Contains(got, "badge fail") || !strings.Contains(got, "denied") {
		t.Errorf("denied badge: %s", got)
	}
	m := strings.Join(strings.Fields(renderHTML(t, matrixView("user:1", "doc:1", []matrixCell{
		{Relation: "owner", Allowed: true}, {Relation: "viewer"}, {Relation: "editor", Err: "timed out after 10s"},
	}, nil, 3*time.Millisecond))), " ")
	for _, want := range []string{"1 of 3 relations allowed", "1 error", "timed out after 10s", "3.0 ms"} {
		if !strings.Contains(m, want) {
			t.Errorf("matrix missing %q:\n%s", want, m)
		}
	}
	added := []melange.ContextualTuple{{Subject: melange.Object{Type: "user", ID: "bob"}, Relation: "viewer", Object: melange.Object{Type: "doc", ID: "1"}}}
	with := &melange.Trace{Result: &yes, Root: &melange.Node{Type: "direct", Evidence: []melange.TupleRef{{SubjectType: "user", SubjectID: "bob", Relation: "viewer", ObjectType: "doc", ObjectID: "1"}}}}
	w := strings.Join(strings.Fields(renderHTML(t, whatIfView(&melange.Trace{Result: &no}, with, added, "", time.Millisecond, time.Millisecond))), " ")
	for _, want := range []string{"the what-if tuples grant it", "Without what-if"} {
		if !strings.Contains(w, want) {
			t.Errorf("what-if view missing %q:\n%s", want, w)
		}
	}
	if strings.Count(w, "what-if") < 3 {
		t.Errorf("added evidence should be tagged what-if:\n%s", w)
	}
}

func TestAccessEntityCollapsesChips(t *testing.T) {
	rels := make([]string, chipLimit+3)
	for i := range rels {
		rels[i] = fmt.Sprintf("r%d", i)
	}
	got := renderHTML(t, accessEntity("user", entityGroup{ID: "1", Relations: rels}, nil, entityRef{Type: "doc", ID: "1"}, false, nil))
	more := strings.Index(got, "data-more-chips")
	if more < 0 || !strings.Contains(got, "+3 more") {
		t.Fatalf("no +3 more button:\n%s", got)
	}
	if i, j := strings.Index(got, fmt.Sprintf(">r%d<", chipLimit-1)), strings.Index(got, fmt.Sprintf(">r%d<", chipLimit)); i > more || j < more {
		t.Errorf("button should sit between chip %d and %d:\n%s", chipLimit-1, chipLimit, got)
	}
	short := renderHTML(t, accessEntity("user", entityGroup{ID: "1", Relations: rels[:chipLimit]}, nil, entityRef{Type: "doc", ID: "1"}, false, nil))
	if strings.Contains(short, "data-more-chips") {
		t.Errorf("no button at exactly chipLimit relations:\n%s", short)
	}
}

// The drift stat counts what the Drift section lists, orphans included, and
// the sidebar can drop either filter.
func TestSQLAPIPageCountsAndFilters(t *testing.T) {
	r := &sqlapi.Report{
		Dispatchers: []sqlapi.Function{{CatalogFunction: sqlgen.CatalogFunction{Name: "check_permission", Kind: sqlgen.KindDispatcher}, Status: sqlapi.StatusMissing}},
		Orphans:     []sqlapi.InstalledFunction{{Name: "check_doc_old", Arguments: "p_subject_type text"}},
	}
	overview := strings.Join(strings.Fields(renderHTML(t, sqlAPIPage(sqlAPIData{Report: r, Drifted: r.Dispatchers}))), " ")
	if !strings.Contains(overview, `<span class="t-heading">2</span>drifted`) {
		t.Errorf("drift stat does not count the orphan:\n%s", overview)
	}

	d := sqlAPIData{Type: "doc", Kind: "check", Report: r, Types: []sqlAPIType{{Name: "doc", Relations: 1}}}
	page := renderHTML(t, sqlAPIPage(d))
	for _, href := range []string{`href="/sql?type=doc"`, `href="/sql?kind=check"`} {
		if !strings.Contains(page, href) {
			t.Errorf("sidebar has no %s link", href)
		}
	}
}
