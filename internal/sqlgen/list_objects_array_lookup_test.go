package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// arrayLookupModel has a self-referential userset walk (team.member) and a
// tuple-to-userset part inside an intersection (doc.can_edit).
const arrayLookupModel = `
model
  schema 1.1
type user
type team
  relations
    define member: [user, team#member]
type org
  relations
    define admin: [user]
    define editor: [user]
type doc
  relations
    define org: [org]
    define owner: [user]
    define can_edit: (owner or admin from org) and editor from org
`

func generateArrayLookupSQL(t *testing.T) ListGeneratedSQL {
	t.Helper()
	types, err := parser.ParseSchemaString(arrayLookupModel)
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

func TestSelfRefUsersetWalkMatchesFrontierAsArray(t *testing.T) {
	out := generateArrayLookupSQL(t)
	var list string
	for _, fn := range out.ListObjectsFunctions {
		if strings.Contains(fn, "list_team_member_obj(") {
			list = fn
		}
	}
	if list == "" {
		t.Fatal("no list function for team.member")
	}
	// The frontier reaches the index on each branch's subject_id expression, and
	// an object ID holding '#' never names another userset.
	assertContains(t, list, "t.subject_id = ANY(ARRAY(SELECT me.object_id || '#member' FROM unnest(v_frontier) AS me(object_id) WHERE position('#' in me.object_id) = 0))")
	assertNotContains(t, list, "split_part(t.subject_id, '#', 1) = me.object_id")
}
