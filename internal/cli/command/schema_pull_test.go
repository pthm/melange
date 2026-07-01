package command

import (
	"strings"
	"testing"
	"time"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// TestPullHeader_SingleReparses guards that for a single-file schema the header
// uses valid DSL comment syntax, so the pulled file (header + schema) parses.
func TestPullHeader_SingleReparses(t *testing.T) {
	const dsl = "model\n  schema 1.1\n\ntype user\n"
	model := &migrator.DeployedModel{
		DSL:            dsl,
		Format:         migrator.FormatSingle,
		SchemaChecksum: "abc123",
		MelangeVersion: "v0.9.0",
		MigratedAt:     time.Unix(1_700_000_000, 0).UTC(),
	}

	header := pullHeader(model)
	out := header + model.DSL

	// Every header line must be a '#' comment (or blank).
	for _, line := range strings.Split(header, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("header line is not a comment: %q", line)
		}
	}
	// Provenance details are present, but never the DB URL.
	if !strings.Contains(out, "abc123") || !strings.Contains(out, "v0.9.0") {
		t.Error("header should include checksum and version")
	}
	// The combined output re-parses.
	if _, err := parser.ParseSchemaString(out); err != nil {
		t.Fatalf("pulled single-file schema (with header) should parse: %v", err)
	}
}

// TestPullHeader_ModularNotesBundle checks that a modular deployment is honestly
// labeled as a non-parseable bundle rather than claiming to be a single .fga.
func TestPullHeader_ModularNotesBundle(t *testing.T) {
	header := pullHeader(&migrator.DeployedModel{Format: migrator.FormatModular})
	if !strings.Contains(header, "modular") || !strings.Contains(header, "NOT a single parseable") {
		t.Errorf("modular header should flag the bundle, got:\n%s", header)
	}
}

func TestClassifySync(t *testing.T) {
	rec := &migrator.MigrationRecord{SchemaChecksum: "deadbeef"}
	cases := []struct {
		name          string
		localChecksum string
		rec           *migrator.MigrationRecord
		want          string
	}{
		{"no migration record", "deadbeef", nil, syncNotRecorded},
		{"no local schema", "", rec, syncUnknown},
		{"matching checksum", "deadbeef", rec, syncInSync},
		{"differing checksum", "cafe", rec, syncDrift},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifySync(tc.localChecksum, tc.rec); got != tc.want {
				t.Errorf("classifySync = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPresentMissingAndShortChecksum(t *testing.T) {
	if presentMissing(true) != "present" || presentMissing(false) != "missing" {
		t.Error("presentMissing mapping is wrong")
	}
	if got := shortChecksum("0123456789abcdef0123"); got != "0123456789ab…" {
		t.Errorf("shortChecksum = %q", got)
	}
	if got := shortChecksum("short"); got != "short" {
		t.Errorf("shortChecksum should pass through short input, got %q", got)
	}
}
