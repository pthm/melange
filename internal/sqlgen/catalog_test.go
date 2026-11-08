package sqlgen

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
)

// catalogExposures are the exposures the catalog is checked under: the
// soundness test's default and restricted ones, plus each family off.
func catalogExposures(m exposureModel, seed int64) map[string]Exposure {
	exposures := exposuresFor(m.analyses, seed)
	for _, family := range []struct {
		name string
		set  func(*FunctionFamilies)
	}{
		{"accessible off", func(f *FunctionFamilies) { f.Accessible = boolPtr(false) }},
		{"parent_key off", func(f *FunctionFamilies) { f.ParentKey = boolPtr(false) }},
		{"explain and expand off", func(f *FunctionFamilies) { f.Explain, f.Expand = boolPtr(false), boolPtr(false) }},
		{"list_objects off", func(f *FunctionFamilies) { f.ListObjects = boolPtr(false) }},
	} {
		var e Exposure
		family.set(&e.Functions)
		exposures[family.name] = e
	}
	return exposures
}

// TestCatalog_MatchesGenerated checks the catalog against generation over
// the OpenFGA corpus and the testdata schemas under the default, restricted
// and family-off exposures: its names are exactly the ones migrate creates
// and tracks, its public accessible functions are AccessibleFunctions, its
// dispatchers are the checksummed entries, and every call a generated body
// makes is among the calls the catalog lists for that function.
func TestCatalog_MatchesGenerated(t *testing.T) {
	models := append(exposureCorpus(t), mustExposureModel(t, longNamesModel), mustExposureModel(t, collidingModel))
	for i, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			for label, e := range catalogExposures(m, int64(i)) {
				assertCatalogMatches(t, m, label, e, generateExposed(t, m, e))
			}
		})
	}
}

