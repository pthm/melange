package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// listSubjectsPlanFor compiles model and returns the list_subjects plan of
// objectType.relation.
func listSubjectsPlanFor(t *testing.T, model, objectType, relation string) ListPlan {
	t.Helper()
	types, err := parser.ParseSchemaString(model)
	if err != nil {
		t.Fatal(err)
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := ComputeCanGenerate(AnalyzeRelations(types, closure))
	inline := BuildInlineSQLData(closure, analyses)
	g := newRelationGraph(buildAnalysisLookup(analyses))
	for _, a := range analyses {
		if a.ObjectType == objectType && a.Relation == relation {
			return BuildListSubjectsPlanWithLookup(a, inline, "", g)
		}
	}
	t.Fatalf("no relation %s.%s", objectType, relation)
	return ListPlan{}
}

const anchorsModel = `
model
  schema 1.1
type user
type group
  relations
    define member: [user]
type organization
  relations
    define member: [user]
    define viewer: [user]
    define everyone: [user, user:*]
    define cable_admin: [user]
    define parent: [parent]
    define reader: (viewer and member) or viewer from parent or cable_admin
type parent
  relations
    define owner: [organization]
    define viewer: [user] and member from owner
    define group_viewer: [user, group#member] and member from owner
    define public_viewer: [user, user:*] and member from owner
type doc
  relations
    define org: [organization]
    define reader: [user] and everyone from org
`

func TestIntersectionAnchors(t *testing.T) {
	for _, tc := range []struct {
		objectType, relation string
		want                 string // the anchors, as anchorLabel renders them
		ok                   bool
	}{
		// The direct tuples anchor the group; the base block lists them.
		{"parent", "viewer", "this", true},
		// A direct userset or wildcard grant can't anchor; the parent relation does.
		{"parent", "group_viewer", "member from owner", true},
		{"parent", "public_viewer", "member from owner", true},
		// The parent-only group anchors through the parent relation.
		{"organization", "reader", "viewer, cable_admin, viewer from parent", true},
		// A parent relation that can return '*' can't anchor; the direct tuples do.
		{"doc", "reader", "this", true},
	} {
		plan := listSubjectsPlanFor(t, anchorsModel, tc.objectType, tc.relation)
		anchors, ok := intersectionAnchors(plan)
		labels := make([]string, len(anchors))
		for i, a := range anchors {
			labels[i] = anchorLabel(a)
		}
		if got := strings.Join(labels, ", "); ok != tc.ok || got != tc.want {
			t.Errorf("%s.%s: anchors = %q, %v; want %q, %v", tc.objectType, tc.relation, got, ok, tc.want, tc.ok)
		}
	}
}

func anchorLabel(a intersectionAnchor) string {
	switch {
	case a.This:
		return "this"
	case a.Parent != nil:
		return a.Parent.Relation + " from " + a.Parent.LinkingRelation
	}
	return a.Relation
}

// Once every group is anchored, the candidates are the base block and the
// anchors only: no subject pool, and no per-part block joining every tuple
// of the parent object, which the check filter could be pushed into.
func TestIntersectionSubjectsAnchoredCandidates(t *testing.T) {
	plan := listSubjectsPlanFor(t, anchorsModel, "organization", "reader")
	blocks := buildListSubjectsIntersectionRegularCandidates(plan)
	var comments []string
	for _, b := range blocks {
		comments = append(comments, b.Comments...)
	}
	got := strings.Join(comments, "\n")
	for _, want := range []string{
		"-- Base: direct tuple lookup",
		"-- Intersection anchor: subjects of viewer",
		"-- Intersection anchor: subjects of viewer on the parent via parent",
		"-- Intersection anchor: subjects of cable_admin",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"Subject pool", "Intersection part"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unexpected %q in:\n%s", unwanted, got)
		}
	}
	var sql string
	for _, b := range blocks {
		if strings.Contains(strings.Join(b.Comments, ""), "on the parent via parent") {
			sql = b.Query.SQL()
		}
	}
	if !strings.Contains(sql, "list_parent_viewer_sub(link.subject_id, p_subject_type)") || !strings.Contains(sql, "LATERAL") {
		t.Errorf("parent anchor should list the parent's subjects per link, got:\n%s", sql)
	}
}
