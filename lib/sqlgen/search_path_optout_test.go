package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/lib/sqlgen/plpgsql"
	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// #8b: dispatchers and public wrappers reference only schema-qualified names,
// so they carry NoSearchPath and must render WITHOUT a SET search_path clause —
// which is what lets LANGUAGE sql wrappers inline and drops a per-call GUC
// cycle. Leaf functions (check_{type}_{rel}, list_*, etc.) reference
// unqualified melange_tuples and MUST keep search_path so pg_temp can shadow it
// for contextual tuples.
func TestSearchPath_NoSearchPathOptOut(t *testing.T) {
	sp := plpgsql.PlpgsqlFunction{Schema: "authz", Name: "f", Returns: "INTEGER", Body: []plpgsql.Stmt{plpgsql.ReturnInt{Value: 0}}}
	if !strings.Contains(sp.SQL(), "SET search_path = 'authz'") {
		t.Errorf("plpgsql without NoSearchPath should emit SET search_path:\n%s", sp.SQL())
	}
	sp.NoSearchPath = true
	if strings.Contains(sp.SQL(), "SET search_path") {
		t.Errorf("plpgsql with NoSearchPath should omit SET search_path:\n%s", sp.SQL())
	}

	sq := plpgsql.SqlFunction{Schema: "authz", Name: "f", Returns: "INTEGER", Body: sqldsl.Raw("SELECT 0")}
	if !strings.Contains(sq.SQL(), "SET search_path = 'authz'") {
		t.Errorf("sql func without NoSearchPath should emit SET search_path:\n%s", sq.SQL())
	}
	sq.NoSearchPath = true
	if strings.Contains(sq.SQL(), "SET search_path") {
		t.Errorf("sql func with NoSearchPath should omit SET search_path:\n%s", sq.SQL())
	}
}

// End-to-end: dispatchers/wrappers render without search_path; the leaf
// check_{type}_{rel} keeps it. Pins the exact set of functions opted out.
func TestSearchPath_DispatchersOptOut_LeavesKeep(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	analyses[0].DirectSubjectTypes = []string{"user"}

	gen, err := GenerateSQL(analyses, InlineSQLData{}, "authz")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}

	// Dispatchers + wrappers: no search_path.
	for name, sql := range map[string]string{
		"check dispatcher":   gen.Dispatcher,
		"nw dispatcher":      gen.DispatcherNoWildcard,
		"explain dispatcher": gen.ExplainDispatcher,
		"expand dispatcher":  gen.ExpandDispatcher,
	} {
		if strings.Contains(sql, "SET search_path") {
			t.Errorf("%s must NOT contain SET search_path (references only schema-qualified names):\n%s", name, sql)
		}
	}

	// Leaf check function: keeps search_path (references unqualified melange_tuples).
	if len(gen.Functions) == 0 {
		t.Fatal("expected at least one leaf check function")
	}
	for _, fn := range gen.Functions {
		if !strings.Contains(fn, "SET search_path = 'authz'") {
			t.Errorf("leaf check function must keep SET search_path so pg_temp shadows melange_tuples:\n%s", fn)
		}
	}

	// Bulk dispatcher inlines a direct melange_tuples EXISTS, so it MUST keep it.
	if !strings.Contains(gen.BulkDispatcher, "SET search_path = 'authz'") {
		t.Errorf("bulk dispatcher references unqualified melange_tuples and must keep SET search_path:\n%s", gen.BulkDispatcher)
	}

	// Expand leaves reference melange_tuples only schema-qualified and have no
	// contextual-tuple support, so they opt out (dead GUC otherwise).
	if len(gen.ExpandFunctions) == 0 {
		t.Fatal("expected at least one expand leaf function")
	}
	for _, fn := range gen.ExpandFunctions {
		if strings.Contains(fn, "SET search_path") {
			t.Errorf("expand leaf must NOT contain SET search_path (schema-qualified, no pg_temp shadow):\n%s", fn)
		}
	}
}

// #8a: the per-row list_subjects validation calls the internal dispatchers
// directly (empty visited), skipping the public LANGUAGE sql wrappers that
// cannot inline. Pins the DSL call shapes and that the nw routing is preserved.
func TestListSubjects_CallsInternalDirectly(t *testing.T) {
	// Regular (wildcard-allowed) per-row check: check_permission_internal, not
	// the bare check_permission wrapper.
	reg := CheckPermission{
		Schema:      "authz",
		Subject:     SubjectRef{Type: Param("v_filter_type"), ID: Col{Table: "t", Column: "subject_id"}},
		Relation:    "viewer",
		Object:      LiteralObject("document", ObjectID),
		ExpectAllow: true,
	}.SQL()
	if !strings.Contains(reg, "check_permission_internal") {
		t.Errorf("regular list_subjects check must call check_permission_internal:\n%s", reg)
	}
	if !strings.Contains(reg, "ARRAY[]::TEXT[]") {
		t.Errorf("internal call must pass an empty visited array:\n%s", reg)
	}

	// No-wildcard per-row check: check_permission_nw_internal (nw routing
	// preserved), not the regular internal and not the bare nw wrapper.
	nw := sqldsl.NoWildcardPermissionCheckCall("authz", "viewer", "document", Col{Table: "br", Column: "subject_id"}, ObjectID).SQL()
	if !strings.Contains(nw, "check_permission_nw_internal") {
		t.Errorf("no-wildcard list_subjects check must call check_permission_nw_internal:\n%s", nw)
	}
	if !strings.Contains(nw, "ARRAY[]::TEXT[]") {
		t.Errorf("nw internal call must pass an empty visited array:\n%s", nw)
	}
}

// Depth-limit routing is preserved: the internal dispatchers still enforce the
// M2002 depth guard that the public wrappers bypassed by starting empty.
func TestDispatchers_DepthGuardPreserved(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	analyses[0].DirectSubjectTypes = []string{"user"}
	checkSQL, err := generateDispatcher(analyses, "authz", false)
	if err != nil {
		t.Fatalf("generateDispatcher: %v", err)
	}
	if !strings.Contains(checkSQL, "M2002") || !strings.Contains(checkSQL, "array_length") {
		t.Errorf("internal dispatcher must keep the depth guard:\n%s", checkSQL)
	}
}