func assertCatalogMatches(t *testing.T, m exposureModel, label string, e Exposure, o exposureOutput) {
	t.Helper()
	cat, err := BuildCatalog(m.analyses, CatalogOptions{Exposure: e, DatabaseSchema: exposureSchema})
	if err != nil {
		t.Fatalf("%s [%s]: %v", m.name, label, err)
	}
	got := slices.Sorted(slices.Values(cat.InstalledNames()))
	want := slices.Sorted(slices.Values(o.names))
	if !slices.Equal(got, want) {
		t.Errorf("%s [%s]: catalog names differ from CollectFunctionNamesWithOptions:\n got %v\nwant %v", m.name, label, got, want)
	}
	createdSet := o.created()
	for _, name := range got {
		if !createdSet[name] {
			t.Errorf("%s [%s]: catalog lists %s, which is not created", m.name, label, name)
		}
	}
	for name := range createdSet {
		if !slices.Contains(got, name) {
			t.Errorf("%s [%s]: %s is created but not in the catalog", m.name, label, name)
		}
	}

	// The public accessible entries are the client inventory.
	inventory, err := AccessibleFunctions(m.analyses, e)
	if err != nil {
		t.Fatal(err)
	}
	var invNames, pubNames []string
	for _, f := range inventory {
		invNames = append(invNames, f.Name)
	}
	for _, r := range cat.Relations {
		for _, f := range r.Functions {
			if f.Public {
				pubNames = append(pubNames, f.Name)
				if f.Kind != KindAccessible && f.Kind != KindParentKey {
					t.Errorf("%s [%s]: %s (%s) is public", m.name, label, f.Name, f.Kind)
				}
			}
		}
	}
	slices.Sort(invNames)
	slices.Sort(pubNames)
	if !slices.Equal(invNames, pubNames) {
		t.Errorf("%s [%s]: public accessible functions differ from AccessibleFunctions:\n got %v\nwant %v", m.name, label, pubNames, invNames)
	}

	// Dispatchers with SQL of their own are the checksummed entries;
	// accessible_objects is checksummed with the accessible functions.
	var own []string
	for _, d := range cat.Dispatchers {
		if d.RenderedWith == "" && d.Name != accessibleObjectsDispatcherName {
			own = append(own, d.Name)
		}
		if d.RenderedWith != "" && !slices.ContainsFunc(cat.Dispatchers, func(x CatalogFunction) bool { return x.Name == d.RenderedWith }) {
			t.Errorf("%s [%s]: %s is rendered with unknown %s", m.name, label, d.Name, d.RenderedWith)
		}
	}
	var entries []string
	for _, nf := range o.dispatchers {
		entries = append(entries, nf.Name)
	}
	slices.Sort(own)
	slices.Sort(entries)
	if !slices.Equal(own, entries) {
		t.Errorf("%s [%s]: dispatchers with their own SQL %v, CollectDispatcherFunctions %v", m.name, label, own, entries)
	}
	if !slices.ContainsFunc(o.named, func(nf NamedFunction) bool { return nf.Name == accessibleObjectsDispatcherName }) {
		t.Errorf("%s [%s]: accessible_objects is not checksummed", m.name, label)
	}
	for _, d := range cat.Dispatchers {
		if entry := cmp.Or(d.RenderedWith, d.Name); !slices.Contains(created(entrySQL(o, entry)), d.Name) {
			t.Errorf("%s [%s]: %s is not created by the %s entry", m.name, label, d.Name, entry)
		}
	}

	// Every literal call in a body is listed in its function's Calls.
	bodies := functionSQL(append(slices.Clone(o.named), o.dispatchers...))
	colliding := make(map[string]bool)
	for _, c := range cat.Collisions {
		colliding[c.Name] = true
	}
	for _, r := range cat.Relations {
		for _, f := range r.Functions {
			if colliding[f.Name] {
				continue
			}
			for _, c := range calls(bodies[f.Name]) {
				if c.name != f.Name && !slices.Contains(f.Calls, c.name) {
					t.Errorf("%s [%s]: %s (%s) calls %s, which its catalog entry does not list (%v)", m.name, label, f.Name, f.Kind, c.name, f.Calls)
				}
			}
			for _, c := range f.Calls {
				if !createdSet[c] {
					t.Errorf("%s [%s]: %s lists %s, which is not created", m.name, label, f.Name, c)
				}
			}
		}
	}
	// A function that reads melange_tuples through the caller's search_path
	// says so, since its example then needs the path set: a body without a
	// search_path of its own that names melange_tuples unqualified, or calls
	// such a function. The note is on accessible functions only, which are
	// written to be inlined; some of them read only functions that set
	// their own path.
	callerPath := make(map[string]bool)
	qualified := `"` + exposureSchema + `"."melange_tuples"`
	for changed := true; changed; {
		changed = false
		for name, body := range bodies {
			if callerPath[name] || strings.Contains(body, "SET search_path") {
				continue
			}
			reads := strings.Contains(strings.ReplaceAll(withoutComments(body), qualified, ""), "melange_tuples")
			for _, c := range calls(body) {
				reads = reads || (c.name != name && callerPath[c.name])
			}
			if reads {
				callerPath[name], changed = true, true
			}
		}
	}
	for _, f := range append(slices.Clone(cat.Dispatchers), catalogFunctions(cat)...) {
		noted := strings.Contains(f.Note, "search_path")
		if colliding[f.Name] || noted == (f.Kind == KindAccessible) && (noted || !callerPath[f.Name]) {
			continue
		}
		t.Errorf("%s [%s]: %s (%s) reads melange_tuples through the caller's search_path = %v, note %q", m.name, label, f.Name, f.Kind, callerPath[f.Name], f.Note)
	}

	// A relation's public dispatchers and the dispatchers' answers agree,
	// and agree with PublicAPI.
	api := NewPublicAPI(m.analyses, e)
	answers := make(map[string][]string)
	for _, d := range cat.Dispatchers {
		answers[d.Name] = d.Answers
	}
	for _, r := range cat.Relations {
		key := r.Type + "#" + r.Relation
		for _, d := range publicDispatcherNames {
			if slices.Contains(r.Dispatchers, d) != slices.Contains(answers[d], key) {
				t.Errorf("%s [%s]: %s and %s disagree on whether it answers", m.name, label, key, d)
			}
		}
		if slices.Contains(r.Dispatchers, "check_permission") && !api.Check(r.Type, r.Relation) {
			t.Errorf("%s [%s]: check_permission answers for %s, which PublicAPI says raises", m.name, label, key)
		}
		if slices.Contains(r.Dispatchers, "list_accessible_subjects") && !api.ListSubjects(r.Type, r.Relation) {
			t.Errorf("%s [%s]: list_accessible_subjects answers for %s, which PublicAPI says raises", m.name, label, key)
		}
		if r.Exposed != e.Exposes(r.Type, r.Relation) {
			t.Errorf("%s [%s]: %s exposed = %v", m.name, label, key, r.Exposed)
		}
		assertKindsAccounted(t, m.name, label, r)
	}
	// Internal dispatchers route every installed function of their kind.
	for _, r := range cat.Relations {
		key := r.Type + "#" + r.Relation
		for kind, d := range map[FunctionKind]string{
			KindCheck: "check_permission_internal", KindListSubjects: listSubjectsInternalDispatcherName,
			KindExplain: "explain_permission_internal", KindExpand: "expand_permission_internal",
		} {
			if slices.ContainsFunc(r.Functions, func(f CatalogFunction) bool { return f.Kind == kind }) != slices.Contains(answers[d], key) {
				t.Errorf("%s [%s]: %s routes %s = %v, but its %s function is installed = %v", m.name, label, d, key, !slices.Contains(answers[d], key), kind, slices.Contains(answers[d], key))
			}
		}
	}
}

