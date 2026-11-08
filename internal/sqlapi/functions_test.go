package sqlapi

import (
	"slices"
	"strings"
	"testing"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// installedAsMigrated returns the pg_proc rows a migration of r leaves: one
// row per expected name, with the expected arguments and generated body.
func installedAsMigrated(t *testing.T, r *Report, gen *generated) []installedRow {
	t.Helper()
	var rows []installedRow
	seen := make(map[string]bool)
	add := func(f Function) {
		if seen[f.Name] {
			return
		}
		seen[f.Name] = true
		body, ok := gen.bodies[f.Name]
		if !ok {
			t.Fatalf("no generated body for %s", f.Name)
		}
		rows = append(rows, installedRow{name: f.Name, args: f.Signature.IdentityArgs(), srcMD5: &body})
	}
	for _, rel := range r.Relations {
		for _, f := range rel.Functions {
			add(f)
		}
	}
	for _, d := range r.Dispatchers {
		add(d)
	}
	return rows
}

func recordFor(r *Report, gen *generated) *migrator.MigrationRecord {
	rec := &migrator.MigrationRecord{CodegenVersion: migrator.CodegenVersion(), FunctionChecksums: map[string]string{}}
	for name := range r.Owners() {
		rec.FunctionNames = append(rec.FunctionNames, name)
	}
	for k, v := range gen.checksums {
		rec.FunctionChecksums[k] = v
	}
	return rec
}

func statusOf(r *Report, name string) Function {
	for _, rel := range r.Relations {
		for _, f := range rel.Functions {
			if f.Name == name {
				return f
			}
		}
	}
	for _, d := range r.Dispatchers {
		if d.Name == name {
			return d
		}
	}
	return Function{}
}

// The drift classification: a database as migrated is clean; each change
// to it is reported as what it is, and an application's own function with a
// generated prefix is never an orphan.
func TestReport_Classify(t *testing.T) {
	for _, exposure := range []sqlgen.Exposure{{}, editorOnly()} {
		r, err := Describe(testTypes(t), exposure, "authz")
		if err != nil {
			t.Fatal(err)
		}
		gen, err := generatedFor(testTypes(t), exposure, "authz")
		if err != nil {
			t.Fatal(err)
		}
		rows := installedAsMigrated(t, r, gen)
		rec := recordFor(r, gen)
		r.classify(rows, rec, gen)
		if r.Summary.Drift() || r.Summary.Installed != r.Summary.Functions || len(r.Unmanaged) != 0 || r.CodegenSkew {
			t.Fatalf("%+v: a database as migrated reports drift: %+v", exposure, r.Summary)
		}
		if _, listed := r.Owners()["check_permission_unexposed"]; listed == exposure.IsDefault() {
			t.Errorf("%+v: the raising helper is listed = %v", exposure, !exposure.IsDefault())
		}
	}

	r, err := Describe(testTypes(t), sqlgen.Exposure{}, "authz")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := generatedFor(testTypes(t), sqlgen.Exposure{}, "authz")
	if err != nil {
		t.Fatal(err)
	}
	rows := installedAsMigrated(t, r, gen)
	rec := recordFor(r, gen)
	rec.FunctionNames = append(rec.FunctionNames, "accessiblesk_document_viewer")

	edited := "0cc175b9c0f1b6a831c399e269772661" // another body's md5
	var changed []installedRow
	for _, row := range rows {
		switch row.name {
		case "check_document_owner": // dropped
			continue
		case "check_document_editor": // edited in place
			row.srcMD5 = &edited
		case "list_document_viewer_obj": // signature changed
			row.args = "p_subject_type text, p_subject_id text"
		case "accessible_document_viewer": // an old overload survives
			changed = append(changed, installedRow{name: row.name, args: "p_subject_type text, p_subject_id text"})
		}
		changed = append(changed, row)
	}
	changed = append(changed,
		installedRow{name: "explain_invoice", args: "p_id text"},                                           // the application's own
		installedRow{name: "accessiblesk_document_viewer", args: "p_subject_type text, p_subject_id text"}, // an orphan
	)
	r.classify(changed, rec, gen)
	for name, want := range map[string]Status{
		"check_document_owner":       StatusMissing,
		"check_document_editor":      StatusBodyChanged,
		"list_document_viewer_obj":   StatusSignatureDiffers,
		"accessible_document_viewer": StatusInstalled,
		"check_document_viewer":      StatusInstalled,
	} {
		if got := statusOf(r, name); got.Status != want {
			t.Errorf("%s: %s (%s), want %s", name, got.Status, got.Detail, want)
		}
	}
	if got := statusOf(r, "accessible_document_viewer").StaleOverloads; !slices.Equal(got, []string{"p_subject_type text, p_subject_id text"}) {
		t.Errorf("stale overloads %v", got)
	}
	if !strings.Contains(statusOf(r, "list_document_viewer_obj").Detail, "installed as list_document_viewer_obj(p_subject_type text, p_subject_id text)") {
		t.Errorf("signature detail %q", statusOf(r, "list_document_viewer_obj").Detail)
	}
	// CREATE OR REPLACE cannot change a function's arguments, so the hand-made
	// one is dropped first; without skew, only --force reinstalls.
	if d := statusOf(r, "list_document_viewer_obj").Detail; !strings.Contains(d, `drop it first (DROP FUNCTION "authz".list_document_viewer_obj(p_subject_type text, p_subject_id text);), then melange migrate --force`) {
		t.Errorf("signature remedy %q", d)
	}
	for _, name := range []string{"check_document_owner", "check_document_editor"} {
		if d := statusOf(r, name).Detail; !strings.Contains(d, "melange migrate --force reinstalls it") {
			t.Errorf("%s remedy without skew %q", name, d)
		}
	}
	if len(r.Orphans) != 1 || r.Orphans[0].Name != "accessiblesk_document_viewer" {
		t.Errorf("orphans %+v", r.Orphans)
	}
	if len(r.Unmanaged) != 1 || r.Unmanaged[0].Name != "explain_invoice" {
		t.Errorf("unmanaged %+v", r.Unmanaged)
	}
	s := r.Summary
	if s.Missing != 1 || s.BodyChanged != 1 || s.SignatureDiffers != 1 || s.StaleOverloads != 1 || s.Orphans != 1 || s.Unmanaged != 1 || !s.Drift() {
		t.Errorf("summary %+v", s)
	}

	// A body this melange generates differently, recorded by the last
	// migration, is codegen drift, and so is any body difference under
	// version skew without checksums.
	rec.FunctionChecksums["check_document_editor"] = "recorded-by-another-codegen"
	r.classify(changed, rec, gen)
	if got := statusOf(r, "check_document_editor").Status; got != StatusCodegenDiffers {
		t.Errorf("recorded checksum differs: %s", got)
	}
	skewed := *rec
	skewed.CodegenVersion, skewed.FunctionChecksums = "v0.0.1", nil
	r.Notes = nil
	r.classify(changed, &skewed, gen)
	if got := statusOf(r, "check_document_editor").Status; got != StatusCodegenDiffers || !r.CodegenSkew {
		t.Errorf("skew without checksums: %s, skew %v", got, r.CodegenSkew)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "generated by melange v0.0.1") || r.CodegenAhead {
		t.Errorf("notes %v, ahead %v", r.Notes, r.CodegenAhead)
	}
	// An older codegen migrated: a plain migrate comes first, since --force
	// would CASCADE-drop the old version's orphans and their dependents.
	for _, name := range []string{"check_document_owner", "list_document_viewer_obj"} {
		if d := statusOf(r, name).Detail; !strings.Contains(d, "run melange migrate with this melange") || strings.Index(d, "--force") < strings.Index(d, "run melange migrate") {
			t.Errorf("%s remedy under skew %q", name, d)
		}
	}

	// A newer melange migrated the database: this one's migrate refuses to
	// replace or drop, and the report says so instead of promising it.
	prev := version.Version
	version.Version = "v0.10.0"
	t.Cleanup(func() { version.Version = prev })
	skewed.CodegenVersion = "v0.11.0"
	r.Notes = nil
	r.classify(changed, &skewed, gen)
	if f := statusOf(r, "check_document_editor"); f.Status != StatusCodegenDiffers || strings.Contains(f.Detail, "replaces it") || !r.CodegenAhead {
		t.Errorf("database ahead: %s (%s), ahead %v", f.Status, f.Detail, r.CodegenAhead)
	}
	if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "newer than this melange (v0.10.0)") || strings.Contains(r.Notes[0], "run melange migrate with this melange") {
		t.Errorf("database ahead notes %v", r.Notes)
	}
	// --force would override the downgrade guard: never advised, nor a drop.
	for _, name := range []string{"check_document_owner", "list_document_viewer_obj"} {
		if d := statusOf(r, name).Detail; strings.Contains(d, "--force") || strings.Contains(d, "DROP FUNCTION") || !strings.Contains(d, "use melange v0.11.0 or later") {
			t.Errorf("%s remedy with the database ahead %q", name, d)
		}
	}

	// Without a record nothing is an orphan, and nothing is compared
	// without bodies.
	r.Notes = nil
	r.classify(changed, nil, nil)
	if len(r.Orphans) != 0 || len(r.Unmanaged) != 2 || r.BodiesChecked {
		t.Errorf("no record: orphans %v unmanaged %v", r.Orphans, r.Unmanaged)
	}
	if got := statusOf(r, "check_document_editor").Status; got != StatusInstalled {
		t.Errorf("without bodies: %s", got)
	}
}

