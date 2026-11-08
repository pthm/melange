package sqlgen

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

var updateExposureGolden = flag.Bool("update-exposure-golden", false, "rewrite testdata/exposure_default/*.golden from the current default-exposure output")

func boolPtr(b bool) *bool { return &b }

// exposureSchema is the database schema exposure tests generate into, so
// every generated call is a quoted, schema-qualified name.
const exposureSchema = "authz"

type exposureModel struct {
	name     string
	analyses []RelationAnalysis
	inline   InlineSQLData
}

func compileExposureTypes(name string, types []schema.TypeDefinition) (exposureModel, error) {
	if err := schema.DetectCycles(types); err != nil {
		return exposureModel{}, err
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := ComputeCanGenerate(AnalyzeRelations(types, closure))
	return exposureModel{name: name, analyses: analyses, inline: BuildInlineSQLData(closure, analyses)}, nil
}

func mustExposureModel(t *testing.T, src string) exposureModel {
	t.Helper()
	types, err := parser.ParseSchemaString(src)
	if err != nil {
		t.Fatal(err)
	}
	m, err := compileExposureTypes(t.Name(), types)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// exposureOutput is everything generated for one model under one exposure.
type exposureOutput struct {
	gen         GeneratedSQL
	list        ListGeneratedSQL
	named       []NamedFunction
	dispatchers []NamedFunction
	names       []string
}

func generateExposed(t *testing.T, m exposureModel, e Exposure) exposureOutput {
	t.Helper()
	opts := GenerateSQLOptions{Exposure: e}
	gen, err := GenerateSQLWithOptions(m.analyses, m.inline, exposureSchema, opts)
	if err != nil {
		t.Fatalf("%s: GenerateSQLWithOptions: %v", m.name, err)
	}
	list, err := GenerateListSQLWithOptions(m.analyses, m.inline, exposureSchema, opts)
	if err != nil {
		t.Fatalf("%s: GenerateListSQLWithOptions: %v", m.name, err)
	}
	return exposureOutput{
		gen:         gen,
		list:        list,
		named:       CollectNamedFunctions(gen, list, m.analyses),
		dispatchers: CollectDispatcherFunctions(gen, list),
		names:       CollectFunctionNamesWithOptions(m.analyses, opts),
	}
}

// sql returns every generated statement.
func (o exposureOutput) sql() []string {
	var out []string
	for _, nf := range o.named {
		out = append(out, nf.SQL)
	}
	for _, nf := range o.dispatchers {
		out = append(out, nf.SQL)
	}
	return out
}

var createdFunction = regexp.MustCompile(`CREATE OR REPLACE FUNCTION "` + exposureSchema + `"\."([^"]+)"\(`)

// created returns the names of the functions sql creates, in order.
func created(sql string) []string {
	var names []string
	for _, m := range createdFunction.FindAllStringSubmatch(sql, -1) {
		names = append(names, m[1])
	}
	return names
}

func (o exposureOutput) created() map[string]bool {
	set := make(map[string]bool)
	for _, sql := range o.sql() {
		for _, n := range created(sql) {
			set[n] = true
		}
	}
	return set
}

// withoutComments drops SQL comment lines, which name functions in prose.
func withoutComments(sql string) string {
	lines := strings.Split(sql, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "--") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

var calledFunction = regexp.MustCompile(`"` + exposureSchema + `"\."([^"]+)"\(`)

// call is one schema-qualified function call in a generated body.
type call struct {
	name string
	args []string
}

// calls returns the schema-qualified calls in sql (comments excluded), with
// their top-level arguments.
func calls(sql string) []call {
	sql = withoutComments(sql)
	var out []call
	for _, loc := range calledFunction.FindAllStringSubmatchIndex(sql, -1) {
		name := sql[loc[2]:loc[3]]
		out = append(out, call{name: name, args: splitArgs(sql[loc[1]:])})
	}
	return out
}

// splitArgs splits the argument list that starts s (just after the opening
// parenthesis) at its top-level commas.
func splitArgs(s string) []string {
	var args []string
	depth, start, inQuote := 0, 0, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			}
		case c == '\'':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				return append(args, strings.TrimSpace(s[start:i]))
			}
			depth--
		case c == ',' && depth == 0:
			args = append(args, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	return args
}

// literal returns the value of a quoted SQL string literal.
func literal(arg string) (string, bool) {
	if len(arg) < 2 || arg[0] != '\'' || arg[len(arg)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(arg[1:len(arg)-1], "''", "'"), true
}

// dispatcherCallArgs gives, for each internal dispatcher generated bodies
// call, the positions of its relation and object type arguments and the
// per-relation functions it routes a (type, relation) pair to.
var dispatcherCallArgs = map[string]struct {
	relation, objectType int
	routes               func(objectType, relation string) []string
}{
	"check_permission_internal": {2, 3, func(t, r string) []string { return []string{functionName(t, r)} }},
	"check_permission_nw_internal": {2, 3, func(t, r string) []string {
		return []string{functionNameNoWildcard(t, r), functionName(t, r)}
	}},
	"explain_permission_internal":      {2, 3, func(t, r string) []string { return []string{explainFunctionName(t, r)} }},
	listSubjectsInternalDispatcherName: {2, 0, func(t, r string) []string { return []string{listSubjectsFunctionName(t, r)} }},
}

// internalDispatcherArms gives, for each internal dispatcher, the function
// its arm for a (type, relation) pair calls given the functions created, or
// "" when the pair has no installed function of the dispatcher's kind.
var internalDispatcherArms = map[string]func(objectType, relation string, created map[string]bool) string{
	"check_permission_internal": func(t, r string, created map[string]bool) string {
		return ifCreated(created, functionName(t, r))
	},
	"check_permission_nw_internal": func(t, r string, created map[string]bool) string {
		// Relations that reach no wildcard have no _nw variant and route to
		// the base function.
		if created[functionNameNoWildcard(t, r)] {
			return functionNameNoWildcard(t, r)
		}
		return ifCreated(created, functionName(t, r))
	},
	"explain_permission_internal": func(t, r string, created map[string]bool) string {
		return ifCreated(created, explainFunctionName(t, r))
	},
	"expand_permission_internal": func(t, r string, created map[string]bool) string {
		return ifCreated(created, expandFunctionName(t, r))
	},
	listSubjectsInternalDispatcherName: func(t, r string, created map[string]bool) string {
		return ifCreated(created, listSubjectsFunctionName(t, r))
	},
}

func ifCreated(created map[string]bool, fn string) string {
	if created[fn] {
		return fn
	}
	return ""
}

// functionSQL returns the SQL of each function the statements in fns create,
// by name. A statement creating several functions is split at each CREATE.
func functionSQL(fns []NamedFunction) map[string]string {
	out := make(map[string]string)
	for _, nf := range fns {
		locs := createdFunction.FindAllStringSubmatchIndex(nf.SQL, -1)
		for i, loc := range locs {
			end := len(nf.SQL)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			out[nf.SQL[loc[2]:loc[3]]] = nf.SQL[loc[0]:end]
		}
	}
	return out
}

// publicDispatchers are the entry points generated bodies must not call:
// under a restricted exposure they raise for relations the bodies need.
var publicDispatchers = []string{
	"check_permission", "check_permission_nw", "check_permission_bulk",
	"list_accessible_objects", "list_accessible_subjects", accessibleObjectsDispatcherName,
	"explain_permission", "expand_permission",
}

// owners maps each per-relation function name, parent-key functions
// included, to the relation it belongs to.
func owners(analyses []RelationAnalysis) map[string]*RelationAnalysis {
	out := make(map[string]*RelationAnalysis)
	byKey := make(map[string]*RelationAnalysis)
	for i := range analyses {
		a := &analyses[i]
		byKey[a.ObjectType+"."+a.Relation] = a
		for _, n := range []string{
			functionName(a.ObjectType, a.Relation),
			functionNameNoWildcard(a.ObjectType, a.Relation),
			listObjectsFunctionName(a.ObjectType, a.Relation),
			listSubjectsFunctionName(a.ObjectType, a.Relation),
			AccessibleObjectsFunctionName(a.ObjectType, a.Relation),
			explainFunctionName(a.ObjectType, a.Relation),
			expandFunctionName(a.ObjectType, a.Relation),
		} {
			out[n] = a
		}
	}
	for _, pk := range ParentKeyFunctions(analyses) {
		out[pk.Name] = byKey[pk.ObjectType+"."+pk.Relation]
	}
	return out
}

// exposureBaseline is what the default exposure generates for a model: the
// functions it creates and the SQL of each named function.
type exposureBaseline struct {
	created map[string]bool
	bodies  map[string]string
}

func newExposureBaseline(o exposureOutput) exposureBaseline {
	b := exposureBaseline{created: o.created(), bodies: make(map[string]string, len(o.named))}
	for _, nf := range o.named {
		b.bodies[nf.Name] = nf.SQL
	}
	return b
}

// assertExposureSound reports every exposureProblems finding for o.
func assertExposureSound(t *testing.T, m exposureModel, label string, o exposureOutput, base exposureBaseline) {
	t.Helper()
	for _, p := range exposureProblems(m, o, base) {
		t.Errorf("%s [%s]: %s", m.name, label, p)
	}
}

// exposureProblems checks one generated output against base, the default
// exposure's output for the model: the names collected match what is
// created, each named function is paired with its own SQL, every call
// targets a generated function, and no body calls a public dispatcher.
// Every named function except accessible_objects has the default body, so
// bodies do not depend on the exposure. Every call a per-relation body
// makes, direct or through an internal dispatcher, must follow a
// relationReferences edge of its relation (so Reach installs the callee),
// and a dispatcher call must reach every function the dispatcher routes the
// pair to under the default exposure (so a disabled family cannot uninstall
// it). Each internal dispatcher has an arm for exactly the installed
// functions of its kind, so a call that reaches one is routed to it.
func exposureProblems(m exposureModel, o exposureOutput, base exposureBaseline) []string {
	var problems []string
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	createdSet := o.created()
	nameSet := make(map[string]bool, len(o.names))
	for _, n := range o.names {
		if nameSet[n] {
			fail("CollectFunctionNames lists %s twice", n)
		}
		nameSet[n] = true
	}
	for n := range createdSet {
		if !nameSet[n] {
			fail("%s is generated but not in CollectFunctionNames", n)
		}
	}
	for n := range nameSet {
		if !createdSet[n] {
			fail("%s is in CollectFunctionNames but not generated", n)
		}
	}

	for _, nf := range o.named {
		if got := created(nf.SQL); len(got) != 1 || got[0] != nf.Name {
			fail("named function %s pairs with SQL creating %v", nf.Name, got)
		}
		// accessible_objects routes by its own family under a restricted
		// exposure (see generateAccessibleObjectsDispatcher).
		if nf.Name == accessibleObjectsDispatcherName {
			continue
		}
		if want, ok := base.bodies[nf.Name]; !ok {
			fail("%s is generated, but not under the default exposure", nf.Name)
		} else if nf.SQL != want {
			fail("%s differs from its body under the default exposure", nf.Name)
		}
	}

	internalSQL := functionSQL(o.dispatchers)
	for name, arm := range internalDispatcherArms {
		sql, ok := internalSQL[name]
		if !ok {
			fail("internal dispatcher %s is not generated", name)
			continue
		}
		want := make(map[string]bool)
		for _, a := range m.analyses {
			if fn := arm(a.ObjectType, a.Relation, createdSet); fn != "" {
				want[fn] = true
			}
		}
		got := make(map[string]bool)
		for _, c := range calls(sql) {
			// The CREATE header names the dispatcher itself.
			if c.name != name {
				got[c.name] = true
			}
		}
		for fn := range want {
			if !got[fn] {
				fail("%s has no arm for %s, which is generated", name, fn)
			}
		}
		for fn := range got {
			if !want[fn] {
				fail("%s calls %s, which is not an installed function of its kind", name, fn)
			}
		}
	}

	own := owners(m.analyses)
	for _, nf := range append(slices.Clone(o.named), o.dispatchers...) {
		isDispatcher := slices.ContainsFunc(o.dispatchers, func(d NamedFunction) bool { return d.Name == nf.Name })
		owner := own[nf.Name]
		var refs []string
		if owner != nil {
			refs = append(relationReferences(owner), owner.ObjectType+"."+owner.Relation)
		}
		for _, c := range calls(nf.SQL) {
			if !createdSet[c.name] {
				fail("%s calls %s, which is not generated", nf.Name, c.name)
			}
			if !isDispatcher && nf.Name != accessibleObjectsDispatcherName && slices.Contains(publicDispatchers, c.name) {
				fail("%s calls the public dispatcher %s", nf.Name, c.name)
			}
			if isDispatcher || owner == nil {
				continue
			}
			if callee := own[c.name]; callee != nil {
				if key := callee.ObjectType + "." + callee.Relation; !slices.Contains(refs, key) {
					fail("%s calls %s, but %s is not a relationReferences edge of %s.%s",
						nf.Name, c.name, key, owner.ObjectType, owner.Relation)
				}
				continue
			}
			pos, ok := dispatcherCallArgs[c.name]
			if !ok {
				continue
			}
			if len(c.args) <= max(pos.relation, pos.objectType) {
				fail("%s: cannot parse %s call arguments %v", nf.Name, c.name, c.args)
				continue
			}
			relation, ok := literal(c.args[pos.relation])
			if !ok {
				fail("%s calls %s with a non-literal relation %s", nf.Name, c.name, c.args[pos.relation])
				continue
			}
			// A literal type names one target; a dynamic one (a TTU parent)
			// can be any type the edges allow.
			var targets []string
			objectType, typeIsLiteral := literal(c.args[pos.objectType])
			for _, ref := range refs {
				if typeIsLiteral && ref == objectType+"."+relation || !typeIsLiteral && strings.HasSuffix(ref, "."+relation) {
					targets = append(targets, strings.TrimSuffix(ref, "."+relation))
				}
			}
			if len(targets) == 0 {
				fail("%s calls %s for (%s, %s), which is not a relationReferences edge of %s.%s",
					nf.Name, c.name, c.args[pos.objectType], relation, owner.ObjectType, owner.Relation)
			}
			for _, target := range targets {
				for _, fn := range pos.routes(target, relation) {
					if base.created[fn] && !createdSet[fn] {
						fail("%s calls %s for (%s, %s), but %s, which it routes to by default, is not generated",
							nf.Name, c.name, target, relation, fn)
					}
				}
			}
		}
	}
	return problems
}

// exposuresFor returns the exposures the soundness test generates each model
// under: the default and several restricted ones, chosen deterministically.
func exposuresFor(analyses []RelationAnalysis, seed int64) map[string]Exposure {
	rng := rand.New(rand.NewSource(seed))
	var qualified []string
	for _, a := range analyses {
		qualified = append(qualified, a.ObjectType+"#"+a.Relation)
	}
	if len(qualified) == 0 {
		return map[string]Exposure{"default": {}}
	}
	subset := func(p float64) []string {
		var out []string
		for _, q := range qualified {
			if rng.Float64() < p {
				out = append(out, q)
			}
		}
		if len(out) == 0 {
			out = []string{qualified[rng.Intn(len(qualified))]}
		}
		return out
	}
	var alternate []string
	for i, q := range qualified {
		if i%2 == 1 {
			alternate = append(alternate, q)
		}
	}
	return map[string]Exposure{
		"default": {},
		"one":     {Relations: RelationPatterns{Include: []string{qualified[len(qualified)-1]}}},
		"third":   {Relations: RelationPatterns{Include: subset(1.0 / 3)}},
		"exclude-alternate, parent_key without list_objects/accessible": {
			Relations: RelationPatterns{Exclude: alternate},
			Functions: FunctionFamilies{ListObjects: boolPtr(false), Accessible: boolPtr(false), Explain: boolPtr(false)},
		},
		"half, list_subjects only": {
			Relations: RelationPatterns{Include: subset(0.5)},
			Functions: FunctionFamilies{ListObjects: boolPtr(false), Accessible: boolPtr(false), ParentKey: boolPtr(false), Expand: boolPtr(false)},
		},
		"list_subjects off": {Functions: FunctionFamilies{ListSubjects: boolPtr(false)}},
		"third, list_subjects off": {
			Relations: RelationPatterns{Include: subset(1.0 / 3)},
			Functions: FunctionFamilies{ListSubjects: boolPtr(false)},
		},
		"half, check only": {
			Relations: RelationPatterns{Include: subset(0.5)},
			Functions: FunctionFamilies{
				ListObjects: boolPtr(false), ListSubjects: boolPtr(false), Accessible: boolPtr(false),
				ParentKey: boolPtr(false), Expand: boolPtr(false), Explain: boolPtr(false),
			},
		},
	}
}

// upstreamModels is the vendored copy of the upstream OpenFGA suite's
// models, which the root module cannot load from github.com/openfga/openfga.
// TestOpenFGA_UpstreamModelsVendored in the test module keeps it current.
const upstreamModels = "testdata/openfga_upstream_models.yaml"

// exposureCorpus loads every model of the upstream and melange OpenFGA YAML
// suites and the testdata schemas.
func exposureCorpus(t *testing.T) []exposureModel {
	t.Helper()
	local, err := filepath.Glob("../../test/openfgatests/testdata/*.yaml")
	if err != nil || len(local) == 0 {
		t.Fatalf("no YAML corpus found: %v", err)
	}
	var models []exposureModel
	// Each source must keep most of its models: a floor well under today's
	// count (161 upstream, 165 local) still notices a source that stops
	// loading or a parser change that starts rejecting models wholesale.
	for _, src := range []struct {
		name  string
		files []string
		floor int
	}{
		{"upstream suite", []string{upstreamModels}, 150},
		{"melange suite", local, 150},
	} {
		before := len(models)
		for _, f := range src.files {
			models = append(models, yamlModels(t, f)...)
		}
		if n := len(models) - before; n < src.floor {
			t.Fatalf("the %s yields %d models, expected at least %d", src.name, n, src.floor)
		}
	}
	for _, f := range exposureTestdataSchemas(t) {
		types, err := parser.ParseSchema(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := compileExposureTypes(filepath.Base(f), types)
		if err != nil {
			t.Fatal(err)
		}
		models = append(models, m)
	}
	return models
}

// yamlModels compiles every stage model of an OpenFGA YAML test file,
// skipping the models the suite expects to be rejected.
func yamlModels(t *testing.T, f string) []exposureModel {
	t.Helper()
	data, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Tests []struct {
			Name   string `json:"name"`
			Stages []struct {
				Model string `json:"model"`
			} `json:"stages"`
		} `json:"tests"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("%s: %v", f, err)
	}
	var models []exposureModel
	for _, tc := range doc.Tests {
		for i, st := range tc.Stages {
			if st.Model == "" {
				continue
			}
			types, err := parser.ParseSchemaString(st.Model)
			if err != nil {
				continue
			}
			m, err := compileExposureTypes(fmt.Sprintf("%s/%s/stage%d", filepath.Base(f), tc.Name, i), types)
			if err != nil {
				continue
			}
			models = append(models, m)
		}
	}
	return models
}

func exposureTestdataSchemas(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../../test/testutil/testdata/*.fga")
	if err != nil || len(files) == 0 {
		t.Fatalf("no testdata schemas found: %v", err)
	}
	return files
}

// TestExposure_ClosureSound generates every corpus model under the default
// and several restricted exposures and checks each output with
// assertExposureSound. A codegen edge relationReferences misses, an internal
// dispatcher arm dropped for an installed function, or a body that changes
// with the exposure fails here instead of becoming a silently uninstalled or
// unrouted function.
func TestExposure_ClosureSound(t *testing.T) {
	models := exposureCorpus(t)
	for i, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			base := newExposureBaseline(generateExposed(t, m, Exposure{}))
			for label, e := range exposuresFor(m.analyses, int64(i)) {
				assertExposureSound(t, m, label, generateExposed(t, m, e), base)
			}
		})
	}
}

// TestExposure_ProblemsCatchRegressions checks that exposureProblems reports
// the codegen regressions TestExposure_ClosureSound exists to catch, by
// injecting each into an otherwise sound output: a call appended to a named
// function's body, or a dispatcher arm rerouted to another function.
func TestExposure_ProblemsCatchRegressions(t *testing.T) {
	m := mustExposureModel(t, exposureDocModel)
	base := newExposureBaseline(generateExposed(t, m, Exposure{}))
	off := boolPtr(false)
	viewerOnly := Exposure{Relations: RelationPatterns{Include: []string{"doc#viewer"}}}
	// rerouted makes a dispatcher's arms for owner call the viewer function
	// instead, as if its arms were built from the exposed relations.
	rerouted := func(fn func(objectType, relation string) string) [2]string {
		return [2]string{`"authz"."` + fn("doc", "owner") + `"(`, `"authz"."` + fn("doc", "viewer") + `"(`}
	}
	for _, tc := range []struct {
		name, fn, call, want string
		replace              [2]string
		e                    Exposure
	}{
		{
			name: "direct call without an edge",
			fn:   "check_doc_viewer",
			call: `"authz"."check_doc_secret"(p_subject_type, p_subject_id, p_object_id, ARRAY[]::TEXT[])`,
			want: "doc.secret is not a relationReferences edge of doc.viewer",
		},
		{
			name: "list_subjects call with list_subjects off",
			fn:   "check_doc_viewer",
			call: `"authz"."list_accessible_subjects_internal"('doc', p_object_id, 'owner', 'user', NULL, NULL)`,
			want: "list_doc_owner_sub, which it routes to by default, is not generated",
			e:    Exposure{Functions: FunctionFamilies{ListSubjects: off}},
		},
		{
			name: "explain call with explain off",
			fn:   "list_doc_viewer_obj",
			call: `"authz"."explain_permission_internal"(p_subject_type, p_subject_id, 'owner', 'doc', 'x', 1)`,
			want: "explain_doc_owner, which it routes to by default, is not generated",
			e:    Exposure{Functions: FunctionFamilies{Explain: off}},
		},
		{
			name: "body that depends on the exposure",
			fn:   "check_doc_viewer",
			call: `"authz"."check_doc_owner"(p_subject_type, p_subject_id, p_object_id, ARRAY[]::TEXT[])`,
			want: "check_doc_viewer differs from its body under the default exposure",
			e:    viewerOnly,
		},
		{
			name:    "check_permission_internal arm dropped",
			fn:      "check_permission",
			replace: rerouted(functionName),
			want:    "check_permission_internal has no arm for check_doc_owner, which is generated",
			e:       viewerOnly,
		},
		{
			name:    "check_permission_nw_internal arm dropped",
			fn:      "check_permission_nw",
			replace: rerouted(functionName),
			want:    "check_permission_nw_internal has no arm for check_doc_owner, which is generated",
			e:       viewerOnly,
		},
		{
			name:    "explain_permission_internal arm dropped",
			fn:      "explain_permission",
			replace: rerouted(explainFunctionName),
			want:    "explain_permission_internal has no arm for explain_doc_owner, which is generated",
			e:       viewerOnly,
		},
		{
			name:    "expand_permission_internal arm dropped",
			fn:      "expand_permission",
			replace: rerouted(expandFunctionName),
			want:    "expand_permission_internal has no arm for expand_doc_owner, which is generated",
			e:       viewerOnly,
		},
		{
			name:    "list_accessible_subjects_internal arm dropped",
			fn:      "list_accessible_subjects",
			replace: rerouted(listSubjectsFunctionName),
			want:    "list_accessible_subjects_internal has no arm for list_doc_owner_sub, which is generated",
			e:       viewerOnly,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := generateExposed(t, m, tc.e)
			if problems := exposureProblems(m, o, base); len(problems) > 0 {
				t.Fatalf("output is unsound before injecting: %v", problems)
			}
			o.named, o.dispatchers = slices.Clone(o.named), slices.Clone(o.dispatchers)
			fns := o.named
			if tc.replace[0] != "" {
				fns = o.dispatchers
			}
			i := slices.IndexFunc(fns, func(nf NamedFunction) bool { return nf.Name == tc.fn })
			if i < 0 {
				t.Fatalf("%s is not generated", tc.fn)
			}
			if tc.replace[0] != "" {
				if !strings.Contains(fns[i].SQL, tc.replace[0]) {
					t.Fatalf("%s does not contain %s", tc.fn, tc.replace[0])
				}
				fns[i].SQL = strings.ReplaceAll(fns[i].SQL, tc.replace[0], tc.replace[1])
			} else {
				fns[i].SQL += "\nSELECT " + tc.call + ";"
			}
			problems := exposureProblems(m, o, base)
			if !slices.ContainsFunc(problems, func(p string) bool { return strings.Contains(p, tc.want) }) {
				t.Errorf("problems %v do not report %q", problems, tc.want)
			}
		})
	}
}

// goldenDigest renders the digest of a generated output pinned by the
// default-exposure golden files.
func goldenDigest(o exposureOutput) string {
	digest := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:8])
	}
	var b strings.Builder
	b.WriteString("# CollectNamedFunctions\n")
	for _, nf := range o.named {
		fmt.Fprintf(&b, "%s %s\n", nf.Name, digest(nf.SQL))
	}
	b.WriteString("# CollectDispatcherFunctions\n")
	for _, nf := range o.dispatchers {
		fmt.Fprintf(&b, "%s %s\n", nf.Name, digest(nf.SQL))
	}
	fmt.Fprintf(&b, "# CollectFunctionNames %s\n", digest(strings.Join(o.names, "\n")))
	return b.String()
}

// TestExposure_DefaultGolden pins the default-exposure output for the
// testdata schemas. It was recorded from the output before exposure support,
// which differs only by the list_*_sub calls now routed to
// check_permission_internal and list_accessible_subjects_internal, and by
// list_accessible_subjects_internal itself. Some schema.golden entries were
// re-recorded from post-exposure output when the long-name pull_request
// relation was added: that relation's functions, the list_pull_request_*_sub
// functions, the dispatchers and the function name digest. Those entries are
// a snapshot, not a pre-exposure baseline. Exposures that leave nothing out
// for the model must produce the same output, as must the signatures without
// options. Run with -update-exposure-golden after an intended codegen change.
func TestExposure_DefaultGolden(t *testing.T) {
	for _, f := range exposureTestdataSchemas(t) {
		name := strings.TrimSuffix(filepath.Base(f), ".fga")
		t.Run(name, func(t *testing.T) {
			types, err := parser.ParseSchema(f)
			if err != nil {
				t.Fatal(err)
			}
			m, err := compileExposureTypes(name, types)
			if err != nil {
				t.Fatal(err)
			}
			want := generateExposed(t, m, Exposure{})
			got := goldenDigest(want)

			path := filepath.Join("testdata", "exposure_default", name+".golden")
			if *updateExposureGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with -update-exposure-golden)", err)
			}
			if got != string(golden) {
				t.Errorf("default-exposure output differs from %s (run with -update-exposure-golden if intended):\n%s", path, firstDiff(string(golden), got))
			}

			for _, sql := range want.sql() {
				if strings.Contains(sql, NotExposedErrCode) || strings.Contains(sql, UnexposedFunctionName) {
					t.Fatalf("default output renders the not-exposed raise")
				}
			}

			gen, err := GenerateSQL(m.analyses, m.inline, exposureSchema)
			if err != nil {
				t.Fatal(err)
			}
			list, err := GenerateListSQL(m.analyses, m.inline, exposureSchema)
			if err != nil {
				t.Fatal(err)
			}
			legacy := exposureOutput{
				gen: gen, list: list, named: CollectNamedFunctions(gen, list, m.analyses),
				dispatchers: CollectDispatcherFunctions(gen, list), names: CollectFunctionNames(m.analyses),
			}
			if d := goldenDigest(legacy); d != got {
				t.Errorf("signatures without options differ from the default exposure:\n%s", firstDiff(got, d))
			}

			everything := []Exposure{
				{Relations: RelationPatterns{Include: []string{"*"}}},
				{Relations: RelationPatterns{Include: []string{"*#*"}, Exclude: []string{"no_such_relation", "nope#*"}}},
				{Functions: FunctionFamilies{
					ListObjects: boolPtr(true), ListSubjects: boolPtr(true), Accessible: boolPtr(true),
					ParentKey: boolPtr(true), Expand: boolPtr(true), Explain: boolPtr(true),
				}},
			}
			for _, e := range everything {
				if d := goldenDigest(generateExposed(t, m, e)); d != got {
					t.Errorf("exposure %+v leaves nothing out but changes the output:\n%s", e, firstDiff(got, d))
				}
			}
		})
	}
}

// expectSQL fails unless sql contains every want.
func expectSQL(t *testing.T, label, sql string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(sql, w) {
			t.Errorf("%s: SQL missing %q:\n%s", label, w, sql)
		}
	}
}

// rejectSQL fails if sql contains any unwanted string.
func rejectSQL(t *testing.T, label, sql string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(sql, u) {
			t.Errorf("%s: SQL unexpectedly contains %q:\n%s", label, u, sql)
		}
	}
}

func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want %q\n  got  %q", i+1, wl, gl)
		}
	}
	return "(no line differs)"
}

func TestExposure_PatternMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern, objectType, relation string
		want                          bool
	}{
		{"viewer", "document", "viewer", true},
		{"viewer", "folder", "viewer", true},
		{"viewer", "document", "viewers", false},
		{"view*", "document", "viewer", true},
		{"*", "document", "viewer", true},
		{"can_?", "document", "can_x", true},
		{"can_?", "document", "can_xy", false},
		{"document#viewer", "document", "viewer", true},
		{"document#viewer", "folder", "viewer", false},
		{"doc*#viewer", "document", "viewer", true},
		{"*#viewer", "folder", "viewer", true},
		{"document#*", "document", "owner", true},
		// '*' never crosses '#': a relation-only pattern sees the relation only.
		{"doc*", "document", "viewer", false},
		{"*viewer", "document", "viewer", true},
		{"document*", "document", "viewer", false},
		// '.' is a literal, not a separator.
		{"team.v2#member", "team.v2", "member", true},
		{"team.v2#member", "team", "member", false},
		{"team.*#member", "team.v2", "member", true},
		{"a*b*c", "t", "aXbYc", true},
		{"a*b*c", "t", "aXbY", false},
	} {
		if got := patternMatches(tc.pattern, tc.objectType, tc.relation); got != tc.want {
			t.Errorf("patternMatches(%q, %q, %q) = %v, want %v", tc.pattern, tc.objectType, tc.relation, got, tc.want)
		}
	}
}

func TestExposure_IncludeExclude(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns RelationPatterns
		want     map[string]bool
	}{
		{"empty include exposes all", RelationPatterns{}, map[string]bool{"doc#viewer": true, "doc#owner": true, "org#member": true}},
		{"include", RelationPatterns{Include: []string{"viewer", "org#*"}}, map[string]bool{"doc#viewer": true, "doc#owner": false, "org#member": true}},
		{"exclude wins", RelationPatterns{Include: []string{"*"}, Exclude: []string{"doc#viewer"}}, map[string]bool{"doc#viewer": false, "doc#owner": true, "org#member": true}},
		{"exclude without include", RelationPatterns{Exclude: []string{"owner", "org#*"}}, map[string]bool{"doc#viewer": true, "doc#owner": false, "org#member": false}},
	} {
		e := Exposure{Relations: tc.patterns}
		for key, want := range tc.want {
			ot, rel, _ := strings.Cut(key, "#")
			if got := e.Exposes(ot, rel); got != want {
				t.Errorf("%s: Exposes(%s) = %v, want %v", tc.name, key, got, want)
			}
		}
	}
}

func TestExposure_Validate(t *testing.T) {
	for _, p := range []string{"*", "viewer", "doc*#view?", "team.v2#member", "*#*", "can_read"} {
		if err := ValidateRelationPattern(p); err != nil {
			t.Errorf("ValidateRelationPattern(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"", "a#b#c", "#viewer", "document#", "#", "view er", "doc:1", "user@x", "viewer\t"} {
		if err := ValidateRelationPattern(p); err == nil {
			t.Errorf("ValidateRelationPattern(%q) = nil, want an error", p)
		}
	}
	err := Exposure{Relations: RelationPatterns{Include: []string{"viewer"}, Exclude: []string{"a#b#c"}}}.Validate()
	if err == nil || !strings.Contains(err.Error(), "relations.exclude") {
		t.Errorf("Validate() = %v, want an error naming relations.exclude", err)
	}
	if err := (Exposure{}).Validate(); err != nil {
		t.Errorf("zero Exposure: Validate() = %v", err)
	}
	bad := GenerateSQLOptions{Exposure: Exposure{Relations: RelationPatterns{Include: []string{"doc#"}}}}
	if _, err := GenerateSQLWithOptions(nil, InlineSQLData{}, "", bad); err == nil {
		t.Error("GenerateSQLWithOptions accepted an invalid pattern")
	}
	if _, err := GenerateListSQLWithOptions(nil, InlineSQLData{}, "", bad); err == nil {
		t.Error("GenerateListSQLWithOptions accepted an invalid pattern")
	}
}

func TestExposure_UnmatchedPatterns(t *testing.T) {
	m := mustExposureModel(t, exposureDocModel)
	e := Exposure{Relations: RelationPatterns{
		Include: []string{"doc#viewer", "folder#*", "own*"},
		Exclude: []string{"secret", "nothing#here"},
	}}
	got := e.UnmatchedPatterns(m.analyses)
	if want := []string{"folder#*", "nothing#here"}; !slices.Equal(got, want) {
		t.Errorf("UnmatchedPatterns = %v, want %v", got, want)
	}
}

const exposureDocModel = `
model
  schema 1.1
type user
type doc
  relations
    define owner: [user]
    define viewer: [user] or owner
    define secret: [user]
`

// TestExposure_PublicRaise checks the rendered dispatchers under a
// restricted exposure: the public ones raise M2003 for relations the model
// defines but does not expose and keep the unknown-pair result; the internal
// ones route every installed relation, including unexposed ones.
func TestExposure_PublicRaise(t *testing.T) {
	m := mustExposureModel(t, exposureDocModel)
	o := generateExposed(t, m, Exposure{Relations: RelationPatterns{Include: []string{"doc#viewer"}}})
	helper := `"authz"."check_permission_unexposed"`

	// owner is reached through viewer, secret is not.
	names := o.created()
	for n, want := range map[string]bool{
		"check_doc_viewer": true, "check_doc_owner": true, "check_doc_secret": false,
		"list_doc_owner_obj": true, "list_doc_secret_sub": false, UnexposedFunctionName: true,
	} {
		if names[n] != want {
			t.Errorf("%s generated = %v, want %v", n, names[n], want)
		}
	}

	expectSQL(t, "raising helper", o.gen.Dispatcher,
		`RAISE EXCEPTION 'relation "%" on type "%" is not exposed', p_relation, p_object_type USING ERRCODE = 'M2003';`)
	// check_permission stays an inlinable LANGUAGE sql wrapper with a guard.
	public := o.gen.Dispatcher[strings.Index(o.gen.Dispatcher, `FUNCTION "authz"."check_permission"(`):]
	expectSQL(t, "check_permission", public,
		"WHEN p_object_type = 'doc' THEN p_relation IN ('owner', 'secret')",
		"ELSE FALSE",
		"THEN "+helper+"(p_object_type, p_relation)",
		`ELSE "authz"."check_permission_internal"(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[])`,
		"LANGUAGE sql STABLE",
	)
	rejectSQL(t, "check_permission", public, "'viewer'")
	internal := o.gen.Dispatcher[strings.Index(o.gen.Dispatcher, `FUNCTION "authz"."check_permission_internal"(`):strings.Index(o.gen.Dispatcher, `FUNCTION "authz"."check_permission"(`)]
	expectSQL(t, "check_permission_internal", internal, `"authz"."check_doc_owner"(`, `"authz"."check_doc_viewer"(`, "RETURN 0;")
	rejectSQL(t, "check_permission_internal", internal, "check_doc_secret", "M2003", helper)
	expectSQL(t, "check_permission_nw", o.gen.DispatcherNoWildcard, "p_relation IN ('owner', 'secret')", helper)

	expectSQL(t, "check_permission_bulk", o.gen.BulkDispatcher,
		"PERFORM "+helper+"(u.object_type, u.relation)",
		"UNNEST(p_relations, p_object_types) AS u(relation, object_type)",
		"WHEN u.object_type = 'doc' THEN u.relation IN ('owner', 'secret')",
		"r.relation NOT IN ('viewer')",
	)

	expectSQL(t, "explain_permission", o.gen.ExplainDispatcher, "to_jsonb("+helper+"(p_object_type, p_relation))", "p_relation IN ('owner', 'secret')")
	// expand answers for all of Reach (pointer chasing), so only secret raises.
	expectSQL(t, "expand_permission", o.gen.ExpandDispatcher, "to_jsonb("+helper+"(p_object_type, p_relation))", "p_relation IN ('secret')")

	for name, sql := range map[string]string{
		"list_accessible_objects":  o.list.ListObjectsDispatcher,
		"list_accessible_subjects": o.list.ListSubjectsDispatcher[strings.Index(o.list.ListSubjectsDispatcher, `FUNCTION "authz"."list_accessible_subjects"(`):],
	} {
		expectSQL(t, name, sql,
			"IF p_relation IN ('owner', 'secret') THEN\n            RAISE EXCEPTION",
			// Unknown type or relation: empty, as before.
			"RETURN;\n    END IF;\n    -- Unknown type/relation pair - return empty result",
		)
		rejectSQL(t, name, sql, "_owner_", "_secret_")
	}
	// With list_objects and accessible both on, accessible_objects stays the
	// default LANGUAGE sql wrapper: list_accessible_objects raises for it.
	if got, want := o.list.AccessibleObjectsFunctions[len(o.list.AccessibleObjectsFunctions)-1].SQL,
		generateAccessibleObjectsDispatcher(m.analyses, exposureSchema, nil); got != want {
		t.Errorf("accessible_objects differs from the default wrapper:\n%s", got)
	}
	internalSubjects := o.list.ListSubjectsDispatcher[:strings.Index(o.list.ListSubjectsDispatcher, `FUNCTION "authz"."list_accessible_subjects"(`)]
	expectSQL(t, "list_accessible_subjects_internal", internalSubjects, `"authz"."list_doc_owner_sub"(`, `"authz"."list_doc_viewer_sub"(`)
	rejectSQL(t, "list_accessible_subjects_internal", internalSubjects, "M2003", "secret")

	// The raise lists only relations the model defines.
	for _, sql := range o.sql() {
		if strings.Contains(sql, "'user'") && strings.Contains(sql, "M2003") && strings.Contains(sql, "p_object_type = 'user'") {
			t.Errorf("a guard names type user, which defines no relation")
		}
	}
}

// TestExposure_FamilyToggles checks which functions each family toggle
// removes, including the list_objects/accessible/parent_key coupling.
func TestExposure_FamilyToggles(t *testing.T) {
	m := mustExposureModel(t, parentKeyModel)
	full := generateExposed(t, m, Exposure{}).created()
	has := func(set map[string]bool, prefix, suffix string) int {
		n := 0
		for name := range set {
			if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
				n++
			}
		}
		return n
	}
	parentKeyNames := make(map[string]bool)
	for _, pk := range ParentKeyFunctions(m.analyses) {
		parentKeyNames[pk.Name] = true
	}
	if len(parentKeyNames) == 0 {
		t.Fatal("model has no parent-key functions")
	}
	without := func(set, drop map[string]bool) map[string]bool {
		out := make(map[string]bool)
		for n := range set {
			if !drop[n] {
				out[n] = true
			}
		}
		return out
	}
	matching := func(set map[string]bool, keep func(string) bool) map[string]bool {
		out := make(map[string]bool)
		for n := range set {
			if keep(n) {
				out[n] = true
			}
		}
		return out
	}
	equalSets := func(label string, got, want map[string]bool) {
		t.Helper()
		var missing, extra []string
		for n := range want {
			if !got[n] {
				missing = append(missing, n)
			}
		}
		for n := range got {
			if !want[n] {
				extra = append(extra, n)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing)+len(extra) > 0 {
			t.Errorf("%s: missing %v, extra %v", label, missing, extra)
		}
	}
	off := boolPtr(false)
	gen := func(f FunctionFamilies) map[string]bool {
		return without(generateExposed(t, m, Exposure{Functions: f}).created(), map[string]bool{UnexposedFunctionName: true})
	}

	// parent_key off removes exactly the parent-key functions.
	equalSets("parent_key off", gen(FunctionFamilies{ParentKey: off}), without(full, parentKeyNames))

	// One of list_objects, accessible, parent_key keeps list_obj and
	// accessible_ installed: they select from each other.
	for label, f := range map[string]FunctionFamilies{
		"list_objects only": {Accessible: off, ParentKey: off},
		"accessible only":   {ListObjects: off, ParentKey: off},
		"parent_key only":   {ListObjects: off, Accessible: off},
	} {
		got := gen(f)
		want := full
		if f.ParentKey != nil {
			want = without(full, parentKeyNames)
		}
		equalSets(label, got, want)
	}

	// All three off removes every list_obj, accessible_ and parent-key function.
	noObjects := gen(FunctionFamilies{ListObjects: off, Accessible: off, ParentKey: off})
	equalSets("list_objects, accessible, parent_key off", noObjects, matching(full, func(n string) bool {
		return !(strings.HasSuffix(n, "_obj") || (strings.HasPrefix(n, "accessible_") && n != accessibleObjectsDispatcherName))
	}))

	// list_subjects off removes only list_sub functions.
	equalSets("list_subjects off", gen(FunctionFamilies{ListSubjects: off}), matching(full, func(n string) bool { return !strings.HasSuffix(n, "_sub") }))

	// expand and explain off remove their per-relation functions only.
	equalSets("expand off", gen(FunctionFamilies{Expand: off}), matching(full, func(n string) bool {
		return !strings.HasPrefix(n, "expand_") || strings.HasPrefix(n, "expand_permission")
	}))
	equalSets("explain off", gen(FunctionFamilies{Explain: off}), matching(full, func(n string) bool {
		return !strings.HasPrefix(n, "explain_") || strings.HasPrefix(n, "explain_permission")
	}))

	// Everything off keeps every check function and every dispatcher.
	all := gen(FunctionFamilies{ListObjects: off, ListSubjects: off, Accessible: off, ParentKey: off, Expand: off, Explain: off})
	if has(all, "check_", "") != has(full, "check_", "") {
		t.Errorf("all families off: %d check functions, want %d", has(all, "check_", ""), has(full, "check_", ""))
	}
	for _, d := range DispatcherFunctionNames {
		if !all[d] {
			t.Errorf("all families off: dispatcher %s missing", d)
		}
	}
	for n := range all {
		if !strings.HasPrefix(n, "check_") && !slices.Contains(DispatcherFunctionNames, n) {
			t.Errorf("all families off: unexpected %s", n)
		}
	}

	// A disabled family's public dispatcher raises for every relation.
	// accessible_objects follows its own family, so with only one of
	// list_objects and accessible on it routes to the list functions itself.
	accessibleObjects := func(o exposureOutput) string {
		return o.list.AccessibleObjectsFunctions[len(o.list.AccessibleObjectsFunctions)-1].SQL
	}
	o := generateExposed(t, m, Exposure{Functions: FunctionFamilies{Expand: off, ListObjects: off}})
	for _, a := range m.analyses {
		expectSQL(t, "expand_permission", o.gen.ExpandDispatcher, "'"+a.Relation+"'")
	}
	rejectSQL(t, "list_accessible_objects", o.list.ListObjectsDispatcher, "RETURN QUERY")
	expectSQL(t, "accessible_objects", accessibleObjects(o), `"authz"."list_subject_can_read_obj"(`, "LANGUAGE plpgsql")
	rejectSQL(t, "accessible_objects", accessibleObjects(o), `"authz"."list_accessible_objects"(`, "M2003")

	o = generateExposed(t, m, Exposure{Functions: FunctionFamilies{Accessible: off}})
	expectSQL(t, "list_accessible_objects", o.list.ListObjectsDispatcher, `"authz"."list_subject_can_read_obj"(`)
	expectSQL(t, "accessible_objects", accessibleObjects(o), "M2003", "LANGUAGE plpgsql")
	rejectSQL(t, "accessible_objects", accessibleObjects(o), "RETURN QUERY")

	// With both on or both off, it wraps list_accessible_objects, which
	// raises for the same relations.
	wrapper := generateAccessibleObjectsDispatcher(m.analyses, exposureSchema, nil)
	for _, f := range []FunctionFamilies{{ListObjects: off, Accessible: off}, {Expand: off}} {
		if got := accessibleObjects(generateExposed(t, m, Exposure{Functions: f})); got != wrapper {
			t.Errorf("functions %+v: accessible_objects is not the default wrapper:\n%s", f, got)
		}
	}
}

// TestExposure_ParentKeyNamesStable checks that a restricted exposure never
// adds or renames a parent-key function: names are taken over the whole
// model, so a relation whose own accessible function holds a parent-key
// function's name keeps it from appearing even when that relation is not
// installed.
func TestExposure_ParentKeyNamesStable(t *testing.T) {
	const model = `
model
  schema 1.1
type user
type organization
  relations
    define member: [user]
type subject
  relations
    define org: [organization]
    define can_read: member from org
    define can_read_by_org: [user]
    define can_view: member from org
`
	m := mustExposureModel(t, model)
	full := ParentKeyFunctions(m.analyses)
	if !slices.ContainsFunc(full, func(pk ParentKeyFunction) bool { return pk.Name == "accessible_subject_can_view_by_org" }) ||
		slices.ContainsFunc(full, func(pk ParentKeyFunction) bool { return pk.Relation == "can_read" }) {
		t.Fatalf("unexpected parent-key functions under the default exposure: %+v", full)
	}

	models := append([]exposureModel{m, mustExposureModel(t, parentKeyModel)}, exposureCorpus(t)...)
	for i, m := range models {
		byName := make(map[string]ParentKeyFunction)
		for _, pk := range ParentKeyFunctions(m.analyses) {
			byName[pk.Name] = pk
		}
		exposures := exposuresFor(m.analyses, int64(i))
		exposures["can_read only"] = Exposure{Relations: RelationPatterns{Include: []string{"subject#can_read"}}}
		for label, e := range exposures {
			got := ParentKeyFunctionsWithOptions(m.analyses, GenerateSQLOptions{Exposure: e})
			for _, pk := range got {
				if want, ok := byName[pk.Name]; !ok || want != pk {
					t.Errorf("%s [%s]: parent-key function %+v is not in the default set", m.name, label, pk)
				}
				if !e.Exposes(pk.ObjectType, pk.Relation) {
					t.Errorf("%s [%s]: parent-key function %s for an unexposed relation", m.name, label, pk.Name)
				}
			}
		}
	}
}

// TestExposure_CollectNamedFunctionsPairing checks that CollectNamedFunctions
// pairs each name with its own SQL under a restricted exposure, with and
// without options, and that a hand-built output uses opts.Exposure.
func TestExposure_CollectNamedFunctionsPairing(t *testing.T) {
	for _, f := range exposureTestdataSchemas(t) {
		types, err := parser.ParseSchema(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := compileExposureTypes(filepath.Base(f), types)
		if err != nil {
			t.Fatal(err)
		}
		for label, e := range exposuresFor(m.analyses, 7) {
			opts := GenerateSQLOptions{Exposure: e}
			o := generateExposed(t, m, e)
			for _, nf := range o.named {
				if got := created(nf.SQL); len(got) != 1 || got[0] != nf.Name {
					t.Errorf("%s [%s]: %s pairs with SQL creating %v", m.name, label, nf.Name, got)
				}
			}
			withOpts := CollectNamedFunctionsWithOptions(o.gen, o.list, m.analyses, opts)
			if !slices.Equal(withOpts, o.named) {
				t.Errorf("%s [%s]: CollectNamedFunctionsWithOptions differs from CollectNamedFunctions", m.name, label)
			}

			// A hand-built copy carries no exposure: opts supplies it.
			handGen := GeneratedSQL{
				Functions: o.gen.Functions, NoWildcardFunctions: o.gen.NoWildcardFunctions, NoWildcardIndex: o.gen.NoWildcardIndex,
				ExplainFunctions: o.gen.ExplainFunctions, ExplainEligible: o.gen.ExplainEligible,
				ExpandFunctions: o.gen.ExpandFunctions, ExpandEligible: o.gen.ExpandEligible,
			}
			handList := ListGeneratedSQL{
				ListObjectsFunctions: o.list.ListObjectsFunctions, ListSubjectsFunctions: o.list.ListSubjectsFunctions,
				AccessibleObjectsFunctions: o.list.AccessibleObjectsFunctions,
			}
			if got := CollectNamedFunctionsWithOptions(handGen, handList, m.analyses, opts); !slices.Equal(got, o.named) {
				t.Errorf("%s [%s]: hand-built output with opts pairs differently", m.name, label)
			}
		}
	}
}

func TestExposureNormalized(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name        string
		e           Exposure
		wantDefault bool
	}{
		{"zero value", Exposure{}, true},
		{"include star", Exposure{Relations: RelationPatterns{Include: []string{"*"}}}, true},
		{"include star star", Exposure{Relations: RelationPatterns{Include: []string{"viewer", "**#*"}}}, true},
		{"families explicitly on", Exposure{Functions: FunctionFamilies{ListObjects: &on, Explain: &on}}, true},
		{"include one", Exposure{Relations: RelationPatterns{Include: []string{"viewer"}}}, false},
		{"exclude one", Exposure{Relations: RelationPatterns{Exclude: []string{"*"}}}, false},
		{"family off", Exposure{Functions: FunctionFamilies{Expand: &off}}, false},
		{"star inside a part", Exposure{Relations: RelationPatterns{Include: []string{"*er"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.e.IsDefault(); got != tc.wantDefault {
				t.Errorf("IsDefault() = %v, want %v", got, tc.wantDefault)
			}
		})
	}

	n := Exposure{
		Relations: RelationPatterns{Include: []string{"viewer", "doc#owner", "viewer"}, Exclude: []string{"b", "a"}},
		Functions: FunctionFamilies{ListSubjects: &on, Expand: &off},
	}.Normalized()
	if !slices.Equal(n.Relations.Include, []string{"doc#owner", "viewer"}) || !slices.Equal(n.Relations.Exclude, []string{"a", "b"}) {
		t.Errorf("patterns not sorted and deduplicated: %+v", n.Relations)
	}
	if n.Functions.ListSubjects != nil || n.Functions.Expand == nil || *n.Functions.Expand {
		t.Errorf("enabled flags should be nil and disabled ones kept: %+v", n.Functions)
	}
}