// assertKindsAccounted checks that each kind a relation can have is either
// installed or explained, never both.
func assertKindsAccounted(t *testing.T, model, label string, r CatalogRelation) {
	t.Helper()
	for _, kind := range []FunctionKind{KindCheck, KindListObjects, KindListSubjects, KindAccessible, KindExpand, KindExplain} {
		installed := slices.ContainsFunc(r.Functions, func(f CatalogFunction) bool { return f.Kind == kind })
		explained := slices.ContainsFunc(r.NotInstalled, func(n NotInstalled) bool { return n.Kind == kind })
		if installed == explained {
			t.Errorf("%s [%s]: %s#%s %s: installed %v, explained %v", model, label, r.Type, r.Relation, kind, installed, explained)
		}
	}
	for _, n := range r.NotInstalled {
		if n.Detail == "" || n.Reason == "" {
			t.Errorf("%s [%s]: %s#%s %s has no reason", model, label, r.Type, r.Relation, n.Kind)
		}
	}
}

// catalogFunctions returns every relation's functions.
func catalogFunctions(cat *Catalog) []CatalogFunction {
	var out []CatalogFunction
	for _, r := range cat.Relations {
		out = append(out, r.Functions...)
	}
	return out
}

// entrySQL returns the SQL of the checksummed entry named name.
func entrySQL(o exposureOutput, name string) string {
	for _, nf := range append(slices.Clone(o.dispatchers), o.named...) {
		if nf.Name == name {
			return nf.SQL
		}
	}
	return ""
}

// functionHeader matches one CREATE FUNCTION statement's header, up to its
// language, as functionSQL splits them.
var (
	functionHeader = regexp.MustCompile(`(?s)^CREATE OR REPLACE FUNCTION "` + exposureSchema + `"\."([^"]+)"\((.*?)\)\s*RETURNS\s+(.*?)\s+AS \$\$.*?\$\$\s*LANGUAGE (\w+)`)
	rowsClause     = regexp.MustCompile(`\s+ROWS \d+$`)
)

// TestCatalog_Signatures checks every catalog signature against the CREATE
// FUNCTION header generation renders for that name, over the corpus, for
// every kind and dispatcher. Signatures depend on the exposure only through
// accessible_objects (self-routing when one of list_objects and accessible
// is on) and internal dispatchers left with nothing to route.
func TestCatalog_Signatures(t *testing.T) {
	models := append(exposureCorpus(t), mustExposureModel(t, longNamesModel))
	var mu sync.Mutex
	seen := make(map[string]bool)
	t.Run("models", func(t *testing.T) {
		for i, m := range models {
			t.Run(m.name, func(t *testing.T) {
				t.Parallel()
				all := catalogExposures(m, int64(i))
				for _, label := range []string{"default", "accessible off", "half, check only", "one"} {
					assertSignatures(t, m, label, all[label], &mu, seen)
				}
			})
		}
	})
	for _, want := range append(slices.Clone(DispatcherFunctionNames), UnexposedFunctionName,
		"check plpgsql", "check_nw plpgsql", "list_objects plpgsql", "list_subjects plpgsql", "accessible sql",
		"parent_key plpgsql", "expand plpgsql", "explain plpgsql", "dispatcher sql", "dispatcher plpgsql") {
		if !seen[want] {
			t.Errorf("no generated function covered %s", want)
		}
	}
}

