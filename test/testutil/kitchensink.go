package testutil

import (
	_ "embed"
	"fmt"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// kitchenSinkSchemaFGA is the large comprehensive "kitchen-sink" schema. Unlike
// the small per-feature YAML fixtures and the GitHub scale-benchmark schema, it
// exercises every list strategy and the feature combinations where recent bugs
// lived (self-referential recursive TTU, wildcard-via-TTU, userset-in-closure,
// intersection/exclusion in list_subjects). It backs the generator-coverage
// test, the differential list-vs-check correctness test, and the kitchen-sink
// benchmarks. The full rendered SQL is available via `just dump-kitchensink-sql`.
//
//go:embed testdata/kitchen_sink_schema.fga
var kitchenSinkSchemaFGA string

// AnalyzeKitchenSink runs the analysis pipeline over the kitchen-sink schema and
// returns the per-relation analyses (with ListStrategy and Features populated).
// DB-free; used by the generator-coverage test to assert every strategy and
// feature combination is exercised.
func AnalyzeKitchenSink() ([]compiler.RelationAnalysis, error) {
	types, err := parser.ParseSchemaString(kitchenSinkSchemaFGA)
	if err != nil {
		return nil, fmt.Errorf("parse kitchen-sink schema: %w", err)
	}
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	return analyses, nil
}

// KitchenSinkListSubjectsSQL compiles the kitchen-sink schema and returns the
// generated list_subjects function bodies. DB-free; used for codegen-shape
// assertions over a realistic mix of wildcard and non-wildcard relations.
func KitchenSinkListSubjectsSQL() ([]string, error) {
	types, err := parser.ParseSchemaString(kitchenSinkSchemaFGA)
	if err != nil {
		return nil, fmt.Errorf("parse kitchen-sink schema: %w", err)
	}
	closureRows := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closureRows)
	analyses = compiler.ComputeCanGenerate(analyses)
	inlineData := compiler.BuildInlineSQLData(closureRows, analyses)
	listSQL, err := compiler.GenerateListSQL(analyses, inlineData, "")
	if err != nil {
		return nil, fmt.Errorf("generate list SQL: %w", err)
	}
	return listSQL.ListSubjectsFunctions, nil
}
