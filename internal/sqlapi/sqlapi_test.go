package sqlapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
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

// recorded is the API of a database whose last migration recorded e, the
// test model and the codegen version.
func recorded(t *testing.T, e sqlgen.Exposure, types []schema.TypeDefinition, codegen string) *API {
	t.Helper()
	return FromModel(&migrator.DeployedModel{Exposure: e, Types: types, CodegenVersion: codegen})
}

// Explain and expand with the family off are compiled on demand when the
// record has a model and this codegen version; otherwise they are refused
// with why. Other families are never compiled.
func TestOnDemand_Decision(t *testing.T) {
	this := migrator.CodegenVersion()
	allOff := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: off(), Expand: off(), ListSubjects: off()}}
	for _, tc := range []struct {
		name     string
		api      *API
		f        Family
		onDemand bool
		want     []string // in the Unavailable message; nil when answered
	}{
		{"explain off, recorded model", recorded(t, allOff, testTypes(t), this), Explain, true, nil},
		{"expand off, recorded model", recorded(t, allOff, testTypes(t), this), Expand, true, nil},
		{"version stamped with v", recorded(t, allOff, testTypes(t), "v"+strings.TrimPrefix(this, "v")), Explain, true, nil},
		{"explain on", recorded(t, sqlgen.Exposure{}, testTypes(t), "v0.0.1"), Explain, false, nil},
		{
			"no recorded model", recorded(t, allOff, nil, this), Explain, false,
			[]string{"the explain functions are not installed", "cannot compile them on demand", "recorded no model", "run melange migrate to record it", "sql.functions.explain back on"},
		},
		{
			"other codegen version", recorded(t, allOff, testTypes(t), "v0.0.1"), Expand, false,
			[]string{"the expand functions are not installed", "cannot compile them on demand", "generated by melange v0.0.1", "this is melange " + this, "run melange migrate with this melange", "use melange v0.0.1"},
		},
		{
			"list subjects off", recorded(t, allOff, testTypes(t), this), ListSubjects, false,
			[]string{"the list subjects functions are not installed", "sql.functions.list_subjects off"},
		},
		{"nil API", nil, Explain, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.api.OnDemand(tc.f); got != tc.onDemand {
				t.Errorf("OnDemand = %v, want %v", got, tc.onDemand)
			}
			u := tc.api.Unavailable(tc.f, "document", "viewer")
			if tc.want == nil {
				if u != nil {
					t.Fatalf("not answered: %v", u)
				}
				return
			}
			if u == nil {
				t.Fatal("answered")
			}
			if u.Label() != "not installed" {
				t.Errorf("label %q", u.Label())
			}
			for _, w := range tc.want {
				if !strings.Contains(u.Error(), w) {
					t.Errorf("message %q lacks %q", u.Error(), w)
				}
			}
			// Trace refuses the same way, before touching the database, and
			// FromError gives the refusal the relation.
			err := tc.api.Trace(context.Background(), nil, "public", tc.f, nil, func(*melange.Checker) error {
				t.Fatal("ran")
				return nil
			})
			got := FromError(tc.f, "document", "viewer", err)
			if got == nil || got.Error() != u.Error() {
				t.Errorf("Trace: %v, want %v", err, u)
			}
		})
	}
}

