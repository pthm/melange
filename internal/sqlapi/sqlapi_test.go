package sqlapi

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// Exposing document#editor reaches editor and owner, not viewer or auditor.
const testSchema = `model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define editor: [user] or owner
    define viewer: [user] or editor
    define auditor: [user]
`

func testTypes(t *testing.T) []schema.TypeDefinition {
	t.Helper()
	types, err := parser.ParseSchemaString(testSchema)
	if err != nil {
		t.Fatal(err)
	}
	return types
}

func editorOnly() sqlgen.Exposure {
	return sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"document#editor"}}}
}

func off() *bool { return new(bool) }

func TestUnavailable_DefaultAnswersEverything(t *testing.T) {
	for name, api := range map[string]*API{
		"default exposure": New(sqlgen.Exposure{}, testTypes(t)),
		"no model":         New(editorOnly(), nil),
		"zero value":       {},
		"nil":              nil,
	} {
		for _, f := range []Family{Check, ListObjects, ListSubjects, Explain, Expand} {
			for _, rel := range []string{"owner", "editor", "viewer", "auditor"} {
				if u := api.Unavailable(f, "document", rel); u != nil {
					t.Errorf("%s: %s document#%s: %v", name, f.name, rel, u)
				}
			}
		}
	}
}

func TestUnavailable_RestrictedRelations(t *testing.T) {
	api := New(editorOnly(), testTypes(t))
	for _, tc := range []struct {
		f        Family
		relation string
		want     bool // answered
	}{
		{Check, "editor", true},
		{Check, "owner", false},
		{Check, "viewer", false},
		{ListObjects, "editor", true},
		{ListSubjects, "owner", false},
		{Explain, "editor", true},
		{Explain, "owner", false},
		// expand_permission answers for the pointer closure of editor.
		{Expand, "editor", true},
		{Expand, "owner", true},
		{Expand, "viewer", false},
		{Expand, "auditor", false},
		// Relations the model does not define keep the unknown-pair answer.
		{Check, "nonexistent", true},
		{Expand, "nonexistent", true},
	} {
		u := api.Unavailable(tc.f, "document", tc.relation)
		if got := u == nil; got != tc.want {
			t.Errorf("%s document#%s: answered %v, want %v (%v)", tc.f.name, tc.relation, got, tc.want, u)
			continue
		}
		if u == nil {
			continue
		}
		if u.Label() != "not exposed" {
			t.Errorf("label: %q", u.Label())
		}
		msg := u.Error()
		for _, want := range []string{"document#" + tc.relation + " is not exposed", "sql.relations.include", "--include-relation document#" + tc.relation, "the flag replaces the list", "melange migrate"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s document#%s: message %q lacks %q", tc.f.name, tc.relation, msg, want)
			}
		}
	}
}

func TestUnavailable_FamilyOff(t *testing.T) {
	e := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: off(), Expand: off(), ListSubjects: off()}}
	for name, types := range map[string][]schema.TypeDefinition{"model": testTypes(t), "no model": nil} {
		api := New(e, types)
		for _, tc := range []struct {
			f          Family
			key, label string
		}{
			{Explain, "explain", "not installed"},
			{Expand, "expand", "not installed"},
			{ListSubjects, "list_subjects", "not installed"},
		} {
			u := api.Unavailable(tc.f, "document", "viewer")
			if u == nil {
				t.Fatalf("%s: %s is answered with the family off", name, tc.key)
			}
			if u.Label() != tc.label {
				t.Errorf("%s: label %q, want %q", name, u.Label(), tc.label)
			}
			if msg := u.Error(); !strings.Contains(msg, "sql.functions."+tc.key+" off") || !strings.Contains(msg, "does not answer for document#viewer") || !strings.Contains(msg, "melange migrate") {
				t.Errorf("%s: message %q", name, msg)
			}
			// As studio's access tabs ask, with no model and no relation.
			if msg := New(e, nil).Unavailable(tc.f, "", "").Error(); strings.Contains(msg, "#") || strings.Contains(msg, "does not answer") {
				t.Errorf("%s: family-only message names a relation: %q", name, msg)
			}
			// The dispatcher exists with the family off and keeps the
			// unknown-pair answer; without a model the pair cannot be told
			// apart from a known one.
			if u := api.Unavailable(tc.f, "document", "nonexistent"); (u == nil) != (types != nil) {
				t.Errorf("%s: undefined relation with %s off: %v", name, tc.key, u)
			}
			if api.Installed(tc.f) {
				t.Errorf("%s: Installed(%s)", name, tc.key)
			}
		}
		if u := api.Unavailable(Check, "document", "viewer"); u != nil {
			t.Errorf("%s: check is always installed: %v", name, u)
		}
		if u := api.Unavailable(ListObjects, "document", "viewer"); u != nil {
			t.Errorf("%s: list objects is on: %v", name, u)
		}
	}
}

