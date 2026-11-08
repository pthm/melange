package gogen_test

import (
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pthm/melange/internal/clientgen"
	gogen "github.com/pthm/melange/internal/clientgen/go"
	"github.com/pthm/melange/pkg/compiler"
	fgaparser "github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// testdataSchemas are the schemas the generated code is checked over.
var testdataSchemas = []string{
	"../../../test/testutil/testdata/schema.fga",
	"../../../test/testutil/testdata/saas_schema.fga",
	"../../../test/testutil/testdata/kitchen_sink_schema.fga",
}

func parseSchema(t *testing.T, path string) []schema.TypeDefinition {
	t.Helper()
	types, err := fgaparser.ParseSchema(path)
	if err != nil {
		t.Fatal(err)
	}
	return types
}

func parseSchemaString(t *testing.T, src string) []schema.TypeDefinition {
	t.Helper()
	types, err := fgaparser.ParseSchemaString(src)
	if err != nil {
		t.Fatal(err)
	}
	return types
}

func generate(t *testing.T, types []schema.TypeDefinition, cfg *clientgen.Config) string {
	t.Helper()
	files, err := (&gogen.Generator{}).Generate(types, cfg)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return string(files["schema_gen.go"])
}

// stubImporter type-checks the generated file against minimal copies of the
// packages it imports, so the check needs neither the module cache nor the
// go command.
type stubImporter map[string]string

func (s stubImporter) Import(path string) (*gotypes.Package, error) {
	src, ok := s[path]
	if !ok {
		return importer.Default().Import(path)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path+".go", src, 0)
	if err != nil {
		return nil, err
	}
	return (&gotypes.Config{}).Check(path, fset, []*ast.File{f}, nil)
}

var stubs = stubImporter{
	"github.com/pthm/melange/melange": `package melange
type ObjectType string
type Relation string
type Object struct {
	Type ObjectType
	ID   string
}`,
	"fmt": `package fmt
func Sprint(a ...any) string { return "" }`,
}

// typeCheck parses and type-checks code, returning its package scope.
func typeCheck(t *testing.T, code string) *gotypes.Scope {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "schema_gen.go", code, parser.ParseComments)
	if err != nil {
		t.Fatalf("generated code does not parse: %v\n%s", err, code)
	}
	pkg, err := (&gotypes.Config{Importer: stubs}).Check("authz", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("generated code does not type-check: %v\n%s", err, code)
	}
	return pkg.Scope()
}

// accessibleConstants returns the SQLAccessible* constants code declares,
// by identifier.
func accessibleConstants(t *testing.T, code string) map[string]string {
	t.Helper()
	scope := typeCheck(t, code)
	out := make(map[string]string)
	for _, name := range scope.Names() {
		c, ok := scope.Lookup(name).(*gotypes.Const)
		if !ok || !strings.HasPrefix(name, "SQLAccessible") {
			continue
		}
		v, err := strconv.Unquote(c.Val().ExactString())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = v
	}
	return out
}

func inventory(t *testing.T, types []schema.TypeDefinition, e compiler.Exposure) []compiler.AccessibleFunction {
	t.Helper()
	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
	fns, err := compiler.AccessibleFunctions(analyses, e)
	if err != nil {
		t.Fatal(err)
	}
	return fns
}

