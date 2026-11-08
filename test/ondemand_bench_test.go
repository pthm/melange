package test

import (
	"context"
	"testing"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
	"github.com/pthm/melange/test/testutil"
)

// BenchmarkKitchenSinkOnDemand measures what explain and expand on demand
// cost over calling installed functions: one document.can_view Explain and
// one Expand per op, through sqlapi.Trace as the CLI and studio call them,
// with the families installed, then on demand (a new connection, BEGIN,
// CREATE the family's functions in pg_temp, call, ROLLBACK; the script is
// compiled once and cached), and the compile a cache miss adds.
func BenchmarkKitchenSinkOnDemand(b *testing.B) {
	for _, scale := range testutil.KitchenSinkScales {
		b.Run(scale.Name, func(b *testing.B) {
			d := setupKitchenSinkBench(b, scale)
			ctx := context.Background()
			migrate := func(on bool) *sqlapi.API {
				b.Helper()
				if _, err := migrator.MigrateWithOptions(ctx, d.db, kitchenSinkSchemaPath, migrator.MigrateOptions{Exposure: traceFamilies(on)}); err != nil {
					b.Fatal(err)
				}
				return sqlapi.Load(ctx, d.db, "public")
			}
			user, doc := melange.Object{Type: "user", ID: d.subject}, melange.Object{Type: "document", ID: d.doc}
			calls := map[string]func(*sqlapi.API){
				"Explain": func(api *sqlapi.API) {
					if err := api.Trace(ctx, d.db, "public", sqlapi.Explain, nil, func(c *melange.Checker) error {
						_, err := c.Explain(ctx, user, melange.Relation("can_view"), doc)
						return err
					}); err != nil {
						b.Fatal(err)
					}
				},
				"Expand": func(api *sqlapi.API) {
					if err := api.Trace(ctx, d.db, "public", sqlapi.Expand, nil, func(c *melange.Checker) error {
						_, err := c.Expand(ctx, doc, melange.Relation("can_view"))
						return err
					}); err != nil {
						b.Fatal(err)
					}
				},
			}
			for _, mode := range []string{"installed", "on_demand"} {
				api := migrate(mode == "installed")
				if api.OnDemand(sqlapi.Explain) != (mode == "on_demand") {
					b.Fatalf("%s: OnDemand = %v", mode, api.OnDemand(sqlapi.Explain))
				}
				for _, name := range []string{"Explain", "Expand"} {
					call := calls[name]
					b.Run(name+"/"+mode, func(b *testing.B) {
						call(api) // warm the plan and script caches
						testutil.BenchLoop(b, d.db, func() { call(api) })
					})
				}
			}

			types, err := parser.ParseSchema(kitchenSinkSchemaPath)
			if err != nil {
				b.Fatal(err)
			}
			for _, name := range []string{"Explain", "Expand"} {
				b.Run(name+"/compile", func(b *testing.B) {
					var functions int
					for b.Loop() {
						closure := schema.ComputeRelationClosure(types)
						analyses := compiler.ComputeCanGenerate(compiler.AnalyzeRelations(types, closure))
						trace, err := compiler.GenerateTraceSQL(analyses, compiler.BuildInlineSQLData(closure, analyses), "public", compiler.TraceSQLOptions{
							Explain: name == "Explain", Expand: name == "Expand", FunctionSchema: "pg_temp",
						})
						if err != nil {
							b.Fatal(err)
						}
						functions = len(trace.ExplainFunctions) + len(trace.ExpandFunctions)
					}
					b.ReportMetric(float64(functions), "functions")
				})
			}
		})
	}
}
