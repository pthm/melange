package studio

import (
	"slices"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

func TestStatsFor(t *testing.T) {
	stored := func(dsl string) storedSchema {
		t.Helper()
		types, err := parser.ParseSchemaString(dsl)
		if err != nil {
			t.Fatal(err)
		}
		return storedSchema{DSL: dsl, Model: types, HasModel: true}
	}
	prev := stored(`model
  schema 1.1
type user
type team
type doc
  relations
    define owner: [user]
    define viewer: [user] or owner
    define old: [user]
`)
	cur := stored(`model
  schema 1.1
type user
type folder
type doc
  relations
    define owner: [user]
    define viewer: [user]
    define editor: [user]
`)
	st := statsFor(prev, cur)
	if !st.Lines || st.LinesAdded != 3 || st.LinesRemoved != 3 {
		t.Errorf("lines: +%d -%d (known %v), want +3 -3", st.LinesAdded, st.LinesRemoved, st.Lines)
	}
	if !st.Model || st.TypesAdded != 1 || st.TypesRemoved != 1 || st.RelsAdded != 1 || st.RelsRemoved != 1 || st.RelsChanged != 1 {
		t.Errorf("model stats: %+v", st)
	}
	if st.Breaking == 0 {
		t.Errorf("removing a type, a relation and a grant is breaking: %+v", st.Changes)
	}
	if got, want := statsSummary(st), "1 type added · 1 type removed · 1 relation added · 1 relation removed · 1 relation changed · "; len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("summary = %q", got)
	}
	if st := statsFor(storedSchema{}, cur); st.Lines || st.Model {
		t.Errorf("no recorded predecessor should give no stats: %+v", st)
	}
}

func TestGroupChanges(t *testing.T) {
	groups := groupChanges([]schema.Change{
		{Class: schema.ClassAdditive, Type: "doc", Relation: "viewer", Summary: "doc.viewer grants editor"},
		{Class: schema.ClassBreaking, Type: "doc", Relation: "viewer", Summary: "doc.viewer no longer grants owner"},
		{Class: schema.ClassAdditive, Type: "doc", Relation: "editor", Summary: "relation doc.editor added"},
		{Class: schema.ClassBreaking, Type: "doc", Relation: "viewer", Summary: "doc.viewer excludes blocked"},
		{Class: schema.ClassBreaking, Type: "team", Summary: "type team removed"},
	})
	if len(groups) != 2 || groups[0].Type != "doc" || groups[1].Type != "team" || groups[1].Status != "removed" {
		t.Fatalf("groups: %+v", groups)
	}
	rels := groups[0].Relations
	if len(rels) != 2 || rels[0].Name != "viewer" || rels[1].Name != "editor" || rels[1].Status != "added" {
		t.Fatalf("relations: %+v", rels)
	}
	want := []changeItem{{"−", "owner"}, {"+", "editor"}, {"+", "but not blocked"}}
	if !slices.Equal(rels[0].Items, want) || !rels[0].Breaking || rels[0].Status != "changed" {
		t.Errorf("viewer: %+v, want items %+v, breaking", rels[0], want)
	}
	if rels[1].Breaking {
		t.Errorf("an added relation is additive: %+v", rels[1])
	}
}
