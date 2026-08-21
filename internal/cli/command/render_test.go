package command

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pthm/melange/internal/cli"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// These tests pin the text each command prints. The CLI reference documents
// these formats verbatim, so a change here should be a change there too.

// testSchemaPath is the schema location these fixtures report; the value only
// appears in output, so one is enough.
const testSchemaPath = "melange/schema.fga"

func renderStatus(t *testing.T, r statusReport, s *migrator.Status) string {
	t.Helper()
	var b strings.Builder
	printStatusText(&b, r, s, testSchemaPath)
	return b.String()
}

func TestPrintStatusText_InSync(t *testing.T) {
	out := renderStatus(t, statusReport{
		SchemaFile: "present",
		TuplesView: "present",
		Sync:       syncInSync,
		Deployed: &deployedReport{
			MelangeVersion: "v0.9.0",
			MigratedAt:     "2026-07-01T20:46:10Z",
			Checksum:       "d0c1746f7e26ea40027a24b1c0e0c5f34e279e7c27f6fa17e4611ce2f1ec0962",
			Format:         "single",
			ModelRecorded:  true,
		},
	}, &migrator.Status{SchemaExists: true, TuplesExists: true})

	for _, want := range []string{
		"Schema file:  present",
		"Tuples view:  present",
		"Deployed:     checksum d0c1746f7e26… · melange v0.9.0 · 2026-07-01T20:46:10Z",
		"Sync:         in sync — local schema matches deployed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "breaking") {
		t.Errorf("an in-sync status must not print drift detail:\n%s", out)
	}
}