// The generated file is gofmt-clean and type-checks, for string and integer
// IDs, and its accessible constants are exactly the inventory's names,
// truncated ones included.
func TestGenerate_CompilesAndNamesInstalledFunctions(t *testing.T) {
	for _, path := range testdataSchemas {
		types := parseSchema(t, path)
		for _, idType := range []string{"string", "int64"} {
			code := generate(t, types, &clientgen.Config{IDType: idType})
			formatted, err := format.Source([]byte(code))
			if err != nil || string(formatted) != code {
				t.Errorf("%s (%s IDs): output is not gofmt-clean: %v", path, idType, err)
			}
			got := accessibleConstants(t, code)
			fns := inventory(t, types, compiler.Exposure{})
			if len(got) != len(fns) {
				t.Errorf("%s: %d accessible constants, want %d", path, len(got), len(fns))
			}
			for _, f := range fns {
				ident := "SQLAccessible" + pascal(f.ObjectType) + pascal(f.Relation)
				if f.Link != "" {
					ident += "By" + pascal(f.Link)
				}
				if got[ident] != f.Name {
					t.Errorf("%s: %s = %q, want %q", path, ident, got[ident], f.Name)
				}
			}
		}
	}

	code := generate(t, parseSchema(t, testdataSchemas[0]), nil)
	for _, want := range []string{
		`SQLAccessiblePullRequestCanReadSecurityAdvisoriesAndVulnerabilityReports = "accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c"`,
		`SQLAccessiblePullRequestCanReadSecurityAdvisoriesAndVulnerabilityReportsByRepo = "accessible_pull_r_can_read_security_advisories_and_vul_e8c3d69b"`,
		`SQLAccessibleIssueCanReadByRepo = "accessible_issue_can_read_by_repo"`,
		"func SQLAccessibleFunction(objectType melange.ObjectType, relation melange.Relation) (string, bool) {",
		"\t\tcase RelCanRead:\n\t\t\treturn SQLAccessibleIssueCanRead, true\n",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("schema.fga output lacks %q", want)
		}
	}
}

func pascal(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

const docModel = `
model
  schema 1.1
type user
type folder
  relations
    define viewer: [user]
type document
  relations
    define parent: [folder]
    define owner: [user]
    define editor: [user] or owner
    define can_view: viewer from parent
    define can_edit: editor
`

// The exposure and the client's relation selector decide the relation
// constants and the accessible names; the prefix filter narrows both.
func TestGenerate_Selection(t *testing.T) {
	defs := parseSchemaString(t, docModel)
	off := false
	canStar := compiler.RelationPatterns{Include: []string{"can_*"}}
	tests := []struct {
		name     string
		cfg      *clientgen.Config
		rels     []string // relation names with constants
		accessed []string // per-relation accessible constants
		byParent []string // parent-key constants
	}{
		{
			name:     "nil config is the default exposure",
			cfg:      nil,
			rels:     []string{"can_edit", "can_view", "editor", "owner", "parent", "viewer"},
			accessed: []string{"DocumentCanEdit", "DocumentCanView", "DocumentEditor", "DocumentOwner", "DocumentParent", "FolderViewer"},
			byParent: []string{"DocumentCanViewByParent"},
		},
		{
			name:     "exposure relations",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Relations: canStar}},
			rels:     []string{"can_edit", "can_view"},
			accessed: []string{"DocumentCanEdit", "DocumentCanView"},
			byParent: []string{"DocumentCanViewByParent"},
		},
		{
			name:     "type-qualified exclude",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Relations: compiler.RelationPatterns{Exclude: []string{"document#parent", "folder#*", "document#owner"}}}},
			rels:     []string{"can_edit", "can_view", "editor"},
			accessed: []string{"DocumentCanEdit", "DocumentCanView", "DocumentEditor"},
			byParent: []string{"DocumentCanViewByParent"},
		},
		{
			name: "client selector narrower than the exposure",
			cfg:  &clientgen.Config{Relations: &compiler.RelationPatterns{Include: []string{"document#can_edit"}}},
			rels: []string{"can_edit"}, accessed: []string{"DocumentCanEdit"},
		},
		{
			// The client may name relations the database does not expose,
			// but only installed functions of exposed relations get names.
			name:     "client selector wider than the exposure",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Relations: canStar}, Relations: &compiler.RelationPatterns{}},
			rels:     []string{"can_edit", "can_view", "editor", "owner", "parent", "viewer"},
			accessed: []string{"DocumentCanEdit", "DocumentCanView"},
			byParent: []string{"DocumentCanViewByParent"},
		},
		{
			name:     "accessible family off",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Functions: compiler.FunctionFamilies{Accessible: &off}}},
			rels:     []string{"can_edit", "can_view", "editor", "owner", "parent", "viewer"},
			byParent: []string{"DocumentCanViewByParent"},
		},
		{
			name:     "parent_key family off",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Relations: canStar, Functions: compiler.FunctionFamilies{ParentKey: &off}}},
			rels:     []string{"can_edit", "can_view"},
			accessed: []string{"DocumentCanEdit", "DocumentCanView"},
		},
		{
			name:     "prefix filter after the selector",
			cfg:      &clientgen.Config{Exposure: &compiler.Exposure{Relations: compiler.RelationPatterns{Include: []string{"can_view", "editor"}}}, RelationFilter: "can_"},
			rels:     []string{"can_view"},
			accessed: []string{"DocumentCanView"},
			byParent: []string{"DocumentCanViewByParent"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code := generate(t, defs, tc.cfg)
			scope := typeCheck(t, code)
			var rels, accessed, byParent []string
			for _, name := range scope.Names() {
				switch {
				case strings.HasPrefix(name, "Rel"):
					v, _ := strconv.Unquote(scope.Lookup(name).(*gotypes.Const).Val().ExactString())
					rels = append(rels, v)
				case strings.HasPrefix(name, "SQLAccessible") && strings.Contains(name, "By"):
					byParent = append(byParent, strings.TrimPrefix(name, "SQLAccessible"))
				case strings.HasPrefix(name, "SQLAccessible") && name != "SQLAccessibleFunction":
					accessed = append(accessed, strings.TrimPrefix(name, "SQLAccessible"))
				}
			}
			slices.Sort(rels)
			for _, c := range []struct {
				what      string
				got, want []string
			}{{"relations", rels, tc.rels}, {"accessible", accessed, tc.accessed}, {"parent-key", byParent, tc.byParent}} {
				if !slices.Equal(c.got, c.want) {
					t.Errorf("%s = %v, want %v", c.what, c.got, c.want)
				}
			}
			// Every type keeps its constant and constructors.
			for _, ident := range []string{"TypeDocument", "TypeFolder", "TypeUser", "Document", "AnyFolder", "SQLAccessibleFunction"} {
				if scope.Lookup(ident) == nil {
					t.Errorf("%s is missing", ident)
				}
			}
		})
	}
}

