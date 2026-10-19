package studio

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/schema"
)

func TestParseWhatIf(t *testing.T) {
	types := []schema.TypeDefinition{
		{Name: "user"},
		{Name: "group", Relations: []schema.RelationDefinition{
			{Name: "member", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
		}},
		{Name: "document", Relations: []schema.RelationDefinition{
			{Name: "viewer", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}, {Type: "user", Wildcard: true}, {Type: "group", Relation: "member"}}},
			{Name: "editor", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}},
			{Name: "can_view", ImpliedBy: []string{"viewer"}},
		}},
	}
	for _, tc := range []struct {
		in, err string
		n       int
	}{
		{in: "user:bob viewer document:1", n: 1},
		{in: "\n  user:bob viewer document:1 \n\n group:eng#member viewer document:1\n", n: 2},
		{in: "user:* viewer document:1", n: 1},
		{in: "user:bob viewer", err: "line 1: expected <subject> <relation> <object>"},
		{in: "bob viewer document:1", err: "line 1: subject: expected <type>:<id>"},
		{in: "user:bob owner document:1", err: "line 1: relation owner not defined on document"},
		{in: "team:x viewer document:1", err: "line 1: type team not found"},
		{in: "group:eng viewer document:1", err: "line 1: subject type group not allowed for viewer on document (accepts user, user:*, group#member)"},
		{in: "user:bob can_view document:1", err: "line 1: subject type user not allowed for can_view on document (accepts no direct subjects)"},
		{in: "group:eng#admin viewer document:1", err: "line 1: relation admin not defined on group"},
		{in: "group:eng#member editor document:1", err: "line 1: subject group#member not allowed for editor on document"},
		{in: "user:* editor document:1", err: "line 1: subject user:* not allowed for editor on document"},
		{in: "user:bob viewer document:1\nx\nuser:bob owner document:1", err: "line 2: expected <subject> <relation> <object>, got \"x\"\nline 3: relation owner"},
	} {
		got, err := parseWhatIf(tc.in, types)
		switch {
		case tc.err != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.err)):
			t.Errorf("parseWhatIf(%q) err = %v, want prefix %q", tc.in, err, tc.err)
		case tc.err == "" && (err != nil || len(got) != tc.n):
			t.Errorf("parseWhatIf(%q) = %d tuples, %v; want %d", tc.in, len(got), err, tc.n)
		}
	}
	// Without a model only the shape is checked.
	if got, err := parseWhatIf("team:x anything document:1", nil); err != nil || len(got) != 1 {
		t.Errorf("no model: got %v, %v", got, err)
	}
}

func TestWhatIfViewSQLQuotes(t *testing.T) {
	sql := whatIfViewSQL(`we"ird`, []melange.ContextualTuple{{
		Subject: melange.Object{Type: "user", ID: "o'brien"}, Relation: "viewer", Object: melange.Object{Type: "document", ID: "1"},
	}})
	for _, want := range []string{`FROM "we""ird".melange_tuples`, `'o''brien'::text`} {
		if !strings.Contains(sql, want) {
			t.Errorf("whatIfViewSQL missing %s:\n%s", want, sql)
		}
	}
}
