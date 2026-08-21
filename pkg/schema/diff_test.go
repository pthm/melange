package schema_test

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

func mustParse(t *testing.T, dsl string) []schema.TypeDefinition {
	t.Helper()
	types, err := parser.ParseSchemaString(dsl)
	if err != nil {
		t.Fatalf("parsing schema: %v\n%s", err, dsl)
	}
	return types
}

const diffBase = `model
  schema 1.1
type user
type org
type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
    define blocked: [user]
`

// diffFor parses base and a variant and returns the changes from base → variant.
func diffFor(t *testing.T, variant string) schema.SchemaDiff {
	t.Helper()
	return schema.Diff(mustParse(t, diffBase), mustParse(t, variant))
}

// hasChange reports whether any change of the given class has a summary
// containing substr.
func hasChange(d schema.SchemaDiff, class schema.ChangeClass, substr string) bool {
	for _, c := range d.Changes {
		if c.Class == class && strings.Contains(c.Summary, substr) {
			return true
		}
	}
	return false
}

func TestDiff_Identical(t *testing.T) {
	if d := diffFor(t, diffBase); !d.Empty() {
		t.Errorf("identical schemas should not diff, got %+v", d.Changes)
	}
}

func TestDiff_OrderIndependent(t *testing.T) {
	// Reordering subject types is not a change.
	reordered := strings.Replace(diffBase, "define owner: [user]", "define owner: [user, org]", 1)
	widened := strings.Replace(diffBase, "define owner: [user]", "define owner: [org, user]", 1)
	if d := schema.Diff(mustParse(t, reordered), mustParse(t, widened)); !d.Empty() {
		t.Errorf("reordered subject types should be equal, got %+v", d.Changes)
	}
}

func TestDiff_TypeAdded(t *testing.T) {
	d := diffFor(t, diffBase+"\ntype folder\n")
	if !hasChange(d, schema.ClassAdditive, "type folder added") {
		t.Errorf("expected additive type-added, got %+v", d.Changes)
	}
}

func TestDiff_TypeRemoved(t *testing.T) {
	// Drop the org type.
	variant := strings.Replace(diffBase, "type org\n", "", 1)
	d := diffFor(t, variant)
	if !hasChange(d, schema.ClassBreaking, "type org removed") {
		t.Errorf("expected breaking type-removed, got %+v", d.Changes)
	}
}

func TestDiff_RelationAddedAndRemoved(t *testing.T) {
	added := strings.Replace(diffBase,
		"define blocked: [user]",
		"define blocked: [user]\n    define auditor: [user]", 1)
	if d := diffFor(t, added); !hasChange(d, schema.ClassAdditive, "document.auditor added") {
		t.Errorf("expected additive relation-added, got %+v", d.Changes)
	}

	removed := strings.Replace(diffBase, "    define blocked: [user]\n", "", 1)
	if d := diffFor(t, removed); !hasChange(d, schema.ClassBreaking, "document.blocked removed") {
		t.Errorf("expected breaking relation-removed, got %+v", d.Changes)
	}
}

func TestDiff_GrantAddedIsAdditive(t *testing.T) {
	// viewer gains a direct subject type.
	subj := strings.Replace(diffBase, "define viewer: [user] or editor", "define viewer: [user, org] or editor", 1)
	if d := diffFor(t, subj); !hasChange(d, schema.ClassAdditive, "grants [org]") {
		t.Errorf("expected additive grant [org], got %+v", d.Changes)
	}

	// viewer gains a genuinely new implied relation (not already in its closure).
	base := `model
  schema 1.1
type user
type document
  relations
    define editor: [user]
    define reviewer: [user]
    define viewer: [user] or editor
`
	widened := strings.Replace(base, "define viewer: [user] or editor", "define viewer: [user] or editor or reviewer", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, widened)); !hasChange(d, schema.ClassAdditive, "grants reviewer") {
		t.Errorf("expected additive grant reviewer, got %+v", d.Changes)
	}
}

