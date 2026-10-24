package compiler_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// syntheticSchema builds a model with n resource types chained by parent TTUs,
// each mixing direct, implied, userset, wildcard, TTU, exclusion and
// intersection relations so codegen exercises most strategies at scale.
func syntheticSchema(n int) string {
	var b strings.Builder
	b.WriteString("model\n  schema 1.1\n\ntype user\n\ntype group\n  relations\n    define member: [user, group#member]\n    define viewer: [user] or member\n")
	for i := range n {
		parent := "group"
		if i > 0 {
			parent = fmt.Sprintf("res%d", i-1)
		}
		fmt.Fprintf(&b, `
type res%d
  relations
    define parent: [%s]
    define owner: [user]
    define blocked: [user]
    define editor: [user, group#member] or owner
    define viewer: [user, user:*, group#member] or editor or viewer from parent
    define can_view: viewer but not blocked
    define can_edit: editor and can_view
`, i, parent)
	}
	return b.String()
}

func compileAll(tb testing.TB, src string) {
	types, err := parser.ParseSchemaString(src)
	if err != nil {
		tb.Fatal(err)
	}
	if err := schema.DetectCycles(types); err != nil {
		tb.Fatal(err)
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closure))
	inline := compiler.BuildInlineSQLData(closure, analyses)
	if _, err := compiler.GenerateSQL(analyses, inline, ""); err != nil {
		tb.Fatal(err)
	}
	if _, err := compiler.GenerateListSQL(analyses, inline, ""); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkCompile measures the full DB-free pipeline (parse → SQL) that
// `melange migrate` / `generate migration` run, at increasing model sizes.
func BenchmarkCompile(b *testing.B) {
	cases := map[string]string{}
	for _, n := range []int{1, 10, 50, 200} {
		cases[fmt.Sprintf("types=%d", n)] = syntheticSchema(n)
	}
	if ks, err := os.ReadFile("../../test/testutil/testdata/kitchen_sink_schema.fga"); err == nil {
		cases["kitchen_sink"] = string(ks)
	}
	for name, src := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				compileAll(b, src)
			}
		})
	}
}
