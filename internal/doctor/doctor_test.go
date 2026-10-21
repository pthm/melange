package doctor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/update"
	"github.com/pthm/melange/pkg/migrator"
)

// TestGetAnalyses_CachesAndHandlesNilParsedTypes covers both the nil-input
// fallback (no parsed schema yet) and the memoization on second call. The
// real analysis pipeline runs against a tiny schema inline so we don't need
// a database or file fixture.
func TestGetAnalyses_CachesAndHandlesNilParsedTypes(t *testing.T) {
	d := &Doctor{} // no schema parsed yet

	if got := d.getAnalyses(); got != nil {
		t.Errorf("getAnalyses with nil parsedTypes must return nil; got %d entries", len(got))
	}
}

func TestClassifyTupleSignatures(t *testing.T) {
	validTypes := map[string]bool{"user": true, "organization": true, "repository": true}
	validRelations := map[string]map[string]bool{
		"organization": {"owner": true, "member": true},
		"repository":   {"admin": true, "reader": true, "org": true},
	}
	allowedSubjects := map[string]map[string]map[string]bool{
		"organization": {
			"owner":  {"user": true},
			"member": {"user": true},
		},
		"repository": {
			"admin":  {"user": true},
			"reader": {"user": true},
			"org":    {"organization": true},
		},
	}

	t.Run("all valid", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"repository", "admin", "user", 50},
			{"repository", "org", "organization", 30},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Empty(t, f.unknownSubjects)
		assert.Empty(t, f.invalidSubjects)
	})

	t.Run("unknown object type", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"widget", "viewer", "user", 42},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Contains(t, f.unknownObjectTypes[0], "widget")
		assert.Contains(t, f.unknownObjectTypes[0], "42")
		assert.Equal(t, int64(42), f.unknownObjectTypeCount)
	})

	t.Run("unknown relation", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "user", 100},
			{"organization", "billing", "user", 15},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Len(t, f.unknownRelations, 1)
		assert.Contains(t, f.unknownRelations[0], "organization:billing")
		assert.Equal(t, int64(15), f.unknownRelationCount)
	})

	t.Run("unknown subject type", func(t *testing.T) {
		sigs := []tupleSignature{
			{"organization", "member", "device", 30},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Len(t, f.unknownSubjects, 1)
		assert.Contains(t, f.unknownSubjects[0], "device")
		assert.Equal(t, int64(30), f.unknownSubjectCount)
	})

	t.Run("invalid subject type for relation", func(t *testing.T) {
		sigs := []tupleSignature{
			{"repository", "admin", "organization", 5},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Empty(t, f.unknownObjectTypes)
		assert.Empty(t, f.unknownRelations)
		assert.Empty(t, f.unknownSubjects)
		assert.Len(t, f.invalidSubjects, 1)
		assert.Contains(t, f.invalidSubjects[0], "repository:admin")
		assert.Contains(t, f.invalidSubjects[0], "subject_type=organization")
		assert.Equal(t, int64(5), f.invalidSubjectCount)
	})

	t.Run("counts aggregate across signatures", func(t *testing.T) {
		sigs := []tupleSignature{
			{"widget", "viewer", "user", 10},
			{"widget", "editor", "user", 20},
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Equal(t, int64(30), f.unknownObjectTypeCount)
		assert.Contains(t, f.unknownObjectTypes[0], "30")
	})

	t.Run("multiple categories at once", func(t *testing.T) {
		sigs := []tupleSignature{
			{"gadget", "view", "user", 5},              // unknown object type
			{"organization", "billing", "user", 10},    // unknown relation
			{"organization", "member", "device", 3},    // unknown subject type
			{"repository", "admin", "organization", 2}, // invalid subject type
		}
		f := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
		assert.Len(t, f.unknownObjectTypes, 1)
		assert.Len(t, f.unknownRelations, 1)
		assert.Len(t, f.unknownSubjects, 1)
		assert.Len(t, f.invalidSubjects, 1)
	})
}

func TestEmitTupleFindings(t *testing.T) {
	t.Run("all clean", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, StatusPass, report.Checks[0].Status)
		assert.Equal(t, "valid", report.Checks[0].Name)
	})

	t.Run("unknown object types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownObjectTypes:     []string{"widget (10 tuples)"},
			unknownObjectTypeCount: 10,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, StatusWarn, report.Checks[0].Status)
		assert.Equal(t, "unknown_object_types", report.Checks[0].Name)
		assert.Contains(t, report.Checks[0].Message, "10 tuples")
		assert.NotEmpty(t, report.Checks[0].FixHint)
	})

	t.Run("unknown relations only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownRelations:     []string{"org:billing (5 tuples)"},
			unknownRelationCount: 5,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "unknown_relations", report.Checks[0].Name)
	})

	t.Run("unknown subject types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownSubjects:     []string{"device (3 tuples)"},
			unknownSubjectCount: 3,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "unknown_subject_types", report.Checks[0].Name)
	})

	t.Run("invalid subject types only", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			invalidSubjects:     []string{"repo:admin subject_type=org (2 tuples)"},
			invalidSubjectCount: 2,
		})
		require.Len(t, report.Checks, 1)
		assert.Equal(t, "invalid_subject_types", report.Checks[0].Name)
	})

	t.Run("all categories at once", func(t *testing.T) {
		report := &Report{}
		emitTupleFindings(report, tupleFindings{
			unknownObjectTypes:     []string{"a (1 tuples)"},
			unknownObjectTypeCount: 1,
			unknownRelations:       []string{"b:c (2 tuples)"},
			unknownRelationCount:   2,
			unknownSubjects:        []string{"d (3 tuples)"},
			unknownSubjectCount:    3,
			invalidSubjects:        []string{"e:f subject_type=g (4 tuples)"},
			invalidSubjectCount:    4,
		})
		assert.Len(t, report.Checks, 4, "should emit one check per category")
		assert.Equal(t, 0, report.Errors)
		assert.Equal(t, 4, report.Warnings)
	})
}

