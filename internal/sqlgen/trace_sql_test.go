package sqlgen

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
)

// traceOn returns e with the explain and expand families switched on: the
// migration GenerateTraceSQL reproduces.
func traceOn(e Exposure) Exposure {
	e.Functions.Explain, e.Functions.Expand = nil, nil
	return e
}

// traceFamiliesOff returns e with the explain and expand families off: the
// database on-demand generation runs against.
func traceFamiliesOff(e Exposure) Exposure {
	e.Functions.Explain, e.Functions.Expand = boolPtr(false), boolPtr(false)
	return e
}

func generateTrace(t *testing.T, m exposureModel, databaseSchema, functionSchema string, e Exposure) TraceSQL {
	t.Helper()
	trace, err := GenerateTraceSQL(m.analyses, m.inline, databaseSchema, TraceSQLOptions{
		Explain: true, Expand: true, FunctionSchema: functionSchema, Exposure: e,
	})
	if err != nil {
		t.Fatalf("%s: GenerateTraceSQL: %v", m.name, err)
	}
	return trace
}

// migrateTrace is the explain and expand SQL migrate renders under e.
func migrateTrace(t *testing.T, m exposureModel, databaseSchema string, e Exposure) TraceSQL {
	t.Helper()
	gen, err := GenerateSQLWithOptions(m.analyses, m.inline, databaseSchema, GenerateSQLOptions{Exposure: e})
	if err != nil {
		t.Fatalf("%s: GenerateSQLWithOptions: %v", m.name, err)
	}
	return TraceSQL{
		ExplainFunctions: gen.ExplainFunctions, ExplainDispatcher: gen.ExplainDispatcher,
		ExpandFunctions: gen.ExpandFunctions, ExpandDispatcher: gen.ExpandDispatcher,
	}
}

// pgTempName matches a pg_temp-qualified name, as the definitions and the
// calls between generated trace functions render it.
var pgTempName = regexp.MustCompile(`"pg_temp"\."([^"]+)"`)

// requalify replaces the pg_temp qualification with the data schema's: the
// quoted schema, or the bare name when it is empty, as migrate renders it.
func requalify(sql, databaseSchema string) string {
	if databaseSchema == "" {
		return pgTempName.ReplaceAllString(sql, "$1")
	}
	return pgTempName.ReplaceAllString(sql, `"`+databaseSchema+`"."$1"`)
}

// requalifyTrace applies requalify to every statement of trace.
func requalifyTrace(trace TraceSQL, databaseSchema string) TraceSQL {
	each := func(stmts []string) []string {
		out := make([]string, 0, len(stmts))
		for _, s := range stmts {
			out = append(out, requalify(s, databaseSchema))
		}
		return out
	}
	return TraceSQL{
		ExplainFunctions:  each(trace.ExplainFunctions),
		ExplainDispatcher: requalify(trace.ExplainDispatcher, databaseSchema),
		ExpandFunctions:   each(trace.ExpandFunctions),
		ExpandDispatcher:  requalify(trace.ExpandDispatcher, databaseSchema),
	}
}

func assertSameTrace(t *testing.T, label string, want, got TraceSQL) {
	t.Helper()
	w, g := want.Statements(), got.Statements()
	if len(w) != len(g) {
		t.Errorf("%s: %d statements, migrate renders %d", label, len(g), len(w))
		return
	}
	for i := range w {
		if w[i] != g[i] {
			t.Errorf("%s: statement %d differs from migrate's: %s", label, i, firstDiff(w[i], g[i]))
			return
		}
	}
}