// A database migrated through the Go API without a DatabaseSchema has
// unqualified bodies and checksums; checked as public, it is clean, and an
// edit to one of its bodies is still a body change.
func TestReport_UnqualifiedMigration(t *testing.T) {
	r, err := Describe(testTypes(t), sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	unqualified, err := generatedFor(testTypes(t), sqlgen.Exposure{}, "")
	if err != nil {
		t.Fatal(err)
	}
	rows := installedAsMigrated(t, r, unqualified)
	rec := recordFor(r, unqualified)
	gen, err := r.bodiesFor(rows)
	if err != nil {
		t.Fatal(err)
	}
	r.classify(rows, rec, gen)
	if r.Summary.Drift() || r.Summary.Installed != r.Summary.Functions {
		t.Fatalf("an unqualified migration reports drift: %+v", r.Summary)
	}

	edited := "0cc175b9c0f1b6a831c399e269772661"
	for i := range rows {
		if rows[i].name == "check_permission" {
			rows[i].srcMD5 = &edited
		}
	}
	r.classify(rows, rec, gen)
	if got := statusOf(r, "check_permission"); got.Status != StatusBodyChanged {
		t.Errorf("edited check_permission: %s (%s)", got.Status, got.Detail)
	}

	// A qualified migration compiles once.
	qualified, err := generatedFor(testTypes(t), sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	if gen, err := r.bodiesFor(installedAsMigrated(t, r, qualified)); err != nil || gen.unqualified != nil {
		t.Errorf("qualified rows compiled unqualified bodies too (err %v)", err)
	}
}

// Every function the catalog lists has a generated body, so bodies are
// compared for all of them, and the report is in model order.
func TestReport_BodiesAndOrder(t *testing.T) {
	types, err := parser.ParseSchema("../../test/testutil/testdata/schema.fga")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Describe(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := generatedFor(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	installedAsMigrated(t, r, gen) // fails on a missing body
	// Types in model order, each type's relations together, in the order
	// the parsed model lists them.
	var order []string
	for _, rel := range r.Relations {
		if len(order) == 0 || order[len(order)-1] != rel.Type {
			order = append(order, rel.Type)
		}
	}
	var want []string
	for _, typ := range types {
		if len(typ.Relations) > 0 {
			want = append(want, typ.Name)
		}
	}
	if !slices.Equal(order, want) {
		t.Errorf("type order %v, want %v", order, want)
	}
	if r.Source != "file" || r.Checked || r.Summary.Functions == 0 {
		t.Errorf("unchecked report %+v", r.Summary)
	}
}
