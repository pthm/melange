package studio

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// exposureDSL: exposing document#editor installs editor and owner (which
// editor reads), and leaves viewer and auditor out.
const exposureDSL = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
    define auditor: [user]
`

func exposureModel(t *testing.T, e migrator.Exposure) *migrator.DeployedModel {
	t.Helper()
	types, err := parser.ParseSchemaString(exposureDSL)
	if err != nil {
		t.Fatal(err)
	}
	return &migrator.DeployedModel{DSL: exposureDSL, Types: types, Exposure: e}
}

func editorOnlyModel(t *testing.T) *migrator.DeployedModel {
	return exposureModel(t, migrator.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"document#editor"}}})
}

func offFamilies(families ...string) sqlgen.FunctionFamilies {
	var f sqlgen.FunctionFamilies
	for _, name := range families {
		switch name {
		case "explain":
			f.Explain = new(bool)
		case "expand":
			f.Expand = new(bool)
		case "list_subjects":
			f.ListSubjects = new(bool)
		}
	}
	return f
}

// listed records which relations an access group listed.
type listed struct {
	mu   sync.Mutex
	rels []string
}

func (l *listed) list(_ context.Context, rel string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rels = append(l.rels, rel)
	return []string{"1"}, nil
}

func TestAccessGroupSkipsUnexposedRelations(t *testing.T) {
	model := editorOnlyModel(t)
	rels := relationsOf(model.Types, "document")
	var l listed
	g := accessGroup(context.Background(), apiOf(model), tabCan, "document", rels, "document", l.list)
	if !slices.Equal(l.rels, []string{"editor"}) {
		t.Errorf("listed %v; want only the exposed editor", l.rels)
	}
	skipped := make([]string, 0, len(g.Skipped))
	for _, u := range g.Skipped {
		skipped = append(skipped, u.Relation+":"+u.Label())
	}
	slices.Sort(skipped)
	if want := []string{"auditor:not exposed", "owner:not exposed", "viewer:not exposed"}; !slices.Equal(skipped, want) {
		t.Errorf("skipped %v; want %v", skipped, want)
	}
	if len(g.Entities) != 1 || !slices.Equal(g.Entities[0].Relations, []string{"editor"}) {
		t.Errorf("entities %+v", g.Entities)
	}
	if len(g.NoExplain) != 0 {
		t.Errorf("an exposed relation can be explained: %v", g.NoExplain)
	}

	got := renderHTML(t, accessList(g, len(g.Entities), g.Entities, nil, entityRef{Type: "user", ID: "1"}, true, listPage{First: true}))
	for _, want := range []string{"Not listed:", "<code>viewer</code>", "not exposed", `hx-post="/explain"`} {
		if !strings.Contains(got, want) {
			t.Errorf("access list missing %q:\n%s", want, got)
		}
	}
}

func TestAccessGroupWithExplainOff(t *testing.T) {
	model := exposureModel(t, migrator.Exposure{Functions: offFamilies("explain")})
	var l listed
	g := accessGroup(context.Background(), apiOf(model), tabBy, "document", relationsOf(model.Types, "document"), "user", l.list)
	if len(l.rels) != 4 || len(g.Skipped) != 0 {
		t.Errorf("every relation is listed: listed %v, skipped %v", l.rels, g.Skipped)
	}
	if u := g.NoExplain["viewer"]; u == nil || u.Label() != "not installed" {
		t.Fatalf("NoExplain[viewer] = %v", u)
	}
	got := renderHTML(t, accessList(g, len(g.Entities), g.Entities, nil, entityRef{Type: "document", ID: "1"}, false, listPage{First: true}))
	if strings.Contains(got, `hx-post="/explain"`) {
		t.Errorf("no explain chip with the explain functions off:\n%s", got)
	}
	if !strings.Contains(got, `class="chip off"`) || !strings.Contains(got, "sql.functions.explain off") {
		t.Errorf("relations are labeled, with why on hover:\n%s", got)
	}
}

// The default exposure, a model recorded before the exposure column
// (which reads as the default) and no model all answer for everything.
func TestDefaultExposureAnswersEverything(t *testing.T) {
	for name, model := range map[string]*migrator.DeployedModel{
		"default":  exposureModel(t, migrator.Exposure{}),
		"no model": nil,
	} {
		api := apiOf(model)
		for _, rel := range []string{"owner", "editor", "viewer", "auditor"} {
			for _, f := range []sqlapi.Family{sqlapi.Check, sqlapi.ListObjects, sqlapi.ListSubjects, sqlapi.Explain, sqlapi.Expand} {
				if u := api.Unavailable(f, "document", rel); u != nil {
					t.Errorf("%s: %s document#%s: %v", name, f.Name(), rel, u)
				}
			}
		}
	}
	model := exposureModel(t, migrator.Exposure{})
	var l listed
	g := accessGroup(context.Background(), apiOf(model), tabCan, "document", relationsOf(model.Types, "document"), "document", l.list)
	if len(l.rels) != 4 || g.Skipped != nil || g.NoExplain != nil {
		t.Errorf("default: listed %v, skipped %v, noExplain %v", l.rels, g.Skipped, g.NoExplain)
	}
}

func TestMatrixViewLabelsUnexposedRelations(t *testing.T) {
	api := apiOf(editorOnlyModel(t))
	cells := []matrixCell{
		{Relation: "editor", Allowed: true},
		{Relation: "viewer", Skip: api.Unavailable(sqlapi.Check, "document", "viewer"), NoExplain: api.Unavailable(sqlapi.Explain, "document", "viewer")},
	}
	got := strings.Join(strings.Fields(renderHTML(t, matrixView("user:1", "document:1", cells, nil, time.Millisecond))), " ")
	for _, want := range []string{"1 of 2 relations allowed", "1 not exposed", ">not exposed</span>", `class="chip off"`, "--include-relation document#viewer"} {
		if !strings.Contains(got, want) {
			t.Errorf("matrix missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, `hx-post="/explain"`) != 1 {
		t.Errorf("only editor explains:\n%s", got)
	}
	if strings.Contains(got, "denied") {
		t.Errorf("an unchecked relation is not denied:\n%s", got)
	}
}

// Expand drill-ins follow expand_permission: a pointer into owner, which
// is not exposed but which editor's expand reaches, keeps its drill-in; a
// pointer into a relation expand does not answer for is labeled.
func TestExpandViewOffersOnlyAnsweredDrillIns(t *testing.T) {
	tree := &melange.UsersetTree{Root: &melange.UsersetTreeNode{Name: "document:1#editor", Union: &melange.Nodes{Nodes: []*melange.UsersetTreeNode{
		{Name: "document:1#editor", Leaf: &melange.Leaf{Computed: &melange.Computed{Userset: "document:1#owner"}}},
		{Name: "document:1#editor", Leaf: &melange.Leaf{Computed: &melange.Computed{Userset: "document:1#viewer"}}},
	}}}}
	got := renderHTML(t, expandView(tree, true, time.Millisecond, pointerCheck(apiOf(editorOnlyModel(t)))))
	if !strings.Contains(got, `/expand?userset=document%3A1%23owner`) {
		t.Errorf("owner is in editor's expand closure, so it drills in:\n%s", got)
	}
	if strings.Contains(got, `/expand?userset=document%3A1%23viewer`) || !strings.Contains(got, "not exposed") {
		t.Errorf("viewer is labeled, without a drill-in:\n%s", got)
	}

	got = renderHTML(t, expandView(tree, true, time.Millisecond, pointerCheck(apiOf(exposureModel(t, migrator.Exposure{})))))
	if strings.Count(got, `hx-get="/expand?userset=`) != 2 || strings.Contains(got, "not exposed") {
		t.Errorf("the default exposure drills into every pointer:\n%s", got)
	}
}