func assertSignatures(t *testing.T, m exposureModel, label string, e Exposure, mu *sync.Mutex, seen map[string]bool) {
	t.Helper()
	o := generateExposed(t, m, e)
	cat, err := BuildCatalog(m.analyses, CatalogOptions{Exposure: e})
	if err != nil {
		t.Fatal(err)
	}
	headers := make(map[string][]string)
	for name, sql := range functionSQL(append(slices.Clone(o.named), o.dispatchers...)) {
		if h := functionHeader.FindStringSubmatch(strings.TrimSpace(sql)); h != nil {
			headers[name] = h[2:]
		}
	}
	check := func(f CatalogFunction) {
		h, ok := headers[f.Name]
		if !ok {
			t.Errorf("%s [%s]: no header for %s", m.name, label, f.Name)
			return
		}
		if got, want := normalizeArgs(h[0]), normalizeSignatureArgs(f.Signature); got != want {
			t.Errorf("%s [%s]: %s (%s) args:\n got %s\nwant %s", m.name, label, f.Name, f.Kind, got, want)
		}
		if got, want := normalizeType(rowsClause.ReplaceAllString(h[1], "")), normalizeType(f.Signature.Returns); got != want {
			t.Errorf("%s [%s]: %s returns %s, catalog says %s", m.name, label, f.Name, got, want)
		}
		if h[2] != f.Signature.Language {
			t.Errorf("%s [%s]: %s is LANGUAGE %s, catalog says %s", m.name, label, f.Name, h[2], f.Signature.Language)
		}
		mu.Lock()
		seen[string(f.Kind)+" "+f.Signature.Language] = true
		if f.Kind == KindDispatcher {
			seen[f.Name] = true
		}
		mu.Unlock()
	}
	for _, r := range cat.Relations {
		for _, f := range r.Functions {
			check(f)
		}
	}
	for _, d := range cat.Dispatchers {
		check(d)
	}
}

func normalizeType(s string) string {
	s = regexp.MustCompile(`\bint\b`).ReplaceAllString(strings.ToLower(s), "integer")
	return strings.Join(strings.Fields(s), "")
}

func normalizeArgs(s string) string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, normalizeType(strings.Replace(a, " DEFAULT ", "=", 1)))
		}
	}
	return strings.Join(out, ",")
}

func normalizeSignatureArgs(s Signature) string {
	var out []string
	for _, a := range s.Args {
		arg := a.Name + a.Type
		if a.Default != "" {
			arg += "=" + a.Default
		}
		out = append(out, normalizeType(arg))
	}
	return strings.Join(out, ",")
}

// collidingModel has relations whose function names collide: a_b#c and
// a#b_c both name check_a_b_c, and the case of Viewer folds onto viewer.
const collidingModel = `
model
  schema 1.1
type user
type a_b
  relations
    define c: [user]
type a
  relations
    define b_c: [user]
type doc
  relations
    define viewer: [user]
    define Viewer: [user]
`

