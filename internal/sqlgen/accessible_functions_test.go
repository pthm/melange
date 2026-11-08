package sqlgen

import (
	"cmp"
	"slices"
	"strings"
	"testing"
)

// TestAccessibleFunctions_MatchGenerated checks the inventory against what
// generation emits, over the OpenFGA corpus and the testdata schemas under
// the default and several restricted exposures. The accessible functions
// the rendered SQL creates must equal the ones CollectFunctionNamesWithOptions
// tracks and the ones installedAccessibleFunctions lists; AccessibleFunctions
// must name a subset of them, and every created accessible function it leaves
// out must be the per-relation form of a relation the accessible family does
// not expose. So a client generated from the inventory names only installed
// functions, and every installed function of an exposed relation.
func TestAccessibleFunctions_MatchGenerated(t *testing.T) {
	models := append(exposureCorpus(t), mustExposureModel(t, longNamesModel))
	for i, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			exposures := exposuresFor(m.analyses, int64(i))
			exposures["accessible off"] = Exposure{Functions: FunctionFamilies{Accessible: boolPtr(false)}}
			exposures["parent_key off"] = Exposure{Functions: FunctionFamilies{ParentKey: boolPtr(false)}}
			for label, e := range exposures {
				assertAccessibleInventory(t, m, label, e, generateExposed(t, m, e))
			}
		})
	}
}

func assertAccessibleInventory(t *testing.T, m exposureModel, label string, e Exposure, o exposureOutput) {
	t.Helper()
	isAccessible := func(name string) bool {
		return strings.HasPrefix(name, "accessible_") && name != accessibleObjectsDispatcherName
	}
	created := make(map[string]bool)
	for name := range o.created() {
		if isAccessible(name) {
			created[name] = true
		}
	}
	tracked := make(map[string]bool)
	for _, name := range o.names {
		if isAccessible(name) {
			tracked[name] = true
		}
	}
	installed := make(map[string]bool)
	for _, f := range installedAccessibleFunctions(m.analyses, newExposureClosure(m.analyses, e)) {
		installed[f.Name] = true
	}
	for what, set := range map[string]map[string]bool{"tracked by CollectFunctionNamesWithOptions": tracked, "listed by installedAccessibleFunctions": installed} {
		for name := range set {
			if !created[name] {
				t.Errorf("%s [%s]: %s is %s but not created", m.name, label, name, what)
			}
		}
		for name := range created {
			if !set[name] {
				t.Errorf("%s [%s]: %s is created but not %s", m.name, label, name, what)
			}
		}
	}

	got, err := AccessibleFunctions(m.analyses, e)
	if err != nil {
		t.Fatalf("%s [%s]: %v", m.name, label, err)
	}
	if !slices.IsSortedFunc(got, func(a, b AccessibleFunction) int {
		return cmp.Or(cmp.Compare(a.ObjectType, b.ObjectType), cmp.Compare(a.Relation, b.Relation), cmp.Compare(a.Link, b.Link))
	}) {
		t.Errorf("%s [%s]: inventory is not sorted: %+v", m.name, label, got)
	}
	accessibleOn, parentKeyOn := enabled(e.Functions.Accessible), enabled(e.Functions.ParentKey)
	listed := make(map[string]bool)
	for _, f := range got {
		if listed[f.Name] {
			t.Errorf("%s [%s]: %s is listed twice", m.name, label, f.Name)
		}
		listed[f.Name] = true
		if !created[f.Name] {
			t.Errorf("%s [%s]: inventory names %s, which is not created", m.name, label, f.Name)
		}
		if !e.Exposes(f.ObjectType, f.Relation) {
			t.Errorf("%s [%s]: inventory names %s for unexposed %s#%s", m.name, label, f.Name, f.ObjectType, f.Relation)
		}
		if len(f.Name) > 63 {
			t.Errorf("%s [%s]: %s is longer than 63 bytes", m.name, label, f.Name)
		}
		switch {
		case f.Link == "" && (!accessibleOn || f.ParentType != "" || f.Name != AccessibleObjectsFunctionName(f.ObjectType, f.Relation)):
			t.Errorf("%s [%s]: unexpected per-relation entry %+v", m.name, label, f)
		case f.Link != "" && (!parentKeyOn || f.ParentType == "" || !strings.HasPrefix(f.Name, "accessible_")):
			t.Errorf("%s [%s]: unexpected parent-key entry %+v", m.name, label, f)
		}
	}

	// A created accessible function the inventory leaves out is the
	// per-relation form of a relation installed only for another one.
	parentKeys := make(map[string]bool)
	for _, pk := range ParentKeyFunctions(m.analyses) {
		parentKeys[pk.Name] = true
	}
	unexposed := make(map[string]bool)
	for _, a := range m.analyses {
		if !accessibleOn || !e.Exposes(a.ObjectType, a.Relation) {
			unexposed[AccessibleObjectsFunctionName(a.ObjectType, a.Relation)] = true
		}
	}
	for name := range created {
		if listed[name] {
			continue
		}
		if parentKeys[name] || !unexposed[name] {
			t.Errorf("%s [%s]: %s is created but the inventory leaves it out", m.name, label, name)
		}
	}
}

// longNamesModel has accessible and parent-key names over 63 bytes.
const longNamesModel = `
model
  schema 1.1
type user
type organization_with_a_long_type_name
  relations
    define member: [user]
type project_with_an_equally_long_type_name
  relations
    define organization_with_a_long_type_name: [organization_with_a_long_type_name]
    define can_read_security_advisories_and_vulnerability_reports: member from organization_with_a_long_type_name
    define viewer: [user]
`

// TestAccessibleFunctions_LongNames checks that the inventory carries the
// truncated and hashed names codegen creates, and that the type, relation
// and link stay untruncated.
func TestAccessibleFunctions_LongNames(t *testing.T) {
	m := mustExposureModel(t, longNamesModel)
	got, err := AccessibleFunctions(m.analyses, Exposure{})
	if err != nil {
		t.Fatal(err)
	}
	const (
		typ  = "project_with_an_equally_long_type_name"
		rel  = "can_read_security_advisories_and_vulnerability_reports"
		link = "organization_with_a_long_type_name"
	)
	want := map[string]bool{
		AccessibleObjectsFunctionName(typ, rel):                 false,
		SafeIdentifier("accessible_", typ, rel+"_by_"+link, ""): false,
	}
	for _, f := range got {
		if f.ObjectType != typ || f.Relation != rel {
			continue
		}
		if _, ok := want[f.Name]; !ok {
			t.Errorf("unexpected name %q for %+v", f.Name, f)
		}
		want[f.Name] = true
		if len(f.Name) != 63 || f.Name == "accessible_"+typ+"_"+rel {
			t.Errorf("%q is not truncated to 63 bytes", f.Name)
		}
		if f.Link != "" && (f.Link != link || f.ParentType != link) {
			t.Errorf("parent-key entry %+v: want link and parent type %q", f, link)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s missing from %+v", name, got)
		}
	}

	if _, err := AccessibleFunctions(m.analyses, Exposure{Relations: RelationPatterns{Include: []string{"a#b#c"}}}); err == nil {
		t.Error("an invalid pattern is accepted")
	}
}
