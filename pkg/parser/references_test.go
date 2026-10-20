package parser

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
)

// referencesSchema is a valid base model; each case appends one viewer
// definition to it.
const referencesSchema = `model
  schema 1.1

type user

type group
  relations
    define member: [user]

type folder
  relations
    define owner: [user]

type document
  relations
    define parent: [folder, document]
    define owner: [user]
    define owner_alias: owner
    define viewer: `

// The DSL transformer accepts undefined types and relations, and convertModel
// compiles them to branches that never match, so parsing must reject them
// rather than turn a typo into a silent deny.
func TestParseSchemaString_RejectsUndefinedReferences(t *testing.T) {
	tests := []struct {
		name   string
		viewer string
		want   string
	}{
		{"computed relation", "[user] or editr", `document#viewer references "editr", which is not a relation on "document"`},
		{"exclusion subtract", "owner but not blocked", `references "blocked"`},
		{"intersection child", "owner and editor", `references "editor"`},
		{"type restriction", "[usr]", `document#viewer allows [usr] but type "usr" is not defined`},
		{"userset relation", "[group#admin]", `document#viewer allows [group#admin] but "group" has no relation "admin"`},
		{"userset type", "[team#member]", `type "team" is not defined`},
		{"tupleset relation", "owner from folder", "uses `owner from folder`, but \"folder\" is not a relation on \"document\""},
		{"computed relation on no parent type", "editor from parent", `none of the types document#parent can point to (folder, document) has a relation "editor"`},
		{"tupleset without direct types", "owner from owner_alias", "document#owner_alias has no direct type restrictions to follow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSchemaString(referencesSchema + tt.viewer)
			if err == nil {
				t.Fatal("expected undefined reference to be rejected, got nil error")
			}
			if !melange.IsInvalidSchemaErr(err) {
				t.Errorf("expected an ErrInvalidSchema, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestParseSchemaString_AllowsDefinedReferences(t *testing.T) {
	for _, viewer := range []string{
		"[user, user:*, group#member] or owner",
		"owner from parent", // defined on both folder and document
		"owner but not owner from parent",
	} {
		if _, err := ParseSchemaString(referencesSchema + viewer); err != nil {
			t.Errorf("viewer %q should parse: %v", viewer, err)
		}
	}
}

// Deployed models and previous revisions were accepted before references were
// checked, so the recorded-schema parsers must still read them: they are the
// baseline for the migration that fixes the dangling reference.
func TestParseRecorded_SkipsReferenceCheck(t *testing.T) {
	dangling := referencesSchema + "[user] or editr"

	if _, err := ParseRecordedSchemaString(dangling); err != nil {
		t.Errorf("ParseRecordedSchemaString: %v", err)
	}
	modules := map[string]string{"core.fga": "module core\n\ntype user\n\ntype doc\n  relations\n    define viewer: [user] or editr\n"}
	if _, err := ParseRecordedModularSchemaFromStrings(modules, "1.2"); err != nil {
		t.Errorf("ParseRecordedModularSchemaFromStrings: %v", err)
	}
	if _, err := ParseModularSchemaFromStrings(modules, "1.2"); err == nil || !strings.Contains(err.Error(), `"editr"`) {
		t.Errorf("ParseModularSchemaFromStrings should reject the dangling reference, got %v", err)
	}
}