// A recorded model with a name the schema language does not allow, where
// it is defined or wherever it is referenced, is never compiled on demand:
// model_json is not parsed, and the compiled script would carry the name
// into comments and quoted bodies. Names the parser accepts are compiled.
func TestOnDemand_RefusesNamesTheSchemaLanguageDoesNotAllow(t *testing.T) {
	const bad = "viewer\nCOMMIT; DROP TABLE melange_tuples; --"
	allOff := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: off(), Expand: off()}}
	tamper := func(change func(*schema.RelationDefinition)) []schema.TypeDefinition {
		types := testTypes(t)
		for i := range types {
			for j := range types[i].Relations {
				if types[i].Name == "document" && types[i].Relations[j].Name == "viewer" {
					change(&types[i].Relations[j])
				}
			}
		}
		return types
	}
	for _, tc := range []struct {
		name  string
		types []schema.TypeDefinition
	}{
		{"type", append(testTypes(t), schema.TypeDefinition{Name: "x$$y"})},
		{"relation", tamper(func(r *schema.RelationDefinition) { r.Name = bad })},
		{"implied by", tamper(func(r *schema.RelationDefinition) { r.ImpliedBy = []string{"editor", bad} })},
		{"subject type", tamper(func(r *schema.RelationDefinition) { r.SubjectTypeRefs[0].Type = "user'" })},
		{"userset relation", tamper(func(r *schema.RelationDefinition) {
			r.SubjectTypeRefs = append(r.SubjectTypeRefs, schema.SubjectTypeRef{Type: "document", Relation: bad})
		})},
		{"parent", tamper(func(r *schema.RelationDefinition) {
			r.ParentRelations = []schema.ParentRelationCheck{{Relation: "viewer", LinkingRelation: bad}}
		})},
		{"intersection exclusion", tamper(func(r *schema.RelationDefinition) {
			r.IntersectionGroups = []schema.IntersectionGroup{{Relations: []string{"editor"}, Exclusions: map[string][]string{bad: {"owner"}}}}
		})},
		{"excluded intersection", tamper(func(r *schema.RelationDefinition) {
			r.ExcludedIntersectionGroups = []schema.IntersectionGroup{{Relations: []string{"owner", "auditor;"}}}
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := recorded(t, allOff, tc.types, migrator.CodegenVersion())
			for _, f := range []Family{Explain, Expand} {
				if api.OnDemand(f) {
					t.Errorf("%s compiles on demand", f.name)
				}
				u := api.Unavailable(f, "document", "editor")
				if u == nil {
					t.Fatalf("%s answered", f.name)
				}
				if u.Label() != "not installed" {
					t.Errorf("label %q", u.Label())
				}
				for _, w := range []string{"cannot compile them on demand", "a name the schema language does not allow", "run melange migrate", "sql.functions." + f.key + " back on"} {
					if !strings.Contains(u.Error(), w) {
						t.Errorf("message %q lacks %q", u.Error(), w)
					}
				}
				if strings.Contains(u.Error(), "\n") {
					t.Errorf("message prints the name raw: %q", u.Error())
				}
				err := api.Trace(context.Background(), nil, "public", f, nil, func(*melange.Checker) error {
					t.Fatal("ran")
					return nil
				})
				if got := FromError(f, "document", "editor", err); got == nil || got.Error() != u.Error() {
					t.Errorf("Trace: %v, want %v", err, u)
				}
			}
		})
	}

	// Every name form the parser accepts compiles.
	types, err := parser.ParseSchemaString("model\n  schema 1.1\ntype user\ntype a.b/c-d_E9\n  relations\n    define _r-1: [user, a.b/c-d_E9#_r-1]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !recorded(t, allOff, types, migrator.CodegenVersion()).OnDemand(Explain) {
		t.Error("a model the parser accepts is refused")
	}
}

// On demand, the relations explain and expand answer for are those a
// migration with the family on exposes: explain the exposed relations,
// expand their pointer closure. Unexposed relations stay not exposed.
func TestOnDemand_ExposureStillApplies(t *testing.T) {
	e := editorOnly()
	e.Functions = sqlgen.FunctionFamilies{Explain: off(), Expand: off()}
	api := recorded(t, e, testTypes(t), migrator.CodegenVersion())
	for _, tc := range []struct {
		f        Family
		relation string
		answered bool
	}{
		{Explain, "editor", true},
		{Explain, "owner", false},
		{Expand, "editor", true},
		{Expand, "owner", true},
		{Expand, "viewer", false},
		{Check, "viewer", false},
	} {
		u := api.Unavailable(tc.f, "document", tc.relation)
		if (u == nil) != tc.answered {
			t.Errorf("%s document#%s: answered %v, want %v (%v)", tc.f.name, tc.relation, u == nil, tc.answered, u)
		}
		if u != nil && u.Label() != "not exposed" {
			t.Errorf("%s document#%s: label %q", tc.f.name, tc.relation, u.Label())
		}
	}
}

// A failure to create the functions says what on demand needs, with the
// database's message and without its SQLSTATE.
func TestOnDemand_FailureMessage(t *testing.T) {
	cause := &pq.Error{Code: "25006", Message: "cannot execute CREATE FUNCTION in a read-only transaction"}
	u := FromError(Explain, "document", "viewer", fmt.Errorf("explaining: %w", &Unavailable{Family: Explain, kind: onDemandFailed, cause: cause}))
	if u == nil {
		t.Fatal("not recognized")
	}
	msg := u.Error()
	for _, w := range []string{"explain on demand failed", "sql.functions.explain off", "pg_temp", "writable primary", "TEMP privilege", "cannot execute CREATE FUNCTION in a read-only transaction", "connect to the primary", "melange migrate"} {
		if !strings.Contains(msg, w) {
			t.Errorf("message %q lacks %q", msg, w)
		}
	}
	if strings.Contains(msg, "25006") || strings.Contains(msg, "pq:") {
		t.Errorf("message carries the raw database error: %q", msg)
	}
	var got *pq.Error
	if !errors.As(u, &got) || got != cause {
		t.Error("the database error is not wrapped")
	}
}