// pgxError has the shape of pgx's *pgconn.PgError.
type pgxError struct{ Code, Message string }

func (e *pgxError) Error() string    { return "ERROR: " + e.Message + " (SQLSTATE " + e.Code + ")" }
func (e *pgxError) SQLState() string { return e.Code }

// A 42883 for the public function in another schema names that schema, so
// the message points at --db-schema, and the database error stays wrapped.
func TestFromError_MissingInSchema(t *testing.T) {
	pqErr := &pq.Error{Code: "42883", Message: "function wrong.expand_permission(unknown, unknown, unknown, unknown, unknown) does not exist"}
	u := FromError(Expand, "document", "viewer", fmt.Errorf("expand_permission: %w", pqErr))
	if u == nil || u.Label() != "not installed" {
		t.Fatalf("got %v, want not installed", u)
	}
	if msg := u.Error(); !strings.Contains(msg, "wrong.expand_permission") || !strings.Contains(msg, "--db-schema") {
		t.Errorf("message %q does not name the schema-qualified function and --db-schema", msg)
	}
	var got *pq.Error
	if !errors.As(u, &got) || got != pqErr {
		t.Error("the database error is not wrapped")
	}
}

func TestFromError(t *testing.T) {
	notExposed := `relation "viewer" on type "document" is not exposed`
	for _, tc := range []struct {
		name  string
		f     Family
		err   error
		label string // "" for not recognized
	}{
		{"lib/pq M2003 wrapped by the published runtime", Explain, fmt.Errorf("explain_permission: %w", &pq.Error{Code: "M2003", Message: notExposed}), "not exposed"},
		{"pgx M2003", Check, &pgxError{Code: "M2003", Message: notExposed}, "not exposed"},
		{"runtime ValidationError", Expand, &melange.ValidationError{Code: melange.ErrorCodeValidation, Message: notExposed}, "not exposed"},
		{"missing expand_permission", Expand, fmt.Errorf("expand_permission: %w", &pq.Error{Code: "42883", Message: "function expand_permission(unknown, unknown, unknown, unknown, unknown) does not exist"}), "not installed"},
		{"ErrMissingFunction", Explain, fmt.Errorf("%w: %v", melange.ErrMissingFunction, &pq.Error{Code: "42883", Message: "function explain_permission(text, text, text, text, text) does not exist"}), "not installed"},
		{"missing check_permission mapped by the published runtime", Check, fmt.Errorf("%w: %v", melange.ErrMissingFunction, &pq.Error{Code: "42883", Message: "function check_permission(text, text, text, text, text) does not exist"}), "not installed"},
		{"pgx missing list_accessible_objects", ListObjects, &pgxError{Code: "42883", Message: "function list_accessible_objects(unknown, unknown, unknown, unknown, unknown, unknown) does not exist"}, "not installed"},
		// The published runtime prefixes every unmapped error with the public
		// function's name, so the name must be read from the database message.
		{"other missing function", Expand, &pq.Error{Code: "42883", Message: "function my_helper(integer) does not exist"}, ""},
		{"missing function the dispatcher calls", Expand, fmt.Errorf("expand_permission: %w", &pq.Error{Code: "42883", Message: "function expand_document_viewer(text, text, text, integer, integer) does not exist"}), ""},
		{"missing internal function mapped by the published runtime", Explain, fmt.Errorf("%w: %v", melange.ErrMissingFunction, &pq.Error{Code: "42883", Message: "function check_permission_internal(text, text, text, text, text, text[]) does not exist"}), ""},
		{"missing operator", Expand, fmt.Errorf("expand_permission: %w", &pq.Error{Code: "42883", Message: "operator does not exist: integer = text"}), ""},
		{"other ValidationError", Check, &melange.ValidationError{Code: melange.ErrorCodeResolutionTooComplex, Message: "resolution too complex"}, ""},
		{"other error", Check, errors.New("connection refused"), ""},
		{"nil", Check, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := FromError(tc.f, "document", "viewer", tc.err)
			if tc.label == "" {
				if u != nil {
					t.Fatalf("recognized: %v", u)
				}
				return
			}
			if u == nil {
				t.Fatal("not recognized")
			}
			if u.Label() != tc.label {
				t.Errorf("label %q, want %q", u.Label(), tc.label)
			}
			msg := u.Error()
			if strings.Contains(msg, "M2003") || strings.Contains(msg, "42883") || strings.Contains(msg, "pq:") {
				t.Errorf("message carries the raw database error: %q", msg)
			}
			if !strings.Contains(msg, "melange migrate") {
				t.Errorf("message %q does not say how to fix it", msg)
			}
		})
	}
}
