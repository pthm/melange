package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// subjectKindModel composes relations through accessible functions (a union
// of intersections over a parent link, as phoebe's parent.participant), walks
// a self-referential parent, and names a relation after the subject kind
// argument.
const subjectKindModel = `
model
  schema 1.1
type user
type group
  relations
    define member: [user, group#member]
type organization
  relations
    define member: [user, group#member]
    define p_userset: [user]
type parent
  relations
    define owner: [organization]
    define aa_viewer: [user] and member from owner
    define qa_viewer: [user] and member from owner
    define participant: aa_viewer or qa_viewer or p_userset from owner
type folder
  relations
    define parent: [folder]
    define viewer: [user, group#member] or viewer from parent
`

func generateSubjectKindSQL(t *testing.T) ListGeneratedSQL {
	t.Helper()
	types, err := parser.ParseSchemaString(subjectKindModel)
	if err != nil {
		t.Fatal(err)
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := ComputeCanGenerate(AnalyzeRelations(types, closure))
	out, err := GenerateListSQL(analyses, BuildInlineSQLData(closure, analyses), "")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSubjectKind_EveryTokenIsBound(t *testing.T) {
	out := generateSubjectKindSQL(t)
	all := append([]string{}, out.ListObjectsFunctions...)
	for _, fn := range out.AccessibleObjectsFunctions {
		all = append(all, fn.SQL)
	}
	for _, sql := range all {
		if strings.Contains(sql, subjectKindToken) {
			t.Errorf("unbound subject kind token in:\n%s", sql)
		}
	}
}

func TestSubjectKind_ListFunctionBindsLiterals(t *testing.T) {
	out := generateSubjectKindSQL(t)
	var list string
	for _, fn := range out.ListObjectsFunctions {
		if strings.Contains(fn, "list_parent_participant_obj(") {
			list = fn
		}
	}
	if list == "" {
		t.Fatal("no list function for parent.participant")
	}
	// The plain branch composes with FALSE and the userset branch with TRUE, so
	// generic plans fold the arms for the other kind.
	if !strings.Contains(list, "accessiblesk_parent_aa_viewer(p_subject_type, p_subject_id, FALSE)") {
		t.Errorf("plain branch must compose with FALSE:\n%s", list)
	}
	if !strings.Contains(list, "accessiblesk_parent_participant(p_subject_type, p_subject_id, TRUE)") {
		t.Errorf("userset branch must page the internal function with TRUE:\n%s", list)
	}
	// A relation named after the argument keeps its name, in identifiers and
	// in quoted values.
	if !strings.Contains(list, "list_organization_p_userset_obj(") {
		t.Errorf("composition with relation p_userset must keep its function name:\n%s", list)
	}
	for _, fn := range out.ListObjectsFunctions {
		if strings.Contains(fn, "FUNCTION list_organization_p_userset_obj(") && !strings.Contains(fn, "IN ('p_userset')") {
			t.Errorf("relation p_userset must stay quoted as is:\n%s", fn)
		}
	}
}

func TestSubjectKind_InternalFunctionTestsArgument(t *testing.T) {
	out := generateSubjectKindSQL(t)
	for _, fn := range out.AccessibleObjectsFunctions {
		if fn.Name != accessibleSubjectKindFunctionName("parent", "aa_viewer") {
			continue
		}
		if strings.Contains(fn.SQL, subjectIsUserset) {
			t.Errorf("internal function must test p_userset, not the subject id:\n%s", fn.SQL)
		}
		if !strings.Contains(fn.SQL, "p_userset BOOLEAN") {
			t.Errorf("internal function must take p_userset:\n%s", fn.SQL)
		}
		return
	}
	t.Fatal("no internal accessible function for parent.aa_viewer")
}

func TestSubjectKind_PublicFunctionPassesKindTest(t *testing.T) {
	out := generateSubjectKindSQL(t)
	for _, fn := range out.AccessibleObjectsFunctions {
		if fn.Name != AccessibleObjectsFunctionName("parent", "participant") {
			continue
		}
		want := "accessiblesk_parent_participant(p_subject_type, p_subject_id, " + subjectIsUserset + ")"
		if !strings.Contains(fn.SQL, want) {
			t.Errorf("public function must call the internal form with the kind test:\n%s", fn.SQL)
		}
		return
	}
	t.Fatal("no public accessible function for parent.participant")
}

func TestSubjectKind_AccessibleFunctionsCalleesFirst(t *testing.T) {
	out := generateSubjectKindSQL(t)
	created := map[string]bool{}
	for _, fn := range out.AccessibleObjectsFunctions {
		_, body, _ := strings.Cut(fn.SQL, "AS $$")
		for _, name := range accessibleCallRef.FindAllString(body, -1) {
			if name != fn.Name && !created[name] {
				t.Errorf("%s calls %s before it is created", fn.Name, name)
			}
		}
		created[fn.Name] = true
	}
}