// TestDiff_RedundantImpliedRemovalIsNotBreaking guards the closure case: dropping
// a union member that is still reachable through another relation is not a change.
func TestDiff_RedundantImpliedRemovalIsNotBreaking(t *testing.T) {
	base := `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: editor or owner
`
	// owner still reaches viewer via editor, so removing "or owner" changes nothing.
	trimmed := strings.Replace(base, "define viewer: editor or owner", "define viewer: editor", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, trimmed)); !d.Empty() {
		t.Errorf("removing a redundant implied grant should be a no-op, got %+v", d.Changes)
	}
}

func TestDiff_GrantRemovedIsBreaking(t *testing.T) {
	// viewer loses its implied editor path.
	variant := strings.Replace(diffBase, "define viewer: [user] or editor", "define viewer: [user]", 1)
	d := diffFor(t, variant)
	if !hasChange(d, schema.ClassBreaking, "no longer grants editor") {
		t.Errorf("expected breaking grant-removal, got %+v", d.Changes)
	}
}

func TestDiff_RestrictionAddedIsBreaking(t *testing.T) {
	variant := strings.Replace(diffBase, "define viewer: [user] or editor", "define viewer: ([user] or editor) but not blocked", 1)
	d := diffFor(t, variant)
	if !hasChange(d, schema.ClassBreaking, "excludes blocked") {
		t.Errorf("expected breaking exclusion-added, got %+v", d.Changes)
	}
}

func TestDiff_RestrictionRemovedIsAdditive(t *testing.T) {
	// base has the exclusion, variant drops it → additive.
	withExcl := strings.Replace(diffBase, "define viewer: [user] or editor", "define viewer: ([user] or editor) but not blocked", 1)
	d := schema.Diff(mustParse(t, withExcl), mustParse(t, diffBase))
	if !hasChange(d, schema.ClassAdditive, "no longer excludes blocked") {
		t.Errorf("expected additive exclusion-removed, got %+v", d.Changes)
	}
}

