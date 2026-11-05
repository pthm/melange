package studio

import (
	"testing"

	"github.com/pthm/melange/pkg/parser"
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
