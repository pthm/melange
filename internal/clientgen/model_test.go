package clientgen

import (
	"slices"
	"testing"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
)

const selectedSchema = `model
  schema 1.1

type user

type folder
  relations
    define viewer: [user]
    define can_view: viewer

type document
  relations
    define parent: [folder]
    define owner: [user]
    define can_view: can_view from parent or owner
`

// Selected is every selected relation in model order (types as defined, the
// parser's relation order within one), under the same selection as the
// relation constants.
func TestResolve_Selected(t *testing.T) {
	types, err := parser.ParseSchemaString(selectedSchema)
	if err != nil {
		t.Fatal(err)
	}
	include := func(p ...string) compiler.RelationPatterns { return compiler.RelationPatterns{Include: p} }
	for _, tc := range []struct {
		name string
		cfg  *Config
		want []string
	}{
		{"default", nil, []string{"folder#can_view", "folder#viewer", "document#can_view", "document#owner", "document#parent"}},
		{
			"inherited sql.relations", &Config{Exposure: &compiler.Exposure{Relations: include("can_*")}},
			[]string{"folder#can_view", "document#can_view"},
		},
		{"client include wider than sql.relations", &Config{
			Exposure:  &compiler.Exposure{Relations: include("document#can_view")},
			Relations: &compiler.RelationPatterns{Include: []string{"can_*", "owner"}},
		}, []string{"folder#can_view", "document#can_view", "document#owner"}},
		{
			"client exclude narrows", &Config{Relations: &compiler.RelationPatterns{Exclude: []string{"folder#*", "parent"}}},
			[]string{"document#can_view", "document#owner"},
		},
		{"relation filter", &Config{RelationFilter: "can_"}, []string{"folder#can_view", "document#can_view"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := Resolve(types, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			relations := make(map[string]bool)
			for _, r := range m.Selected {
				got = append(got, r.Type+"#"+r.Relation)
				relations[r.Relation] = true
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Selected = %v, want %v", got, tc.want)
			}
			for _, r := range m.Relations {
				if !relations[r] {
					t.Errorf("relation constant %s is selected on no type", r)
				}
			}
			if len(relations) != len(m.Relations) {
				t.Errorf("Selected names %d relations, the constants %d", len(relations), len(m.Relations))
			}
		})
	}
}
