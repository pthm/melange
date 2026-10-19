package studio

import (
	"fmt"
	"slices"
	"testing"

	"github.com/pthm/melange/pkg/parser"
)

const relationDSL = `model
  schema 1.1
type user
type group
  relations
    define member: [user]
type folder
  relations
    define viewer: [user]
type doc
  relations
    define parent: [folder, group#member, user:*]
    define owner: [user]
    define banned: [user]
    define viewer: ([user, group#member] or owner or viewer from parent) but not banned
    define both: [user] and owner
`

func TestDefineText(t *testing.T) {
	got := defineText(relationDSL, "doc", "viewer")
	want := "type doc\n  relations\n    define viewer: ([user, group#member] or owner or viewer from parent) but not banned"
	if got != want {
		t.Errorf("defineText = %q", got)
	}
	if got := defineText(relationDSL, "folder", "owner"); got != "" {
		t.Errorf("missing relation: %q", got)
	}
}

func TestRelationEdges(t *testing.T) {
	types, err := parser.ParseSchemaString(relationDSL)
	if err != nil {
		t.Fatal(err)
	}
	ix := newRelIndex(types)
	str := func(es []relEdge) []string {
		out := make([]string, 0, len(es))
		for _, e := range es {
			out = append(out, fmt.Sprintf("%s#%s%v", e.Type, e.Relation, e.Kinds))
		}
		slices.Sort(out)
		return out
	}
	// The TTU resolves to folder only: the userset and wildcard parent refs
	// are not parents. "both" skips its own [this] part.
	for _, c := range []struct {
		rel  string
		deps []string
	}{
		{"viewer", []string{"doc#banned[but not]", "doc#owner[computed]", "doc#parent[tupleset]", "folder#viewer[from]", "group#member[userset]"}},
		{"both", []string{"doc#owner[and]"}},
	} {
		if got := str(ix.dependsOn("doc", c.rel)); !slices.Equal(got, c.deps) {
			t.Errorf("dependsOn(doc#%s) = %v, want %v", c.rel, got, c.deps)
		}
	}
	if got, want := str(ix.usedBy("doc", "owner")), []string{"doc#both[and]", "doc#viewer[computed]"}; !slices.Equal(got, want) {
		t.Errorf("usedBy(doc#owner) = %v, want %v", got, want)
	}
}