func TestDiff_WildcardWideningIsAdditive(t *testing.T) {
	// Making a direct grant public (`[user]` → `[user:*]`) widens access.
	base := `model
  schema 1.1
type user
type document
  relations
    define viewer: [user]
`
	public := strings.Replace(base, "define viewer: [user]", "define viewer: [user, user:*]", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, public)); d.HasBreaking() {
		t.Errorf("widening [user] to [user:*] should be additive, got %+v", d.Changes)
	}
	// Removing the wildcard narrows access → breaking.
	if d := schema.Diff(mustParse(t, public), mustParse(t, base)); !d.HasBreaking() {
		t.Errorf("removing [user:*] should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_IntersectionAlternativeAddedIsAdditive(t *testing.T) {
	// A relation defined as an intersection gains an OR'd alternative group —
	// this widens access, so it must be additive (regression: was breaking).
	base := `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define editor: [user]
    define auditor: [user]
    define can_x: owner and editor
`
	widened := strings.Replace(base,
		"define can_x: owner and editor",
		"define can_x: (owner and editor) or (auditor and editor)", 1)

	if d := schema.Diff(mustParse(t, base), mustParse(t, widened)); d.HasBreaking() {
		t.Errorf("adding an OR'd intersection alternative should be additive, got %+v", d.Changes)
	}
	// And the reverse narrows access → breaking.
	if d := schema.Diff(mustParse(t, widened), mustParse(t, base)); !d.HasBreaking() {
		t.Errorf("removing an OR'd intersection alternative should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_IntersectionLoosenedIsAdditive(t *testing.T) {
	// Loosening an intersection (`owner and editor` → `owner`) widens access:
	// owner alone now suffices, and nobody who had access loses it.
	base := `model
  schema 1.1
type user
type document
  relations
    define owner: [user]
    define editor: [user]
    define can_x: owner and editor
`
	loosened := strings.Replace(base, "define can_x: owner and editor", "define can_x: owner", 1)

	if d := schema.Diff(mustParse(t, base), mustParse(t, loosened)); d.HasBreaking() {
		t.Errorf("loosening an intersection should be additive, got %+v", d.Changes)
	}
	// Tightening (the reverse) narrows access → breaking.
	if d := schema.Diff(mustParse(t, loosened), mustParse(t, base)); !d.HasBreaking() {
		t.Errorf("tightening an intersection should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_WeakenedExclusionIsAdditive(t *testing.T) {
	// Weakening an exclusion (`but not (a and b)` excludes less than `but not a`)
	// widens access.
	strict := `model
  schema 1.1
type user
type document
  relations
    define a: [user]
    define b: [user]
    define grantee: [user]
    define viewer: grantee but not a
`
	weak := strings.Replace(strict, "define viewer: grantee but not a", "define viewer: grantee but not (a and b)", 1)

	if d := schema.Diff(mustParse(t, strict), mustParse(t, weak)); d.HasBreaking() {
		t.Errorf("weakening an exclusion should be additive, got %+v", d.Changes)
	}
	if d := schema.Diff(mustParse(t, weak), mustParse(t, strict)); !d.HasBreaking() {
		t.Errorf("strengthening an exclusion should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_IntersectingDirectGrantIsBreaking(t *testing.T) {
	// `viewer: [user]` -> `viewer: [user] and editor` requires editor in addition
	// to the direct grant, so someone with only [user] loses access — breaking.
	// (The parser encodes the direct `[user]` as a self-reference in the group.)
	base := `model
  schema 1.1
type user
type document
  relations
    define editor: [user]
    define viewer: [user]
`
	tightened := strings.Replace(base, "define viewer: [user]", "define viewer: [user] and editor", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, tightened)); !d.HasBreaking() {
		t.Errorf("intersecting a direct grant with `and editor` should be breaking, got %+v", d.Changes)
	}
	// Dropping the intersection widens access.
	if d := schema.Diff(mustParse(t, tightened), mustParse(t, base)); d.HasBreaking() {
		t.Errorf("dropping the intersection should be additive, got %+v", d.Changes)
	}
}

func TestDiff_WideningAnExclusionRelationIsBreaking(t *testing.T) {
	// `blocked` is used in `viewer: ... but not blocked`. Widening `blocked`
	// (adding [user]) blocks more subjects, so some viewers lose access.
	base := `model
  schema 1.1
type user
type document
  relations
    define blocked: [user]
    define grantee: [user]
    define viewer: grantee but not blocked
`
	widened := strings.Replace(base, "define blocked: [user]", "define blocked: [user, user:*]", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, widened)); !d.HasBreaking() {
		t.Errorf("widening a relation used as an exclusion should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_WideningAnImplierOfAnExclusionIsBreaking(t *testing.T) {
	// `blocked: owner` and `viewer: grantee but not blocked`. Widening `owner`
	// widens `blocked` (which owner implies), so viewers who become owners lose
	// access — breaking, even though `owner` is not named in the exclusion.
	base := `model
  schema 1.1
type user
type org
type document
  relations
    define owner: [user]
    define blocked: owner
    define grantee: [user]
    define viewer: grantee but not blocked
`
	widened := strings.Replace(base, "define owner: [user]", "define owner: [user, org]", 1)
	if d := schema.Diff(mustParse(t, base), mustParse(t, widened)); !d.HasBreaking() {
		t.Errorf("widening an implier of an excluded relation should be breaking, got %+v", d.Changes)
	}
}

func TestDiff_CountsAndHasBreaking(t *testing.T) {
	// One additive (new type) and one breaking (removed relation).
	variant := strings.Replace(diffBase, "    define blocked: [user]\n", "", 1) + "\ntype folder\n"
	d := diffFor(t, variant)

	additive, breaking := d.Counts()
	if additive != 1 || breaking != 1 {
		t.Errorf("expected 1 additive + 1 breaking, got %d/%d: %+v", additive, breaking, d.Changes)
	}
	if !d.HasBreaking() {
		t.Error("expected HasBreaking to be true")
	}
}

// BreakingSummaries feeds the doctor advisory and status drift block, which show
// only what narrows access.
func TestBreakingSummaries_SelectsOnlyBreaking(t *testing.T) {
	d := schema.SchemaDiff{Changes: []schema.Change{
		{Class: schema.ClassAdditive, Type: "audit_log", Summary: "type audit_log added"},
		{Class: schema.ClassBreaking, Type: "document", Relation: "viewer", Summary: "relation document.viewer removed"},
		{Class: schema.ClassBreaking, Type: "folder", Relation: "editor", Summary: "relation folder.editor removed"},
	}}

	got := d.BreakingSummaries()
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2: %v", len(got), got)
	}
	for _, s := range got {
		if strings.Contains(s, "audit_log") {
			t.Errorf("additive change leaked into breaking summaries: %q", s)
		}
	}
	if len(schema.SchemaDiff{}.BreakingSummaries()) != 0 {
		t.Error("an empty diff must produce no summaries")
	}
}

// A TTU grant renders as "relation from parent" on both sides of the diff, so a
// change to the linking relation reads as a real change rather than a rename.
func TestDiff_TupleToUsersetGrant(t *testing.T) {
	deployed := []schema.TypeDefinition{{Name: "document", Relations: []schema.RelationDefinition{{
		Name:            "viewer",
		ParentRelations: []schema.ParentRelationCheck{{Relation: "viewer", LinkingRelation: "parent"}},
	}}}}
	local := []schema.TypeDefinition{{Name: "document", Relations: []schema.RelationDefinition{{
		Name:            "viewer",
		ParentRelations: []schema.ParentRelationCheck{{Relation: "viewer", LinkingRelation: "owner"}},
	}}}}

	d := schema.Diff(deployed, local)
	additive, breaking := d.Counts()
	if breaking != 1 || additive != 1 {
		t.Fatalf("counts = %d breaking, %d additive; want 1 and 1: %+v", breaking, additive, d.Changes)
	}
	joined := strings.Join(summaries(d), " | ")
	if !strings.Contains(joined, "viewer from parent") || !strings.Contains(joined, "viewer from owner") {
		t.Errorf("summaries should name both TTU tokens, got: %s", joined)
	}
}

// An intersection member carrying its own exclusion is rendered with that
// exclusion, so tightening it is not mistaken for an unrelated grant.
func TestDiff_IntersectionMemberExclusion(t *testing.T) {
	deployed := []schema.TypeDefinition{{Name: "document", Relations: []schema.RelationDefinition{{
		Name:               "viewer",
		IntersectionGroups: []schema.IntersectionGroup{{Relations: []string{"editor", "member"}}},
	}}}}
	local := []schema.TypeDefinition{{Name: "document", Relations: []schema.RelationDefinition{{
		Name: "viewer",
		IntersectionGroups: []schema.IntersectionGroup{{
			Relations:  []string{"editor", "member"},
			Exclusions: map[string][]string{"editor": {"banned"}},
		}},
	}}}}

	d := schema.Diff(deployed, local)
	if d.Empty() {
		t.Fatal("narrowing an intersection member must register as a change")
	}
	joined := strings.Join(summaries(d), " | ")
	if !strings.Contains(joined, "editor but not banned") {
		t.Errorf("summary should render the member exclusion, got: %s", joined)
	}
}

func summaries(d schema.SchemaDiff) []string {
	out := make([]string, 0, len(d.Changes))
	for _, c := range d.Changes {
		out = append(out, c.Summary)
	}
	return out
}