// TestCatalog_Collisions checks that a name several relations produce is
// listed once per relation and reported as a collision, with model names
// kept as written and SQL names folded.
func TestCatalog_Collisions(t *testing.T) {
	m := mustExposureModel(t, collidingModel)
	cat, err := BuildCatalog(m.analyses, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string][]string)
	for _, c := range cat.Collisions {
		byName[c.Name] = c.Owners
	}
	if got := slices.Sorted(slices.Values(byName["check_a_b_c"])); !slices.Equal(got, []string{"a#b_c (check)", "a_b#c (check)"}) {
		t.Errorf("check_a_b_c owners = %v", got)
	}
	if got := slices.Sorted(slices.Values(byName["check_doc_viewer"])); !slices.Equal(got, []string{"doc#Viewer (check)", "doc#viewer (check)"}) {
		t.Errorf("check_doc_viewer owners = %v", got)
	}
	r := cat.Find("doc", "Viewer")
	if r == nil || r.Functions[0].Name != "check_doc_viewer" || r.Functions[0].Relation != "Viewer" {
		t.Fatalf("doc#Viewer = %+v", r)
	}
	if n := strings.Count(strings.Join(cat.InstalledNames(), " "), "check_doc_viewer "); n != 2 {
		t.Errorf("check_doc_viewer is listed %d times, want once per relation", n)
	}
}

