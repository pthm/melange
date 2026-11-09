package studio

import (
	"slices"
	"strings"
	"testing"

	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
)

// An exposed relation's calls: the dispatcher calls, the public accessible
// and parent-key functions in place of accessible_objects, and why a call is
// missing when the family is off or on demand, but nothing for a kind the
// relation can never have.
func TestRelationCalls(t *testing.T) {
	fn := func(kind sqlgen.FunctionKind, public bool, usage, link string) sqlapi.Function {
		return sqlapi.Function{CatalogFunction: sqlgen.CatalogFunction{Name: string(kind) + "_fn", Kind: kind, Public: public, Example: "SELECT " + string(kind), Usage: usage, Link: link, Note: "note"}}
	}
	rel := sqlapi.Relation{
		Type: "deal", Relation: "can_read", Exposed: true,
		Examples: []sqlgen.DispatcherCall{
			{Dispatcher: "check_permission", Example: "SELECT check_permission(...)"},
			{Dispatcher: "accessible_objects", Example: "SELECT accessible_objects(...)", Usage: "WHERE id IN (accessible_objects)"},
			{Dispatcher: "expand_permission", Example: "SELECT expand_permission(...)"},
		},
		Functions: []sqlapi.Function{
			fn(sqlgen.KindCheck, false, "", ""),
			fn(sqlgen.KindAccessible, true, "WHERE id IN (accessible_fn)", ""),
			fn(sqlgen.KindParentKey, true, "WHERE org_id IN (pk)", "org"),
		},
		NotInstalled: []sqlgen.NotInstalled{
			{Kind: sqlgen.KindListObjects, Reason: sqlgen.ReasonFamilyOff, Detail: "sql.functions.list_objects is off"},
			{Kind: sqlgen.KindListSubjects, Reason: sqlgen.ReasonNotEligible, Detail: "never"},
			{Kind: sqlgen.KindExplain, Reason: sqlgen.ReasonOnDemand, Detail: "compiled on demand"},
		},
	}
	got := relationCalls(rel)
	want := []apiCall{
		{Label: "Check", SQL: "SELECT check_permission(...)"},
		{Label: "List objects", Off: "Not installed: sql.functions.list_objects is off."},
		{Label: "Filter a query", SQL: "WHERE id IN (accessible_fn)", Note: accessibleCallNote},
		{Label: "Filter by org", SQL: "WHERE org_id IN (pk)", Note: "note"},
		{Label: "Expand", SQL: "SELECT expand_permission(...)"},
		{Label: "Explain", Off: "Not installed: compiled on demand."},
	}
	if !slices.Equal(got, want) {
		t.Errorf("relationCalls:\n got %+v\nwant %+v", got, want)
	}

	// The calls explain the families off or on demand, so the folded list
	// keeps only the kinds the relation can never have.
	if left := skipCalled(rel.NotInstalled, true); len(left) != 1 || left[0].Kind != sqlgen.KindListSubjects {
		t.Errorf("skipCalled = %+v", left)
	}

	// Without a public accessible function, accessible_objects fills the row.
	rel.Functions = rel.Functions[:1]
	if c := relationCalls(rel); !slices.Contains(c, apiCall{Label: "Filter a query", SQL: "WHERE id IN (accessible_objects)"}) {
		t.Errorf("no accessible_objects fallback: %+v", c)
	}

	// A family that is off while its function is installed for another (the
	// list function backs accessible): the row says why, rather than vanishing.
	public := rel
	public.Examples = []sqlgen.DispatcherCall{{Dispatcher: "check_permission", Example: "SELECT check_permission(...)"}}
	public.Functions = []sqlapi.Function{fn(sqlgen.KindListObjects, false, "", "")}
	public.NotInstalled = nil
	if c := relationCalls(public); len(c) != 2 || c[1].Label != "List objects" || !strings.Contains(c[1].Off, "sql.functions.list_objects is off, so list_accessible_objects does not answer") {
		t.Errorf("installed but not public = %+v", c)
	}

	// An internal relation has only the calls that answer for it.
	rel.Exposed, rel.Examples = false, rel.Examples[2:]
	if c := relationCalls(rel); len(c) != 1 || c[0].Label != "Expand" {
		t.Errorf("internal relation calls = %+v", c)
	}
	if left := skipCalled(rel.NotInstalled, false); len(left) != len(rel.NotInstalled) {
		t.Errorf("internal relation keeps every missing kind: %+v", left)
	}
}

// On the relation page the items are function families, so the line names
// the relation to expose, not "them".
func TestRelationPageExposeReasonNamesTheRelation(t *testing.T) {
	api := apiOf(editorOnlyModel(t))
	us := []*sqlapi.Unavailable{api.Unavailable(sqlapi.Check, "document", "viewer"), api.Unavailable(sqlapi.ListObjects, "document", "viewer")}
	g := groupUnavailable(us, unavailableFamily, "document#viewer")
	if len(g) != 1 || len(g[0].Reasons) != 1 || !strings.Contains(g[0].Reasons[0], "To expose document#viewer, sql.relations.include") {
		t.Errorf("groups = %+v", g)
	}
}