func TestTruncatedJoin(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e"}

	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 0), "maxItems=0 returns all")
	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 5), "exact count returns all")
	assert.Equal(t, "a\nb\nc\nd\ne", truncatedJoin(items, 10), "over count returns all")
	assert.Equal(t, "a\nb\n... and 3 more", truncatedJoin(items, 2), "truncates with summary")
}

// melange doctor --format json is a contract for scripts and agents: statuses
// are strings and empty details or fixes are omitted.
func TestReportJSON(t *testing.T) {
	var r Report
	r.AddCheck(CheckResult{Category: "Schema", Name: "schema_file", Status: StatusPass, Message: "Schema file valid"})
	r.AddCheck(CheckResult{Category: "Tuples", Name: "tuples_view", Status: StatusFail, Message: "melange_tuples missing", FixHint: "create the view"})

	out, err := json.Marshal(&r)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"checks": [
			{"category": "Schema", "name": "schema_file", "status": "pass", "message": "Schema file valid"},
			{"category": "Tuples", "name": "tuples_view", "status": "fail", "message": "melange_tuples missing", "fix": "create the view"}
		],
		"passed": 1, "warnings": 0, "errors": 1
	}`, string(out))
}

func TestCheckAdvisories(t *testing.T) {
	advisories := []update.Advisory{{ID: "GHSA-test", Severity: "high", Summary: "bypass", Range: "< 0.9.5"}}
	tests := []struct {
		name       string
		version    string
		advisories []update.Advisory
		want       []Status // empty: check skipped
	}{
		{"affected deployment warns", "v0.9.3", advisories, []Status{StatusWarn}},
		{"fixed deployment passes", "v0.9.5", advisories, []Status{StatusPass}},
		{"unknown version is skipped", "", advisories, nil},
		{"development version is skipped", "dev", advisories, nil},
		{"advisories unavailable is skipped", "v0.9.3", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Doctor{
				opts:          Options{Advisories: tt.advisories},
				lastMigration: &migrator.MigrationRecord{MelangeVersion: tt.version},
			}
			report := &Report{}
			d.checkAdvisories(report)
			if len(report.Checks) != len(tt.want) {
				t.Fatalf("got %d checks, want %d: %+v", len(report.Checks), len(tt.want), report.Checks)
			}
			for i, c := range report.Checks {
				if c.Status != tt.want[i] {
					t.Errorf("status = %v, want %v", c.Status, tt.want[i])
				}
				if c.Status == StatusWarn && !strings.Contains(c.Message, "GHSA-test") {
					t.Errorf("warning must name the advisory: %q", c.Message)
				}
			}
		})
	}
}

// Print colors only on a terminal: a buffer gets the plain layout.
func TestReportPrint_PlainWhenNotTerminal(t *testing.T) {
	r := &Report{}
	r.AddCheck(CheckResult{Category: "Schema", Status: StatusPass, Message: "ok"})
	r.AddCheck(CheckResult{Category: "Schema", Status: StatusWarn, Message: "drift", Details: "col\tval", FixHint: "migrate"})
	var b strings.Builder
	r.Print(&b, true)
	// The tab in Details must survive: lipgloss expands tabs unless told not to.
	want := "\nSchema\n  ✓ ok\n  ⚠ drift\n      col\tval\n      Fix: migrate\n\nSummary: 1 passed, 1 warnings, 0 errors\n"
	assert.Equal(t, want, b.String())
}
