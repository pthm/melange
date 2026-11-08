package sqlgen

import (
	"regexp"
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// pagedModel lists contacts through their org in the plan shapes that read a
// page of each parent, a recursive plan's cross-type TTU (can_read) and a
// composed plan's TTU composition (can_read_pii, whose target reaches through
// a parent itself), and in one that must not (an exclusion over the set).
const pagedModel = `
model
  schema 1.1
type user
type partner
  relations
    define viewer: [user]
type organization
  relations
    define member: [user]
    define viewer: [user]
    define partner: [partner]
    define reader: viewer and member
    define partner_reader: (viewer and member) or viewer from partner
    define pii_reader: reader or partner_reader
type contact
  relations
    define org: [organization]
    define blocked: [user]
    define can_read: reader from org
    define can_read_pii: pii_reader from org
    define can_read_unblocked: reader from org but not blocked
`

func pagedListFunction(t *testing.T, name string) string {
	t.Helper()
	types, err := parser.ParseSchemaString(pagedModel)
	if err != nil {
		t.Fatal(err)
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := ComputeCanGenerate(AnalyzeRelations(types, closure))
	out, err := GenerateListSQL(analyses, BuildInlineSQLData(closure, analyses), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range out.ListObjectsFunctions {
		if strings.Contains(fn, name+"(") {
			return fn
		}
	}
	t.Fatalf("no list function %s", name)
	return ""
}

// plainStatements returns the plain-subject branch's unlimited and paged
// statements: the last IF p_limit IS NULL of the function.
func plainStatements(t *testing.T, fn string) (all, paged string) {
	t.Helper()
	i := strings.LastIndex(fn, "IF p_limit IS NULL THEN")
	if i < 0 {
		t.Fatalf("no plain statements in:\n%s", fn)
	}
	all, paged, ok := strings.Cut(fn[i:], "ELSE")
	if !ok {
		t.Fatalf("no paged statement in:\n%s", fn)
	}
	return all, paged
}

// The paged statement reads a page of each parent; the unlimited one keeps
// reading every object under every parent.
func TestPagedList_ReadsAPageOfEachParent(t *testing.T) {
	for _, tc := range []struct{ fn, parents, object string }{
		{"list_contact_can_read_obj", "FROM organization_reader_objs AS par,", "child.object_id"},
		{"list_contact_can_read_pii_obj", "FROM list_organization_pii_reader_obj(p_subject_type, p_subject_id, NULL, NULL) AS par,", "t.object_id"},
	} {
		t.Run(tc.fn, func(t *testing.T) {
			all, paged := plainStatements(t, pagedListFunction(t, tc.fn))
			if strings.Contains(all, "LATERAL") {
				t.Errorf("the unlimited statement must not read by parent:\n%s", all)
			}
			for _, want := range []string{
				tc.parents,
				"LATERAL (",
				// A repeated linking tuple must not take a place in a parent's page.
				"SELECT DISTINCT " + tc.object,
				".subject_id = par.object_id",
				tc.object + " > COALESCE(p_after, '')",
			} {
				if !strings.Contains(paged, want) {
					t.Errorf("paged statement is missing %q:\n%s", want, paged)
				}
			}
			// Each parent's lookup stops at a page. The outer page has its own
			// LIMIT p_limit + 1, so match the one closing the LATERAL.
			perParent := regexp.MustCompile(`ORDER BY ` + regexp.QuoteMeta(tc.object) + `\s+LIMIT p_limit \+ 1\s+\) AS x`)
			if !perParent.MatchString(paged) {
				t.Errorf("a parent's lookup must stop at a page:\n%s", paged)
			}
		})
	}
}

// An exclusion filters the accessible set after its arms, so a page of each
// parent could leave out objects the page needs: the arms stay whole.
func TestPagedList_KeepsWholeArmsUnderAFilter(t *testing.T) {
	fn := pagedListFunction(t, "list_contact_can_read_unblocked_obj")
	if strings.Contains(fn, "LATERAL") {
		t.Errorf("a filtered set must not be read a page of each parent:\n%s", fn)
	}
}
