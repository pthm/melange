package sqlgen

import (
	"strings"
	"testing"
)

// directPlan is a minimal Direct-strategy list_objects plan: document.viewer
// assignable to [user], plus a "folder" relation to filter on.
func directPlan() ListPlan {
	return ListPlan{
		ObjectType:          "document",
		Relation:            "viewer",
		FunctionName:        "list_document_viewer_obj",
		DatabaseSchema:      "public",
		RelationList:        []string{"viewer"},
		AllowedSubjectTypes: []string{"user"},
		HasStandaloneAccess: true,
		Analysis: RelationAnalysis{
			ObjectType:          "document",
			Relation:            "viewer",
			SatisfyingRelations: []string{"viewer"},
		},
	}
}

// section returns everything from marker onward. A missing marker means the
// generator changed shape, which should read as a clear failure rather than a
// slice-bounds panic on Index's -1.
func section(t *testing.T, s, marker string) string {
	t.Helper()
	i := strings.Index(s, marker)
	if i < 0 {
		t.Fatalf("marker %q not found in:\n%s", marker, s)
	}
	return s[i:]
}

// between returns the text from the start marker up to the end marker.
func between(t *testing.T, s, start, end string) string {
	t.Helper()
	from := section(t, s, start)
	i := strings.Index(from, end)
	if i < 0 {
		t.Fatalf("marker %q not found after %q in:\n%s", end, start, from)
	}
	return from[:i]
}

func renderDirect(t *testing.T, plan ListPlan) string {
	t.Helper()
	blocks, err := BuildListObjectsBlocks(plan)
	if err != nil {
		t.Fatalf("BuildListObjectsBlocks: %v", err)
	}
	sql, err := RenderListObjectsFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderListObjectsFunction: %v", err)
	}
	return sql
}

// The filter arrives as one TEXT parameter and is split into locals once, the
// way list_subjects splits its 'team#member' userset filter. Three separate
// parameters would have said the same thing with a wider public signature.
func TestObjectFilter_ParsedOnceIntoLocals(t *testing.T) {
	sql := renderDirect(t, directPlan())

	if !strings.Contains(sql, "p_filter TEXT DEFAULT NULL") {
		t.Errorf("missing defaulted p_filter argument:\n%s", sql)
	}
	for _, decl := range []string{filterRelationVar, filterSubjectTypeVar, filterSubjectIDVar} {
		if !strings.Contains(sql, decl+" TEXT :=") {
			t.Errorf("missing DECLARE for %s:\n%s", decl, sql)
		}
	}
	// The parse must happen in DECLARE, not per-row: no split_part over
	// p_filter should survive into the query body.
	body := section(t, sql, "BEGIN")
	if strings.Contains(body, "split_part(p_filter") {
		t.Errorf("p_filter is re-parsed inside the query body:\n%s", body)
	}
}

// A malformed filter must fail loudly. Parsing garbage into empty locals would
// leave the predicate matching nothing, silently returning an empty list for
// what looked like a valid scoped query.
func TestObjectFilter_RejectsMalformed(t *testing.T) {
	sql := renderDirect(t, directPlan())

	if !strings.Contains(sql, "RAISE EXCEPTION 'melange: invalid object filter") {
		t.Errorf("missing malformed-filter guard:\n%s", sql)
	}
	for _, check := range []string{
		"position('@' in p_filter) = 0",               // no relation/subject split
		"position(':' in " + filterSubjectVar + ")",   // subject is not type:id
		"position('#' in " + filterSubjectIDVar + ")", // userset subject
	} {
		if !strings.Contains(sql, check) {
			t.Errorf("guard missing check %q:\n%s", check, sql)
		}
	}
}

// The whole point of the feature: the predicate goes inside each UNION arm, so
// the planner can drive from the filter set. A post-filter over base_results is
// correct but leaves the unfiltered enumeration in place.
func TestObjectFilter_PushedIntoEveryArm(t *testing.T) {
	sql := renderDirect(t, directPlan())

	base := between(t, sql, "WITH base_results", "paged AS")
	arms := strings.Count(base, "SELECT t.object_id") + strings.Count(base, "-- Self-candidate")
	pushed := strings.Count(base, "p_filter IS NULL OR")
	if arms == 0 {
		t.Fatalf("no arms found in base_results:\n%s", base)
	}
	if pushed != arms {
		t.Errorf("filter pushed into %d of %d arms:\n%s", pushed, arms, base)
	}

	// Fully pushed down means no redundant post-filter in the paged CTE.
	paged := section(t, sql, "paged AS")
	if strings.Contains(paged, "p_filter IS NULL OR") {
		t.Errorf("post-filter emitted despite full pushdown:\n%s", paged)
	}
}

// An unfiltered call must plan exactly as it did before the feature existed:
// the only addition is a constant-folded OR against a NULL parameter.
func TestObjectFilter_NoOpWhenNull(t *testing.T) {
	sql := renderDirect(t, directPlan())

	if strings.Count(sql, "p_filter IS NULL OR") == 0 {
		t.Fatal("expected the NULL short-circuit to guard every filter predicate")
	}
	if strings.Contains(sql, "p_filter = ") {
		t.Errorf("filter compared without a NULL guard:\n%s", sql)
	}
}

// Strategies that cannot take the filter inline must still honor it. The
// recursive renderer walks parent chains through its CTE, so seeding it with a
// filtered set would truncate the walk — it post-filters instead.
func TestObjectFilter_RecursiveFallsBackToPostFilter(t *testing.T) {
	plan := directPlan()
	blocks, err := BuildListObjectsBlocks(plan)
	if err != nil {
		t.Fatalf("BuildListObjectsBlocks: %v", err)
	}
	// Render through the same wrapper the recursive path uses.
	query := RenderUnionBlocks(renderTypedQueryBlocks(blocks.Primary))
	withPost := plan.wrapPaginationFiltered(query, false)
	withoutPost := plan.wrapPaginationFiltered(query, true)

	paged := section(t, withPost, "paged AS")
	if !strings.Contains(paged, "p_filter IS NULL OR") {
		t.Errorf("post-filter missing when pushdown unavailable:\n%s", paged)
	}
	if len(withPost) <= len(withoutPost) {
		t.Error("post-filter variant should add a qualifier")
	}
}

// An intersection group is an INTERSECT of parts. Postgres does not push a
// qualifier through a set operation, so a predicate on the group's result would
// only run once every part had been enumerated — the filter has to go on each
// part instead.
func TestObjectFilter_PushedIntoIntersectionParts(t *testing.T) {
	plan := planWithIntersectionGroup(composableLookup())
	blocks, err := BuildListObjectsBlocks(plan)
	if err != nil {
		t.Fatalf("BuildListObjectsBlocks: %v", err)
	}
	if !applyObjectFilter(plan, blocks.Primary) {
		t.Error("intersection blocks should report full filter coverage")
	}

	sql, err := RenderListObjectsFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderListObjectsFunction: %v", err)
	}

	parts := strings.Count(sql, "INTERSECT") + 1
	if got := strings.Count(sql, "p_filter IS NULL OR"); got < parts {
		t.Errorf("filter on %d of %d INTERSECT parts:\n%s", got, parts, sql)
	}
	// The group result itself must not carry a redundant post-filter.
	if strings.Contains(sql, "p_filter IS NULL OR ig.object_id") {
		t.Errorf("filter applied to INTERSECT result instead of its parts:\n%s", sql)
	}
	paged := section(t, sql, "paged AS")
	if strings.Contains(paged, "p_filter IS NULL OR") {
		t.Errorf("post-filter emitted despite full pushdown:\n%s", paged)
	}
}