// Generating twice, or from types in another order, gives the same file.
func TestGenerate_Deterministic(t *testing.T) {
	types := parseSchema(t, testdataSchemas[2])
	want := generate(t, types, nil)
	reversed := slices.Clone(types)
	slices.Reverse(reversed)
	for i := range 3 {
		if got := generate(t, reversed, nil); got != want {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestGenerate_Errors(t *testing.T) {
	tests := []struct {
		name  string
		model string
		cfg   *clientgen.Config
		want  []string
	}{
		{
			name:  "accessible constant and constructor",
			model: "type user\ntype document\n  relations\n    define x: [user]\ntype s_q_l_accessible_document_x\n",
			want:  []string{`the constructor for type "s_q_l_accessible_document_x"`, "document#x", "SQLAccessibleDocumentX"},
		},
		{
			name:  "type constant and constructor",
			model: "type user\ntype type_user\n",
			want:  []string{`the constant for type "user"`, `the constructor for type "type_user"`, "TypeUser"},
		},
		{
			name:  "one SQL name for two relations",
			model: "type user\ntype a_b\n  relations\n    define c: [user]\ntype a\n  relations\n    define b_c: [user]\n",
			want:  []string{"a#b_c", "a_b#c", "accessible_a_b_c"},
		},
		{
			name:  "invalid identifier",
			model: "type user\ntype doc-x\n  relations\n    define viewer: [user]\n",
			want:  []string{`"TypeDoc-x"`, "not a valid identifier"},
		},
		{
			name:  "invalid exposure",
			model: "type user\n",
			cfg:   &clientgen.Config{Exposure: &compiler.Exposure{Relations: compiler.RelationPatterns{Include: []string{"a#b#c"}}}},
			want:  []string{"invalid exposure", "more than one '#'"},
		},
		{
			name:  "invalid client relations",
			model: "type user\n",
			cfg:   &clientgen.Config{Relations: &compiler.RelationPatterns{Exclude: []string{"doc#"}}},
			want:  []string{"invalid client relations", "empty type or relation"},
		},
		{
			name:  "cycle",
			model: "type user\ntype doc\n  relations\n    define a: [user] or b\n    define b: [user] or a\n",
			want:  []string{"cycle"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			types := parseSchemaString(t, "model\n  schema 1.1\n"+tc.model)
			_, err := (&gogen.Generator{}).Generate(types, tc.cfg)
			if err == nil {
				t.Fatal("Generate succeeded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}