// Unexposed relations a list skips collapse to one line per reason, with
// one shared explanation instead of one per relation.
func TestSkippedRelationsGroupByReason(t *testing.T) {
	api := apiOf(editorOnlyModel(t))
	var us []*sqlapi.Unavailable
	for _, rel := range []string{"owner", "viewer", "auditor"} {
		us = append(us, api.Unavailable(sqlapi.ListObjects, "document", rel))
	}
	groups := groupUnavailable(us, unavailableRelation, "them")
	if len(groups) != 1 || groups[0].Label != "not exposed" || !slices.Equal(groups[0].Items, []string{"owner", "viewer", "auditor"}) || !slices.Equal(groups[0].Reasons, []string{exposeReason("them")}) {
		t.Fatalf("groups = %+v", groups)
	}
	got := strings.Join(strings.Fields(renderHTML(t, skippedRelations(us))), " ")
	if strings.Count(got, "Not listed (not exposed):") != 1 || !strings.Contains(got, "<li><code>owner</code></li><li><code>viewer</code></li><li><code>auditor</code></li>") ||
		!strings.Contains(got, "sql.relations.include") || !strings.Contains(got, "sql.relations.exclude") || strings.Contains(got, "<details") {
		t.Errorf("skipped relations:\n%s", got)
	}

	// A family that is off gives every relation the same reason, without
	// naming any of them.
	model := exposureModel(t, migrator.Exposure{Functions: offFamilies("explain")})
	model.CodegenVersion = "v0.0.1" // another version's functions cannot be explained on demand
	off := apiOf(model)
	us = us[:0]
	for _, rel := range []string{"owner", "viewer"} {
		us = append(us, off.Unavailable(sqlapi.Explain, "document", rel))
	}
	if g := groupUnavailable(us, unavailableRelation, "them"); len(g) != 1 || len(g[0].Reasons) != 1 || strings.Contains(g[0].Reasons[0], "document#") {
		t.Errorf("family-off groups = %+v", g)
	}
}

// An internal relation without functions says whether that is the exposure
// or a missing access path.
func TestInternalNote(t *testing.T) {
	unreached := sqlapi.Relation{NotInstalled: []sqlgen.NotInstalled{{Kind: sqlgen.KindCheck, Reason: sqlgen.ReasonNotExposed}}}
	noPath := sqlapi.Relation{NotInstalled: []sqlgen.NotInstalled{{Kind: sqlgen.KindCheck, Reason: sqlgen.ReasonNoAccessPath}}}
	if got := internalNote(unreached); !strings.Contains(got, "no exposed relation reads it") {
		t.Errorf("unreached: %q", got)
	}
	if got := internalNote(noPath); !strings.Contains(got, "no access path") {
		t.Errorf("no access path: %q", got)
	}
}

func TestFamiliesSayAgrees(t *testing.T) {
	for _, tc := range []struct {
		g    unavailableGroup
		want string
	}{
		{unavailableGroup{Label: "not exposed", Items: []string{"check"}}, "raises M2003"},
		{unavailableGroup{Label: "not exposed", Items: []string{"check", "explain"}}, "raise M2003"},
		{unavailableGroup{Label: "not installed", Items: []string{"explain"}}, "does not answer"},
		{unavailableGroup{Label: "not installed", Items: []string{"explain", "expand"}}, "do not answer"},
	} {
		if got := familiesSay(tc.g); !strings.HasPrefix(got, tc.want) {
			t.Errorf("familiesSay(%v) = %q, want prefix %q", tc.g, got, tc.want)
		}
	}
}

func TestSignatureSQL(t *testing.T) {
	sig := sqlgen.Signature{Args: []sqlgen.Arg{{Name: "p_a", Type: "text"}, {Name: "p_b", Type: "integer", Default: "NULL"}}, Returns: "integer"}
	want := "f(\n    p_a text,\n    p_b integer DEFAULT NULL\n) RETURNS integer"
	if got := signatureSQL("f", sig); got != want {
		t.Errorf("signatureSQL =\n%s\nwant\n%s", got, want)
	}
	if got := signatureSQL("g", sqlgen.Signature{Returns: "void"}); got != "g() RETURNS void" {
		t.Errorf("no args: %q", got)
	}
}

func TestExposureRows(t *testing.T) {
	off := false
	got := exposureRows(sqlgen.Exposure{
		Relations: sqlgen.RelationPatterns{Include: []string{"*#can_*"}, Exclude: []string{"doc#can_x"}},
		Functions: sqlgen.FunctionFamilies{Explain: &off, Expand: &off},
	})
	want := []kvRow{{"Exposes", "*#can_*, except doc#can_x"}, {"Installs", "every family except expand, explain"}}
	if !slices.Equal(got, want) {
		t.Errorf("exposureRows = %+v", got)
	}
	if got := exposureRows(sqlgen.Exposure{}); got[0].Value != "every relation" || got[1].Value != "every family" {
		t.Errorf("default exposure = %+v", got)
	}
}