// The on-demand set is what migrate installs under the recorded exposure with
// the families on, byte for byte, apart from the function schema: with the
// function schema set to the data schema it is migrate's output, and with
// pg_temp it is migrate's output once the pg_temp qualification is replaced
// by the data schema's, for a named and an empty data schema.
func TestTraceSQL_MatchesMigrate(t *testing.T) {
	models := exposureCorpus(t)
	for i, m := range models {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			for label, e := range exposuresFor(m.analyses, int64(i)) {
				for _, schema := range []string{exposureSchema, ""} {
					label := label + ", schema " + schema
					want := migrateTrace(t, m, schema, traceOn(e))
					assertSameTrace(t, label+", function schema = data schema", want, generateTrace(t, m, schema, schema, e))
					assertSameTrace(t, label+", function schema unset", want, generateTrace(t, m, schema, "", e))
					onDemand := generateTrace(t, m, schema, "pg_temp", traceFamiliesOff(e))
					assertSameTrace(t, label+", pg_temp", want, requalifyTrace(onDemand, schema))
				}
			}
		})
	}
}

// createdPgTemp matches the definition of a function created in pg_temp.
var createdPgTemp = regexp.MustCompile(`CREATE OR REPLACE FUNCTION "pg_temp"\."([^"]+)"\(`)

var createAny = regexp.MustCompile(`CREATE OR REPLACE FUNCTION (\S+)\(`)

var explainName = regexp.MustCompile(`CREATE OR REPLACE FUNCTION "pg_temp"\."(explain_[^"]+)"`)

// unqualifiedTrace matches an explain_ or expand_ call with no schema, which
// migrate resolves through the explain functions' search_path but which, on
// demand, never reaches pg_temp: PostgreSQL does not look functions up there.
var unqualifiedTrace = regexp.MustCompile(`(?:^|[^\w".])"?((?:explain|expand)_\w+)"?\s*\(`)

func assertTraceCallsQualified(t *testing.T, sql string) {
	t.Helper()
	for _, c := range unqualifiedTrace.FindAllStringSubmatch(withoutComments(sql), -1) {
		t.Errorf("calls %s without the pg_temp qualification", c[1])
	}
}

// In pg_temp, every trace function is created there and calls the others
// there; explain calls only the check functions and dispatchers a database
// migrated with the families off has, in the data schema; explain bodies keep
// the data schema's search_path, so melange_tuples resolves; and expand reads
// the data schema's melange_tuples.
func TestTraceSQL_FunctionSchema(t *testing.T) {
	for _, f := range exposureTestdataSchemas(t) {
		types, err := parser.ParseSchema(f)
		if err != nil {
			t.Fatal(err)
		}
		m, err := compileExposureTypes(filepath.Base(f), types)
		if err != nil {
			t.Fatal(err)
		}
		for label, e := range exposuresFor(m.analyses, 1) {
			t.Run(m.name+"/"+label, func(t *testing.T) {
				recorded := traceFamiliesOff(e)
				installed := generateExposed(t, m, recorded).created()
				trace := generateTrace(t, m, exposureSchema, "pg_temp", recorded)
				var created []string
				for _, sql := range trace.Statements() {
					for _, c := range createAny.FindAllStringSubmatch(sql, -1) {
						if !strings.HasPrefix(c[1], `"pg_temp".`) {
							t.Errorf("created outside pg_temp: %s", c[1])
						}
					}
					for _, c := range createdPgTemp.FindAllStringSubmatch(sql, -1) {
						created = append(created, c[1])
					}
				}
				if len(trace.ExplainFunctions) == 0 && len(trace.ExpandFunctions) == 0 {
					t.Fatal("no per-relation functions generated")
				}
				for _, sql := range trace.Statements() {
					assertTraceCallsQualified(t, sql)
					body := withoutComments(sql)
					for _, c := range pgTempName.FindAllStringSubmatch(body, -1) {
						if !slices.Contains(created, c[1]) {
							t.Errorf("calls pg_temp.%s, which is not generated", c[1])
						}
						if !strings.HasPrefix(c[1], "explain_") && !strings.HasPrefix(c[1], "expand_") {
							t.Errorf("%s is qualified with pg_temp", c[1])
						}
					}
					for _, c := range calls(body) {
						if strings.HasPrefix(c.name, "explain_") || strings.HasPrefix(c.name, "expand_") {
							t.Errorf("calls the data schema's %s instead of the pg_temp one", c.name)
						}
						if !installed[c.name] {
							t.Errorf("calls %s.%s, which the database does not have", exposureSchema, c.name)
						}
					}
					if strings.Contains(body, "search_path = 'pg_temp'") {
						t.Errorf("a function sets search_path to pg_temp:\n%s", sql)
					}
				}
				for _, fn := range trace.ExplainFunctions {
					if !strings.Contains(fn, "\nSET search_path = 'authz'\nSET plan_cache_mode") {
						t.Errorf("explain function without the data schema's search_path:\n%s", fn)
					}
				}
				for _, fn := range trace.ExpandFunctions {
					if strings.Contains(fn, "melange_tuples") && !strings.Contains(fn, `"authz"."melange_tuples"`) {
						t.Errorf("expand function reads melange_tuples outside the data schema:\n%s", fn)
					}
					if strings.Contains(fn, "SET search_path") {
						t.Errorf("expand function sets search_path:\n%s", fn)
					}
				}
				for _, d := range []string{trace.ExplainDispatcher, trace.ExpandDispatcher} {
					if strings.Contains(d, "SET search_path") {
						t.Errorf("dispatcher sets search_path:\n%s", d)
					}
				}
			})
		}
	}
}