func TestPrintStatusText_DriftListsChanges(t *testing.T) {
	out := renderStatus(t, statusReport{
		SchemaFile: "present",
		TuplesView: "present",
		Sync:       syncDrift,
		Deployed:   &deployedReport{Checksum: "abc123", ModelRecorded: true},
		Drift: &driftReport{
			Additive: 2,
			Breaking: 1,
			Changes: []schema.Change{
				{Class: schema.ClassAdditive, Type: "audit_log", Summary: "type audit_log added"},
				{Class: schema.ClassAdditive, Type: "document", Relation: "can_export", Summary: "relation document.can_export added"},
				{Class: schema.ClassBreaking, Type: "document", Relation: "legacy_viewer", Summary: "relation document.legacy_viewer removed"},
			},
		},
	}, &migrator.Status{SchemaExists: true, TuplesExists: true})

	for _, want := range []string{
		"1 breaking, 2 additive",
		"+ type audit_log added",
		"+ relation document.can_export added",
		"- relation document.legacy_viewer removed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The change list is a summary, not the full diff.
func TestPrintDriftDetail_CapsTheChangeList(t *testing.T) {
	changes := make([]schema.Change, 0, 9)
	for i := range 9 {
		changes = append(changes, schema.Change{
			Class:   schema.ClassAdditive,
			Type:    "document",
			Summary: string(rune('a'+i)) + " added",
		})
	}

	var b strings.Builder
	printDriftDetail(&b, &driftReport{Additive: 9, Changes: changes})
	out := b.String()

	if lines := strings.Count(out, " added\n"); lines != maxDriftLines {
		t.Errorf("printed %d change lines, want %d", lines, maxDriftLines)
	}
	if !strings.Contains(out, "… and 4 more (`melange diff` for the full list)") {
		t.Errorf("missing the truncation hint in:\n%s", out)
	}
}

func TestPrintStatusText_NotRecordedHint(t *testing.T) {
	out := renderStatus(t, statusReport{
		SchemaFile: "present",
		TuplesView: "present",
		Sync:       syncDrift,
		Deployed:   &deployedReport{Checksum: "abc123", ModelRecorded: false},
	}, &migrator.Status{SchemaExists: true, TuplesExists: true})

	if !strings.Contains(out, "model DSL not recorded — re-migrate to enable `melange schema pull`") {
		t.Errorf("a record without a model must say so:\n%s", out)
	}
}

func TestPrintStatusText_NotesAndMissingPieces(t *testing.T) {
	out := renderStatus(t, statusReport{
		SchemaFile: "missing",
		TuplesView: "missing",
		Sync:       syncNotRecorded,
		Notes:      []string{"could not read migration record: permission denied"},
	}, &migrator.Status{SchemaExists: false, TuplesExists: false})

	for _, want := range []string{
		"Deployed:     no migration recorded",
		"Note:         could not read migration record: permission denied",
		"No schema found at melange/schema.fga",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderDiff_TreeOutput(t *testing.T) {
	d := schema.SchemaDiff{Changes: []schema.Change{
		{Class: schema.ClassAdditive, Type: "audit_log", Summary: "type audit_log added"},
		{Class: schema.ClassBreaking, Type: "document", Relation: "legacy", Summary: "relation document.legacy removed"},
	}}

	var b strings.Builder
	renderDiff(&b, d, "deployed", "melange/schema.fga")
	out := b.String()

	for _, want := range []string{
		"Comparing deployed → melange/schema.fga",
		"  additive  type audit_log added",
		"  BREAKING  relation document.legacy removed",
		"1 breaking, 1 additive",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderDiff_EquivalentSchemas(t *testing.T) {
	var b strings.Builder
	renderDiff(&b, schema.SchemaDiff{}, "git:main", "melange/schema.fga")

	if !strings.Contains(b.String(), "No changes — schemas are equivalent.") {
		t.Errorf("unexpected output:\n%s", b.String())
	}
}

func TestHistoryEntries_MapsRecords(t *testing.T) {
	at := time.Date(2026, 7, 2, 20, 46, 10, 0, time.UTC)
	entries := historyEntries([]migrator.MigrationRecord{
		{ID: 2, MigratedAt: at, MelangeVersion: "v0.9.1", SchemaChecksum: "550b0008a779aa", SchemaFormat: "single", FunctionNames: []string{"a", "b"}},
		{ID: 1}, // a legacy row: no timestamp, version, or format
	})

	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].MigratedAt != "2026-07-02T20:46:10Z" {
		t.Errorf("MigratedAt = %q, want RFC3339", entries[0].MigratedAt)
	}
	if entries[0].FunctionCount != 2 {
		t.Errorf("FunctionCount = %d, want 2", entries[0].FunctionCount)
	}
	// Fields are present-but-empty rather than omitted, so the JSON shape is stable.
	if entries[1].MigratedAt != "" || entries[1].MelangeVersion != "" || entries[1].Format != "" {
		t.Errorf("legacy row should carry empty fields, got %+v", entries[1])
	}
}

func TestPrintHistory_TextAndEmpty(t *testing.T) {
	var b strings.Builder
	printHistory(&b, []historyEntry{{
		ID: 1, MigratedAt: "2026-07-02T20:46:10Z", MelangeVersion: "v0.9.1",
		Checksum: "550b0008a779aa40027a24b1c0e0c5f34e279e7c27f6fa17e4611ce2f1ec0962",
		Format:   "single", FunctionCount: 23,
	}})
	want := "  2026-07-02T20:46:10Z · melange v0.9.1 · checksum 550b0008a779… · single · 23 functions"
	if !strings.Contains(b.String(), want) {
		t.Errorf("missing %q in:\n%s", want, b.String())
	}

	var empty strings.Builder
	printHistory(&empty, nil)
	if !strings.Contains(empty.String(), "No migrations recorded in this database.") {
		t.Errorf("unexpected empty output: %q", empty.String())
	}
}

// A row written before melange_version and schema_format existed still renders,
// with the unknowns labeled rather than blank.
func TestPrintHistory_LegacyRow(t *testing.T) {
	var b strings.Builder
	printHistory(&b, []historyEntry{{ID: 1, Checksum: "abc", FunctionCount: 3}})
	out := b.String()

	for _, want := range []string{"(unknown time)", "melange unknown", "3 functions"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, " ·  · ") {
		t.Errorf("absent format should be omitted, not left blank:\n%s", out)
	}
}

func TestPrintEnvironments_ListsAndMarksActive(t *testing.T) {
	cfg := &cli.Config{
		DefaultEnvironment: "local",
		Environments: map[string]cli.EnvironmentConfig{
			"production": {Database: cli.DatabaseConfig{URL: "postgres://prod/app"}},
			"local":      {Database: cli.DatabaseConfig{URL: "postgres://localhost/app"}},
		},
	}

	var b strings.Builder
	if err := printEnvironments(&b, cfg, "production"); err != nil {
		t.Fatalf("printEnvironments: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "* production") {
		t.Errorf("the active environment must be marked:\n%s", out)
	}
	if !strings.Contains(out, "  local") {
		t.Errorf("inactive environments must be listed unmarked:\n%s", out)
	}
	if strings.Index(out, "local") > strings.Index(out, "production") {
		t.Errorf("environments should be listed in sorted order:\n%s", out)
	}
	if !strings.Contains(out, "Default: local") || !strings.Contains(out, "Active:  production") {
		t.Errorf("missing the default/active footer:\n%s", out)
	}
}

// With no profiles configured, the listing explains how to add one rather than
// printing an empty section.
func TestPrintEnvironments_NoneConfigured(t *testing.T) {
	var b strings.Builder
	if err := printEnvironments(&b, &cli.Config{}, ""); err != nil {
		t.Fatalf("printEnvironments: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "No environments defined.") || !strings.Contains(out, "environments:") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestPullContent_HeaderToggle(t *testing.T) {
	model := &migrator.DeployedModel{
		DSL:            "model\n  schema 1.1\ntype user\n",
		Format:         migrator.FormatSingle,
		SchemaChecksum: "abc123",
		MigratedAt:     time.Date(2026, 7, 1, 20, 46, 10, 0, time.UTC),
		MelangeVersion: "v0.9.0",
	}

	withHeader := pullContent(model, false)
	if !strings.HasPrefix(withHeader, "# Pulled from a melange-migrated database") {
		t.Errorf("expected a provenance header, got:\n%s", withHeader)
	}
	if !strings.HasSuffix(withHeader, model.DSL) {
		t.Errorf("the DSL must follow the header verbatim, got:\n%s", withHeader)
	}
	if got := pullContent(model, true); got != model.DSL {
		t.Errorf("--no-header must emit the DSL alone, got:\n%s", got)
	}
}

// --format json emits the SchemaDiff itself, so tooling gets the classification
// rather than having to parse the tree output.
func TestRenderDiff_JSONOutput(t *testing.T) {
	orig := diffFormat
	t.Cleanup(func() { diffFormat = orig })
	diffFormat = "json"

	d := schema.SchemaDiff{Changes: []schema.Change{
		{Class: schema.ClassBreaking, Type: "document", Relation: "legacy", Summary: "relation document.legacy removed"},
	}}

	var b strings.Builder
	renderDiff(&b, d, "deployed", "melange/schema.fga")

	var decoded schema.SchemaDiff
	if err := json.Unmarshal([]byte(b.String()), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if len(decoded.Changes) != 1 || decoded.Changes[0].Class != schema.ClassBreaking {
		t.Errorf("round-trip lost the change: %+v", decoded)
	}
	if strings.Contains(b.String(), "Comparing") {
		t.Errorf("json output must not carry the tree header:\n%s", b.String())
	}
}

func TestValidateHistoryFlags(t *testing.T) {
	if err := validateHistoryFlags("text", 20); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := validateHistoryFlags("json", 1); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := validateHistoryFlags("yaml", 20); err == nil || !strings.Contains(err.Error(), "invalid --format") {
		t.Errorf("error = %v, want an invalid-format error", err)
	}
	if err := validateHistoryFlags("text", 0); err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Errorf("error = %v, want a limit error", err)
	}
}
