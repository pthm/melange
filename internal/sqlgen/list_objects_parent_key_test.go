package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

const parentKeyModel = `
model
  schema 1.1
type user
type organization
  relations
    define member: [user]
    define admin: [user]
    define reader: member or admin
type team
  relations
    define member: [user]
type subject
  relations
    define org: [organization]
    define can_read: reader from org
    define can_admin: admin from org or member from org
    define reader_alias: can_read
    define owner: [user]
    define can_edit: owner or admin from org
    define can_audit: reader from org but not owner
    define member_read: member from org
    define can_view: member_read or admin from org
type document
  relations
    define scope: [organization, team]
    define can_read: member from scope
`

func parentKeyFor(t *testing.T, model string) map[string]parentKeyRelation {
	t.Helper()
	types, err := parser.ParseSchemaString(model)
	if err != nil {
		t.Fatal(err)
	}
	analyses := ComputeCanGenerate(AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
	out := map[string]parentKeyRelation{}
	for _, pk := range parentKeyRelations(analyses) {
		out[pk.ObjectType+"."+pk.Relation] = pk
	}
	return out
}

func TestParentKey_Eligibility(t *testing.T) {
	got := parentKeyFor(t, parentKeyModel)
	for rel, want := range map[string][]string{
		"subject.can_read":     {"reader"},
		"subject.can_admin":    {"admin", "member"},
		"subject.reader_alias": {"reader"},
		// Inherited through the closure: member_read grants through member.
		"subject.can_view": {"admin", "member"},
	} {
		pk, ok := got[rel]
		if !ok {
			t.Errorf("%s: expected a parent-key function", rel)
			continue
		}
		if pk.Link != "org" || pk.ParentType != "organization" || strings.Join(pk.ParentRelations, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %+v, want link org to organization via %v", rel, pk, want)
		}
	}
	for _, rel := range []string{
		"subject.can_edit",  // a direct grant reaches subjects without the link
		"subject.can_audit", // an exclusion removes subjects the org set keeps
		"document.can_read", // the link points at two types
		"organization.reader",
	} {
		if _, ok := got[rel]; ok {
			t.Errorf("%s: must not get a parent-key function", rel)
		}
	}
}

func TestParentKey_FunctionUnionsParentSets(t *testing.T) {
	sql := renderParentKeyFunction("", parentKeyFor(t, parentKeyModel)["subject.can_admin"])
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION accessible_subject_can_admin_by_org(",
		"FROM accessiblesk_organization_admin(p_subject_type, p_subject_id, " + subjectIsUserset + ") AS k",
		"UNION ALL",
		"FROM accessiblesk_organization_member(p_subject_type, p_subject_id, " + subjectIsUserset + ") AS k",
		"$$ LANGUAGE sql STABLE PARALLEL RESTRICTED;",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in:\n%s", want, sql)
		}
	}
	// Inlinable: no SET clause.
	if strings.Contains(sql, "\nSET ") {
		t.Errorf("parent-key function must have no SET clause:\n%s", sql)
	}
}

func TestParentKey_SkipsTakenName(t *testing.T) {
	// subject.can_read_by_org is a relation, so its accessible function is
	// already named accessible_subject_can_read_by_org.
	got := parentKeyFor(t, strings.Replace(parentKeyModel, "    define org: [organization]\n",
		"    define org: [organization]\n    define can_read_by_org: [user]\n", 1))
	if _, ok := got["subject.can_read"]; ok {
		t.Error("subject.can_read: a parent-key name taken by another accessible function must be skipped")
	}
	if _, ok := got["subject.can_admin"]; !ok {
		t.Error("subject.can_admin: an untaken name must still be generated")
	}
}

func TestParentKey_LongLinkKeepsPrefix(t *testing.T) {
	pk := parentKeyRelation{ObjectType: "subject", Relation: "can_read", Link: strings.Repeat("l", 50)}
	name := pk.functionName()
	if len(name) > sqldsl.PostgresMaxIdentifierLength || !strings.HasPrefix(name, "accessible_") {
		t.Errorf("long link must keep the accessible_ prefix within the identifier limit, got %q", name)
	}
	if short := (parentKeyRelation{ObjectType: "subject", Relation: "can_read", Link: "org"}).functionName(); short != "accessible_subject_can_read_by_org" {
		t.Errorf("short name changed: %q", short)
	}
}