// The on-demand script forwards check_permission for the runtime's probe,
// creates the family in pg_temp and only that family, and is compiled once
// per model, exposure, schema and family.
func TestOnDemand_Script(t *testing.T) {
	e := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: off(), Expand: off()}}
	api := recorded(t, e, testTypes(t), migrator.CodegenVersion())
	for _, tc := range []struct {
		schema, forwards string
	}{{"authz", `SELECT "authz".check_permission(`}, {"", "SELECT check_permission("}} {
		ddl, err := api.traceDDL(tc.schema, Explain)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ddl, "CREATE OR REPLACE FUNCTION pg_temp.check_permission(") || !strings.Contains(ddl, tc.forwards) {
			t.Errorf("schema %q: script does not start with the forwarder:\n%s", tc.schema, ddl[:min(len(ddl), 400)])
		}
		for _, w := range []string{`"pg_temp"."explain_permission_internal"(`, `"pg_temp"."explain_permission"(`, `"pg_temp"."explain_document_viewer"(`} {
			if !strings.Contains(ddl, w) {
				t.Errorf("schema %q: script lacks %s", tc.schema, w)
			}
		}
		if strings.Contains(ddl, "expand_") {
			t.Errorf("schema %q: the explain script creates expand functions", tc.schema)
		}
		again, err := api.traceDDL(tc.schema, Explain)
		if err != nil || again != ddl {
			t.Errorf("schema %q: a second compile differs", tc.schema)
		}
	}
	// Another model never gets this model's cached script.
	other, err := parser.ParseSchemaString("model\n  schema 1.1\ntype user\ntype folder\n  relations\n    define viewer: [user]\n")
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := recorded(t, e, other, migrator.CodegenVersion()).traceDDL("authz", Explain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "explain_folder_viewer") || strings.Contains(ddl, "explain_document_viewer") {
		t.Errorf("another model's script:\n%s", ddl)
	}
	expand, err := api.traceDDL("authz", Expand)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(expand, `"pg_temp"."expand_permission"(`) || strings.Contains(expand, "explain_") {
		t.Errorf("expand script:\n%s", expand)
	}
}

// A plain migrate does not read the recorded model, so the remedy for one
// with a bad name follows the codegen version that recorded it: --force
// under this one (a plain migrate skips), this melange's plain migrate over
// an older one, and the newer melange over a newer one.
func TestUnavailable_BadModelRemedy(t *testing.T) {
	prev := version.Version
	version.Version = "v0.10.0"
	t.Cleanup(func() { version.Version = prev })
	allOff := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Explain: off()}}
	types := append(testTypes(t), schema.TypeDefinition{Name: "x$$y"})
	for _, tc := range []struct {
		recorded string
		want     string
		without  string
	}{
		{"v0.10.0", "run melange migrate --force to record the model again", ""},
		{"0.10.0", "run melange migrate --force to record the model again", ""},
		{"v0.9.0", "run melange migrate with this melange to record the model again", "--force"},
		{"v0.11.0", "use melange v0.11.0 or later, which migrated the database, and run melange migrate with it", ""},
	} {
		u := recorded(t, allOff, types, tc.recorded).Unavailable(Explain, "document", "viewer")
		if u == nil {
			t.Fatalf("recorded %s: answered", tc.recorded)
		}
		if msg := u.Error(); !strings.Contains(msg, tc.want) || (tc.without != "" && strings.Contains(msg, tc.without)) {
			t.Errorf("recorded %s: message %q, want %q", tc.recorded, msg, tc.want)
		}
	}
}

// A missing public function names --force as the step after a plain
// migrate that reports the schema unchanged, never before it.
func TestFromError_MissingRemedy(t *testing.T) {
	u := FromError(Check, "document", "viewer", &pq.Error{Code: "42883", Message: "function check_permission(text, text, text, text, text) does not exist"})
	if u == nil {
		t.Fatal("not recognized")
	}
	if msg := u.Error(); !strings.Contains(msg, "run melange migrate (melange migrate --force if it reports the schema unchanged)") {
		t.Errorf("message %q", msg)
	}
}