// TestCatalog_Examples pins the example call of each kind for the testdata
// schema: argument order differs by kind, list functions are paged, and a
// parent-key function's result is filtered on the link column.
func TestCatalog_Examples(t *testing.T) {
	m := mustExposureModel(t, readTestdataSchema(t, "schema.fga"))
	cat, err := BuildCatalog(m.analyses, CatalogOptions{DatabaseSchema: "authz"})
	if err != nil {
		t.Fatal(err)
	}
	examples := make(map[string]string)
	for _, f := range cat.Find("repository", "can_read").Functions {
		examples[string(f.Kind)] = f.Example
	}
	want := map[string]string{
		"check":         `SELECT "authz".check_repository_can_read('user', 'alice', '1');`,
		"list_objects":  `SELECT object_id, next_cursor FROM "authz".list_repository_can_read_obj('user', 'alice', 100, NULL);`,
		"list_subjects": `SELECT subject_id, next_cursor FROM "authz".list_repository_can_read_sub('1', 'user', 100, NULL);`,
		"accessible":    `SELECT object_id FROM "authz".accessible_repository_can_read('user', 'alice');`,
		"expand":        `SELECT "authz".expand_repository_can_read('1');`,
		"explain":       `SELECT "authz".explain_repository_can_read('user', 'alice', '1');`,
	}
	for kind, w := range want {
		if examples[kind] != w {
			t.Errorf("%s example:\n got %s\nwant %s", kind, examples[kind], w)
		}
	}

	r := cat.Find("pull_request", "can_read_security_advisories_and_vulnerability_reports")
	var pk *CatalogFunction
	for i := range r.Functions {
		if r.Functions[i].Kind == KindParentKey {
			pk = &r.Functions[i]
		}
	}
	if pk == nil {
		t.Fatalf("no parent-key function: %+v", r.Functions)
	}
	if pk.Name != "accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b" || !pk.Truncated || !pk.Public {
		t.Errorf("parent-key entry %+v", pk)
	}
	if pk.Link != "repo" || pk.ParentType != "repository" || !slices.Equal(pk.ParentRelations, []string{"can_admin"}) {
		t.Errorf("parent-key link %q, parent %q %v", pk.Link, pk.ParentType, pk.ParentRelations)
	}
	if want := `WHERE <repo column> IN (SELECT object_id FROM "authz".accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b('user', 'alice'))`; pk.Usage != want {
		t.Errorf("parent-key usage:\n got %s\nwant %s", pk.Usage, want)
	}
	if !slices.Contains(pk.Calls, "accessible_repository_can_admin") {
		t.Errorf("parent-key calls %v, want the parent's accessible function", pk.Calls)
	}
	if !strings.Contains(pk.Note, "Returns repository ids") {
		t.Errorf("parent-key note %q", pk.Note)
	}
	if pk.Signature.Language != "plpgsql" || pk.Signature.Paged {
		t.Errorf("parent-key signature %+v", pk.Signature)
	}

	dispatchers := make(map[string]CatalogFunction)
	for _, d := range cat.Dispatchers {
		dispatchers[d.Name] = d
	}
	for name, w := range map[string]string{
		"check_permission":         `SELECT "authz".check_permission('user', 'alice', 'assignee', 'issue', '1');`,
		"check_permission_bulk":    `SELECT idx, allowed FROM "authz".check_permission_bulk(ARRAY['user'], ARRAY['alice'], ARRAY['assignee'], ARRAY['issue'], ARRAY['1']);`,
		"list_accessible_subjects": `SELECT subject_id, next_cursor FROM "authz".list_accessible_subjects('issue', '1', 'assignee', 'user', 100, NULL);`,
		"expand_permission":        `SELECT "authz".expand_permission('issue', '1', 'assignee');`,
	} {
		if got := dispatchers[name].Example; got != w {
			t.Errorf("%s example:\n got %s\nwant %s", name, got, w)
		}
	}
	if d := dispatchers["check_permission_internal"]; d.Public || d.RenderedWith != "check_permission" {
		t.Errorf("check_permission_internal = %+v", d)
	}

	// The default schema leaves the calls unqualified.
	plain, err := BuildCatalog(m.analyses, CatalogOptions{DatabaseSchema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.Find("repository", "can_read").Functions[0].Example; got != `SELECT check_repository_can_read('user', 'alice', '1');` {
		t.Errorf("public example %s", got)
	}
}

// TestCatalog_Reasons checks why a family is not installed, under exposures
// that leave a relation out, turn families off, and for a relation with no
// access path.
func TestCatalog_Reasons(t *testing.T) {
	m := mustExposureModel(t, readTestdataSchema(t, "schema.fga"))
	reasons := func(cat *Catalog, typ, rel string) map[FunctionKind]NotInstalledReason {
		out := make(map[FunctionKind]NotInstalledReason)
		for _, n := range cat.Find(typ, rel).NotInstalled {
			out[n.Kind] = n.Reason
		}
		return out
	}
	off := boolPtr(false)
	cat, err := BuildCatalog(m.analyses, CatalogOptions{Exposure: Exposure{
		Relations: RelationPatterns{Include: []string{"repository#can_read"}},
		Functions: FunctionFamilies{Explain: off, ListSubjects: off, ParentKey: off},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if r := cat.Find("repository", "can_read"); !r.Exposed || !slices.Contains(r.Dispatchers, "check_permission") || slices.Contains(r.Dispatchers, "list_accessible_subjects") {
		t.Errorf("repository#can_read = %+v", r)
	}
	if got := reasons(cat, "repository", "can_read"); got[KindExplain] != ReasonOnDemand || got[KindListSubjects] != ReasonFamilyOff {
		t.Errorf("repository#can_read reasons %v", got)
	}
	// reader is installed for can_read, which reads it, but not exposed;
	// only expand_permission, which follows expand pointers, answers.
	reader := cat.Find("repository", "reader")
	if reader.Exposed || !slices.Equal(reader.Dispatchers, []string{"expand_permission"}) || len(reader.Functions) == 0 {
		t.Errorf("repository#reader = %+v", reader)
	}
	for _, f := range reader.Functions {
		if f.Public {
			t.Errorf("%s of an unexposed relation is public", f.Name)
		}
	}
	// reader is reached, so list_subjects is only off; explain_permission
	// raises for it even when compiled on demand.
	if got := reasons(cat, "repository", "reader"); got[KindExplain] != ReasonNotExposed || got[KindListSubjects] != ReasonFamilyOff {
		t.Errorf("repository#reader reasons %v", got)
	}
	// can_edit is not reached: turning a family on or compiling explain on
	// demand installs nothing for it, so every family says not exposed.
	got := reasons(cat, "issue", "can_edit")
	for _, kind := range []FunctionKind{KindCheck, KindListObjects, KindListSubjects, KindAccessible, KindExpand, KindExplain} {
		if got[kind] != ReasonNotExposed {
			t.Errorf("issue#can_edit %s reason %q, want %q", kind, got[kind], ReasonNotExposed)
		}
	}
	pk := reasons(cat, "pull_request", "can_read_security_advisories_and_vulnerability_reports")
	if pk[KindParentKey] != ReasonNotExposed {
		t.Errorf("parent-key reason of an unexposed relation %v", pk)
	}
	cat, err = BuildCatalog(m.analyses, CatalogOptions{Exposure: Exposure{Functions: FunctionFamilies{ParentKey: off}}})
	if err != nil {
		t.Fatal(err)
	}
	if pk := reasons(cat, "pull_request", "can_read_security_advisories_and_vulnerability_reports"); pk[KindParentKey] != ReasonFamilyOff {
		t.Errorf("parent-key reason with the family off %v", pk)
	}
	cat, err = BuildCatalog(m.analyses, CatalogOptions{Exposure: Exposure{Relations: RelationPatterns{Exclude: []string{"pull_request#can_read_security_*"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if pk := reasons(cat, "pull_request", "can_read_security_advisories_and_vulnerability_reports"); pk[KindParentKey] != ReasonNotExposed {
		t.Errorf("parent-key reason of an excluded relation %v", pk)
	}

	// A relation with no access path has no functions and no dispatcher
	// case.
	analyses := slices.Clone(m.analyses)
	for i := range analyses {
		if analyses[i].ObjectType == "issue" && analyses[i].Relation == "assignee" {
			analyses[i].Capabilities = GenerationCapabilities{CheckReason: "no access paths", ListReason: "no access paths"}
		}
	}
	cat, err = BuildCatalog(analyses, CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r := cat.Find("issue", "assignee")
	if len(r.Functions) != 0 || len(r.Dispatchers) != 0 {
		t.Errorf("issue#assignee without access paths = %+v", r)
	}
	for _, n := range r.NotInstalled {
		if n.Reason != ReasonNoAccessPath || strings.Contains(n.Detail, "generic") {
			t.Errorf("issue#assignee %s: %+v", n.Kind, n)
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(cat.InstalledNames())), slices.Sorted(slices.Values(CollectFunctionNames(analyses)))) {
		t.Error("catalog and CollectFunctionNames disagree on a relation without access paths")
	}
	for _, n := range r.NotInstalled {
		if strings.Contains(n.Detail, NotExposedErrCode) {
			t.Errorf("issue#assignee %s under the default exposure: %s", n.Kind, n.Detail)
		}
	}

	// Left out by the exposure, the dispatchers raise for it instead of
	// denying or returning no rows.
	cat, err = BuildCatalog(analyses, CatalogOptions{Exposure: Exposure{Relations: RelationPatterns{Exclude: []string{"issue#assignee"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range cat.Find("issue", "assignee").NotInstalled {
		if n.Reason != ReasonNoAccessPath || !strings.Contains(n.Detail, "raises "+NotExposedErrCode) || strings.Contains(n.Detail, "denies") || strings.Contains(n.Detail, "no rows") {
			t.Errorf("unexposed issue#assignee %s: %+v", n.Kind, n)
		}
	}
}

// TestCatalog_JSON pins the field names the CLI's JSON output documents.
func TestCatalog_JSON(t *testing.T) {
	m := mustExposureModel(t, readTestdataSchema(t, "schema.fga"))
	cat, err := BuildCatalog(m.analyses, CatalogOptions{Exposure: Exposure{Relations: RelationPatterns{Exclude: []string{"author"}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	rel := doc["relations"].([]any)[0].(map[string]any)
	for _, key := range []string{"type", "relation", "exposed", "dispatchers", "functions"} {
		if _, ok := rel[key]; !ok {
			t.Errorf("relation has no %q: %v", key, rel)
		}
	}
	fn := rel["functions"].([]any)[0].(map[string]any)
	for _, key := range []string{"name", "kind", "public", "type", "relation", "truncated", "signature", "example"} {
		if _, ok := fn[key]; !ok {
			t.Errorf("function has no %q: %v", key, fn)
		}
	}
	sig := fn["signature"].(map[string]any)
	for _, key := range []string{"args", "returns", "language", "paged"} {
		if _, ok := sig[key]; !ok {
			t.Errorf("signature has no %q: %v", key, sig)
		}
	}
	if _, ok := doc["dispatchers"].([]any)[0].(map[string]any)["answers"]; !ok {
		t.Error("dispatcher has no answers")
	}
	if ex := doc["exposure"].(map[string]any)["relations"].(map[string]any)["exclude"]; ex == nil {
		t.Errorf("exposure %v", doc["exposure"])
	}
}

func readTestdataSchema(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../test/testutil/testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