// With an empty data schema, explain functions have no SET search_path (they
// resolve melange_tuples and the check functions through the caller's, as
// migrate renders them) but keep the planner settings, and nothing outside
// the trace functions is qualified with pg_temp.
func TestTraceSQL_EmptyDataSchema(t *testing.T) {
	m := mustExposureModel(t, exposureDocModel)
	trace := generateTrace(t, m, "", "pg_temp", Exposure{Relations: RelationPatterns{Include: []string{"doc#viewer"}}})
	if len(trace.ExplainFunctions) == 0 {
		t.Fatal("no explain functions")
	}
	for _, fn := range trace.ExplainFunctions {
		if strings.Contains(fn, "SET search_path") || !strings.Contains(fn, "SET plan_cache_mode") {
			t.Errorf("explain function settings:\n%s", fn)
		}
		if !explainName.MatchString(fn) {
			t.Errorf("explain function not created in pg_temp:\n%s", fn)
		}
	}
	all := strings.Join(trace.Statements(), "\n")
	assertTraceCallsQualified(t, all)
	for _, c := range pgTempName.FindAllStringSubmatch(all, -1) {
		if !strings.HasPrefix(c[1], "explain_") && !strings.HasPrefix(c[1], "expand_") {
			t.Errorf("%s is qualified with pg_temp", c[1])
		}
	}
	if !strings.Contains(all, "check_permission_unexposed(") {
		t.Error("the restricted exposure's public wrappers do not call the raising helper")
	}
}

// The options select the families, and an invalid exposure is rejected.
func TestTraceSQL_Families(t *testing.T) {
	m := mustExposureModel(t, exposureDocModel)
	for _, tc := range []struct {
		explain, expand bool
	}{{true, false}, {false, true}, {false, false}} {
		trace, err := GenerateTraceSQL(m.analyses, m.inline, exposureSchema, TraceSQLOptions{Explain: tc.explain, Expand: tc.expand, FunctionSchema: "pg_temp"})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(trace.ExplainFunctions) > 0 && trace.ExplainDispatcher != ""; got != tc.explain {
			t.Errorf("explain=%v: explain generated %v", tc.explain, got)
		}
		if got := len(trace.ExpandFunctions) > 0 && trace.ExpandDispatcher != ""; got != tc.expand {
			t.Errorf("expand=%v: expand generated %v", tc.expand, got)
		}
		if !tc.explain && !tc.expand && len(trace.Statements()) != 0 {
			t.Errorf("statements with no family selected: %d", len(trace.Statements()))
		}
	}
	_, err := GenerateTraceSQL(m.analyses, m.inline, exposureSchema, TraceSQLOptions{Explain: true, Exposure: Exposure{Relations: RelationPatterns{Include: []string{"a#b#c"}}}})
	if err == nil {
		t.Error("an invalid pattern is accepted")
	}
}
