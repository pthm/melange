package migrator

import (
	"bytes"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/pthm/melange/internal/sqlgen"
)

func boolPtr(b bool) *bool { return &b }

// restrictedExposure leaves relations and a function family out.
func restrictedExposure() Exposure {
	return Exposure{
		Relations: sqlgen.RelationPatterns{Include: []string{"owner", "document#viewer"}, Exclude: []string{"*#secret"}},
		Functions: sqlgen.FunctionFamilies{Expand: boolPtr(false)},
	}
}

func TestExposureJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    Exposure
		want string
	}{
		{"zero value is the default", Exposure{}, "{}"},
		{"include everything is the default", Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"*"}}}, "{}"},
		{"qualified include everything is the default", Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"*#*", "viewer"}}}, "{}"},
		{"enabled families are the default", Exposure{Functions: sqlgen.FunctionFamilies{Expand: boolPtr(true)}}, "{}"},
		{
			"restricted is sorted and keeps disabled families",
			restrictedExposure(),
			`{"relations":{"include":["document#viewer","owner"],"exclude":["*#secret"]},"functions":{"expand":false}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exposureJSON(tc.e); got != tc.want {
				t.Errorf("exposureJSON = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSameExposure(t *testing.T) {
	a := restrictedExposure()
	b := Exposure{
		Relations: sqlgen.RelationPatterns{
			Include: []string{"document#viewer", "owner", "owner"},
			Exclude: []string{"*#secret"},
		},
		Functions: sqlgen.FunctionFamilies{Expand: boolPtr(false), Explain: boolPtr(true)},
	}
	if !SameExposure(a, b) {
		t.Error("pattern order, duplicates and explicitly enabled families must not matter")
	}
	if SameExposure(a, Exposure{}) {
		t.Error("a restricted exposure is not the default")
	}
}

// The skip key includes the exposure. A released version is stamped because
// unstamped "dev" builds bypass the phase 1 skip entirely.
func TestShouldSkipMigration_Exposure(t *testing.T) {
	withVersion(t, "v9.9.9")
	checksum := ComputeSchemaChecksum("test schema")
	rec := &MigrationRecord{
		SchemaChecksum: checksum,
		CodegenVersion: CodegenVersion(),
		SchemaDSL:      "model",
		Exposure:       restrictedExposure().Normalized(),
	}
	restrictedKey := exposureJSON(restrictedExposure())

	if !shouldSkipMigration(rec, checksum, restrictedKey) {
		t.Error("same schema, codegen version and exposure should skip")
	}
	if shouldSkipMigration(rec, checksum, "{}") {
		t.Error("an exposure-only change must not skip phase 1")
	}
	if migrationRecordMatches(rec, checksum, "{}") {
		t.Error("an exposure-only change must not match the record (phase 2 no-op, record-only and recheck paths)")
	}

	// A record from before the exposure column reads as the default.
	legacy := &MigrationRecord{SchemaChecksum: checksum, CodegenVersion: CodegenVersion(), SchemaDSL: "model"}
	if !shouldSkipMigration(legacy, checksum, exposureJSON(Exposure{})) {
		t.Error("a legacy record matches the default exposure")
	}
	if shouldSkipMigration(legacy, checksum, restrictedKey) {
		t.Error("a legacy record does not match a restricted exposure")
	}
}

func TestEffectiveExposure(t *testing.T) {
	recorded := &MigrationRecord{Exposure: restrictedExposure().Normalized()}
	effective := func(requested *Exposure, rec *MigrationRecord) Exposure {
		t.Helper()
		got, err := effectiveExposure(requested, rec)
		if err != nil {
			t.Fatalf("effectiveExposure: %v", err)
		}
		return got
	}

	if got := effective(nil, nil); !got.IsDefault() {
		t.Errorf("nothing requested or recorded: got %+v, want the default", got)
	}
	if got := effective(nil, recorded); !SameExposure(got, restrictedExposure()) {
		t.Errorf("nil keeps the recorded exposure, got %+v", got)
	}
	if got := effective(&Exposure{}, recorded); !got.IsDefault() {
		t.Errorf("an explicit default replaces the recorded exposure, got %+v", got)
	}
	explicit := restrictedExposure()
	if got := effective(&explicit, nil); exposureJSON(got) != exposureJSON(explicit) {
		t.Errorf("an explicit exposure is used as given, got %+v", got)
	}
}

// An invalid pattern is rejected even next to one that matches everything,
// which normalizing would drop together with it.
func TestEffectiveExposure_ValidatesBeforeNormalizing(t *testing.T) {
	for _, include := range [][]string{{"a#b#c"}, {"*", "a#b#c"}, {"*#*", "a#b#c"}} {
		requested := Exposure{Relations: sqlgen.RelationPatterns{Include: include}}
		if _, err := effectiveExposure(&requested, nil); err == nil || !strings.Contains(err.Error(), "more than one '#'") {
			t.Errorf("include %q: got %v, want the invalid pattern rejected", include, err)
		}
	}
}

func TestRequestedExposure(t *testing.T) {
	m := NewMigrator(nil, "")
	if m.requestedExposure(nil) != nil {
		t.Error("no option and no migrator exposure keeps the recorded one")
	}
	own := restrictedExposure()
	m.SetExposure(&own)
	if got := m.requestedExposure(nil); got != &own {
		t.Error("the migrator's exposure applies when the options carry none")
	}
	opt := Exposure{}
	if got := m.requestedExposure(&opt); got != &opt {
		t.Error("the options' exposure wins over the migrator's")
	}
}

func TestInheritedExposureGuard(t *testing.T) {
	recorded := &MigrationRecord{Exposure: restrictedExposure().Normalized()}
	restrictedKey := exposureJSON(restrictedExposure())

	if err := inheritedExposureGuard(nil, recorded, restrictedKey); err != nil {
		t.Errorf("record unchanged since generation: %v", err)
	}
	if err := inheritedExposureGuard(nil, recorded, "{}"); err != errRecordedExposureChanged {
		t.Errorf("recorded exposure changed under a run keeping it: got %v", err)
	}
	if err := inheritedExposureGuard(&Exposure{}, recorded, "{}"); err != nil {
		t.Errorf("an explicit exposure ignores the record: %v", err)
	}
}

func TestUnrecordedExposureGuard(t *testing.T) {
	restricted := restrictedExposure()
	reordered := restrictedExposure()
	reordered.Relations.Include = []string{"document#viewer", "owner", "owner"}
	familiesOff := Exposure{Functions: sqlgen.FunctionFamilies{Explain: boolPtr(false)}}
	restrictedRecord := &MigrationRecord{Exposure: restricted.Normalized()}
	defaultRecord := &MigrationRecord{Exposure: Exposure{}}

	for _, tc := range []struct {
		name      string
		requested *Exposure
		rec       *MigrationRecord
		refused   bool
	}{
		{"nil without a record", nil, nil, false},
		{"nil with a record", nil, restrictedRecord, false},
		{"default without a record", &Exposure{}, nil, false},
		{"include everything without a record", &Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"*"}}}, nil, false},
		{"restricted without a record", &restricted, nil, true},
		{"recorded patterns reordered and duplicated", &reordered, restrictedRecord, false},
		{"default against a restricted record", &Exposure{}, restrictedRecord, true},
		{"default against a pre-column record", &Exposure{}, defaultRecord, false},
		{"families off against a default record", &familiesOff, defaultRecord, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := unrecordedExposureGuard(tc.requested, tc.rec)
			if !tc.refused {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			var refused *UnrecordedExposureChangeError
			if !errors.As(err, &refused) {
				t.Fatalf("want *UnrecordedExposureChangeError, got %v", err)
			}
			if refused.HasRecord != (tc.rec != nil) {
				t.Errorf("HasRecord = %v, want %v", refused.HasRecord, tc.rec != nil)
			}
			if !SameExposure(refused.Requested, *tc.requested) {
				t.Errorf("Requested = %s, want %s", exposureJSON(refused.Requested), exposureJSON(*tc.requested))
			}
			want := Exposure{}
			if tc.rec != nil {
				want = tc.rec.Exposure
			}
			if !SameExposure(refused.Recorded, want) {
				t.Errorf("Recorded = %s, want %s", exposureJSON(refused.Recorded), exposureJSON(want))
			}
		})
	}
}

func TestUnrecordedExposureChangeError_Error(t *testing.T) {
	restricted := restrictedExposure().Normalized()
	for _, tc := range []struct {
		name string
		err  *UnrecordedExposureChangeError
		want []string
	}{
		{
			"against a record",
			&UnrecordedExposureChangeError{Requested: Exposure{}, Recorded: restricted, HasRecord: true},
			[]string{"{}", exposureJSON(restricted), "recorded exposure"},
		},
		{
			"without a record",
			&UnrecordedExposureChangeError{Requested: restricted, Recorded: Exposure{}},
			[]string{exposureJSON(restricted), "default exposure {}", "no migration recorded"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, want := range append(tc.want,
				"records nothing", "nothing was applied", "MigrateWithOptions", "SchemaContent", "`melange migrate`") {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %s", want, msg)
				}
			}
		})
	}
}

func TestDecodeExposure(t *testing.T) {
	for _, v := range []string{"", "{}"} {
		e, err := decodeExposure(nullString(v))
		if err != nil || !e.IsDefault() {
			t.Errorf("decodeExposure(%q) = %+v, %v; want the default", v, e, err)
		}
	}
	e, err := decodeExposure(nullString(exposureJSON(restrictedExposure())))
	if err != nil || !SameExposure(e, restrictedExposure()) {
		t.Errorf("round trip: got %+v, %v", e, err)
	}
	if _, err := decodeExposure(nullString("not json")); err == nil {
		t.Error("a corrupt value is an error")
	}
}

func TestAddExposureColumn(t *testing.T) {
	ddl := addExposureColumn("authz")
	if !strings.Contains(ddl, `ALTER TABLE "authz"."melange_migrations"`) ||
		!strings.Contains(ddl, "ADD COLUMN IF NOT EXISTS exposure JSONB NOT NULL DEFAULT '{}'") {
		t.Errorf("unexpected DDL:\n%s", ddl)
	}
	if !strings.Contains(migrationsDDL(""), "exposure JSONB") {
		t.Error("base DDL should define the exposure column")
	}
	found := false
	for _, stmt := range migrationsTableDDL("") {
		found = found || strings.Contains(stmt, "ADD COLUMN IF NOT EXISTS exposure")
	}
	if !found {
		t.Error("migrationsTableDDL should add the exposure column to existing tables")
	}
}

func TestOutputDryRun_ExposureAndDrops(t *testing.T) {
	m := NewMigrator(nil, "")
	var buf bytes.Buffer
	key := exposureJSON(restrictedExposure())
	drops := []dryRunDrop{
		{Function: "check_document_editor"},
		{Function: "accessible_document_editor", Dependents: []string{"view editor_docs", "policy p on table docs"}},
	}
	write := migrationWrite{SchemaChecksum: "abc", Exposure: key}
	check := GeneratedSQL{Functions: []string{"CREATE OR REPLACE FUNCTION check_document_editor() ..."}}
	if err := m.outputDryRun(&buf, write, check, ListGeneratedSQL{}, drops); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"-- Exposure: " + key,
		"Drop Orphaned Functions (2 functions)",
		`DROP FUNCTION IF EXISTS "public"."check_document_editor";`,
		"--   view editor_docs",
		"--   policy p on table docs",
		`DROP FUNCTION IF EXISTS "public"."accessible_document_editor";`,
		"DO $melange_guard$",
		"ARRAY['check_document_editor', 'accessible_document_editor']::TEXT[]",
		"model_json, exposure)",
		"ON_ERROR_STOP=1",
		// Recorded only once the orphans are gone, for a client that carries
		// on after the guard or a refused drop raises.
		"model_json, exposure)\nSELECT '', 'abc'",
		"'" + key + "'\nWHERE NOT EXISTS (",
		"p.proname = ANY(ARRAY['check_document_editor', 'accessible_document_editor']::TEXT[])\n);",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	// A dependent created after the preview must make the script refuse, not
	// vanish with the function.
	if strings.Contains(out, "CASCADE;") {
		t.Error("an unforced drop must not CASCADE")
	}
	// The script is not one transaction: the guard must run before it
	// changes anything.
	guard := strings.Index(out, "DO $melange_guard$")
	for _, change := range []string{"CREATE TABLE", "CREATE OR REPLACE FUNCTION", "DROP FUNCTION", "INSERT INTO"} {
		if i := strings.Index(out, change); i >= 0 && i < guard {
			t.Errorf("%q comes before the dependents guard", change)
		}
	}
	if strings.Index(out, "Drop Orphaned Functions") > strings.Index(out, "INSERT INTO") {
		t.Error("orphans are dropped before the migration record")
	}

	// Forced drops CASCADE and need no guard, and name the dependents they
	// drop.
	buf.Reset()
	forced := []dryRunDrop{{Function: "check_document_editor", Dependents: []string{"view editor_docs"}, Cascade: true}}
	if err := m.outputDryRun(&buf, write, GeneratedSQL{}, ListGeneratedSQL{}, forced); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `DROP FUNCTION IF EXISTS "public"."check_document_editor" CASCADE;`) ||
		strings.Contains(buf.String(), "$melange_guard$") {
		t.Errorf("a forced drop cascades without a guard:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "-- WARNING: CASCADE also drops these dependents:\n--   view editor_docs\n") ||
		strings.Contains(buf.String(), "refuses this drop") {
		t.Errorf("a forced drop lists the dependents it drops:\n%s", buf.String())
	}

	buf.Reset()
	if err := m.outputDryRun(&buf, migrationWrite{SchemaChecksum: "abc"}, GeneratedSQL{}, ListGeneratedSQL{}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "-- Exposure:") || strings.Contains(buf.String(), "Drop Orphaned Functions") ||
		strings.Contains(buf.String(), "$melange_guard$") {
		t.Error("the default exposure and no orphans add nothing")
	}
	if !strings.Contains(buf.String(), "'{}');") {
		t.Error("an unset exposure records the default")
	}
}

func TestDependentObjectsError(t *testing.T) {
	err := &DependentObjectsError{Dependents: []FunctionDependent{
		{Function: "check_document_viewer", Object: "view doc_access"},
		{Function: "accessible_document_viewer", Object: "policy doc_read on table docs"},
	}}
	msg := err.Error()
	for _, want := range []string{
		"nothing was applied", "force", "view doc_access depends on check_document_viewer",
		"policy doc_read on table docs depends on accessible_document_viewer",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should contain %q", msg, want)
		}
	}
}

func TestDependentObjectsErrorAtDrop(t *testing.T) {
	refusal := errors.New("cannot drop function check_document_viewer() because other objects depend on it (SQLSTATE 2BP01)")
	err := &DependentObjectsError{Err: refusal}
	if !errors.Is(err, refusal) {
		t.Error("the database's refusal should be unwrappable")
	}
	msg := err.Error()
	for _, want := range []string{"not recorded", "force", "SQLSTATE 2BP01"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "nothing was applied") {
		t.Errorf("error %q claims nothing was applied, which only holds in a transaction", msg)
	}
}

func nullString(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
