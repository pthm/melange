package openfgatests

import (
	"fmt"
	"os"
	"sort"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/openfga/pkg/testutils"
	"github.com/openfga/openfga/pkg/typesystem"

	"github.com/pthm/melange/lib/sqlgen"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/schema"
)

// TestSentinelReport walks every YAML test schema, runs
// ComputeExplainEligibility + ComputeExpandEligibility against each,
// and prints the (object_type, relation) pairs that route to the
// dispatcher sentinel — i.e. no per-relation explain_*/expand_*
// function is generated and a call returns the no-entry envelope.
//
// Diagnostic only — pass triggers no failure, and the body emits a
// human-readable report. Run with:
//
//	go test -count=1 -v -run TestSentinelReport ./test/openfgatests/
//
// Honours `t.Short()` so `go test -short ./...` skips the schema sweep
// (avoids slowing down CI's smoke pass).
func TestSentinelReport(t *testing.T) {
	if testing.Short() {
		t.Skip("sentinel report is diagnostic-only; skipped in -short")
	}

	tests, err := LoadTests()
	if err != nil {
		t.Fatalf("LoadTests: %v", err)
	}

	type key struct{ ObjectType, Relation string }
	type entry struct {
		Explain bool
		Expand  bool
		Sources map[string]struct{}
	}
	bucket := make(map[key]*entry)

	for _, tc := range tests {
		for _, stage := range tc.Stages {
			if stage.Model == "" {
				continue
			}
			analyses, ok := analyseStageSchema(stage.Model)
			if !ok {
				continue // ABAC / unparseable models — outside this report's scope
			}
			explainEligible := sqlgen.ComputeExplainEligibility(analyses)
			expandEligible := sqlgen.ComputeExpandEligibility(analyses)

			for _, a := range analyses {
				if !a.Capabilities.CheckAllowed {
					continue
				}
				explainSentinel := !explainEligible[a.ObjectType][a.Relation]
				expandSentinel := !expandEligible[a.ObjectType][a.Relation]
				if !(explainSentinel || expandSentinel) {
					continue
				}
				k := key{a.ObjectType, a.Relation}
				e, ok := bucket[k]
				if !ok {
					e = &entry{Sources: make(map[string]struct{})}
					bucket[k] = e
				}
				e.Explain = e.Explain || explainSentinel
				e.Expand = e.Expand || expandSentinel
				e.Sources[tc.Name] = struct{}{}
			}
		}
	}

	type row struct {
		Type, Relation  string
		Explain, Expand bool
		Sources         []string
	}
	rows := make([]row, 0, len(bucket))
	for k, e := range bucket {
		src := make([]string, 0, len(e.Sources))
		for s := range e.Sources {
			src = append(src, s)
		}
		sort.Strings(src)
		rows = append(rows, row{k.ObjectType, k.Relation, e.Explain, e.Expand, src})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Type != rows[j].Type {
			return rows[i].Type < rows[j].Type
		}
		return rows[i].Relation < rows[j].Relation
	})

	var both, explainOnly, expandOnly int
	for _, r := range rows {
		switch {
		case r.Explain && r.Expand:
			both++
		case r.Explain:
			explainOnly++
		case r.Expand:
			expandOnly++
		}
	}

	// Write to stderr and a file so the report is greppable post-run.
	w := os.Stderr
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Sentinel-routed (object_type, relation) pairs across the OpenFGA test suite\n")
	fmt.Fprintf(w, "============================================================================\n")
	fmt.Fprintf(w, "Total distinct pairs:         %d\n", len(rows))
	fmt.Fprintf(w, "  Both Explain + Expand:      %d\n", both)
	fmt.Fprintf(w, "  Explain-only sentinel:      %d\n", explainOnly)
	fmt.Fprintf(w, "  Expand-only sentinel:       %d\n", expandOnly)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "type#relation                                    | explain | expand | first source (n more)")
	fmt.Fprintln(w, "-------------------------------------------------|---------|--------|----------------------------------")
	for _, r := range rows {
		exp, exd := " ", " "
		if r.Explain {
			exp = "✗"
		}
		if r.Expand {
			exd = "✗"
		}
		src := r.Sources[0]
		if len(r.Sources) > 1 {
			src = fmt.Sprintf("%s (+%d more)", src, len(r.Sources)-1)
		}
		fmt.Fprintf(w, "%-48s |    %s    |    %s   | %s\n",
			r.Type+"#"+r.Relation, exp, exd, src)
	}
}

// analyseStageSchema parses an OpenFGA DSL string into RelationAnalyses
// the eligibility maps consume. Returns ok=false on parse failure (ABAC
// schemas, malformed DSL, etc.) — those are deeper compatibility gaps
// outside this report's scope.
func analyseStageSchema(dsl string) ([]sqlgen.RelationAnalysis, bool) {
	defer func() {
		_ = recover() // MustTransform panics; treat as "skip this schema"
	}()
	model := testutils.MustTransformDSLToProtoWithID(dsl)
	if model == nil {
		return nil, false
	}
	types := convertProtoModel(&openfgav1.WriteAuthorizationModelRequest{
		SchemaVersion:   typesystem.SchemaVersion1_1,
		TypeDefinitions: model.GetTypeDefinitions(),
		Conditions:      model.GetConditions(),
	})
	if len(types) == 0 {
		return nil, false
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := compiler.AnalyzeRelations(types, closure)
	analyses = compiler.ComputeCanGenerate(analyses)
	return analyses, true
}
