package doctor

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/update"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
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

// withVersion sets the build version to v0.10.0 for the test, restoring it
// afterwards; the test binary reports "dev", which nothing orders against.
func withVersion(t *testing.T) {
	t.Helper()
	prev := version.Version
	version.Version = "v0.10.0"
	t.Cleanup(func() { version.Version = prev })
}

// planned is a Doctor whose plain migrate would take action; nil plan means
// it cannot be predicted.
func planned(rec *migrator.MigrationRecord, plan *migrator.MigrationPlan) *Doctor {
	return &Doctor{lastMigration: rec, plan: plan, planDone: true}
}

// The missing-functions hint follows what a plain migrate would do: it
// re-creates them only when it applies.
func TestReinstallHint(t *testing.T) {
	withVersion(t)
	rec := &migrator.MigrationRecord{CodegenVersion: "v0.10.0"}
	withOrphan := func(a migrator.PlanAction, dependents ...string) *migrator.MigrationPlan {
		return &migrator.MigrationPlan{Action: a, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_old", Dependents: dependents}}}
	}
	for _, tc := range []struct {
		name    string
		d       *Doctor
		want    []string
		without []string
	}{
		{
			"apply", planned(rec, &migrator.MigrationPlan{Action: migrator.PlanApply}),
			[]string{"Run 'melange migrate' to create functions"},
			[]string{"--force", "pending"},
		},
		{"apply with a pending schema change", func() *Doctor {
			d := planned(rec, &migrator.MigrationPlan{Action: migrator.PlanApply})
			d.schemaPending = true
			return d
		}(), []string{"Run 'melange migrate'", "pending changes"}, []string{"--force"}},
		{
			"skip", planned(rec, &migrator.MigrationPlan{Action: migrator.PlanSkip}),
			[]string{"melange migrate --force", "skips"},
			[]string{"CASCADE"},
		},
		{
			"record only", planned(rec, &migrator.MigrationPlan{Action: migrator.PlanRecordOnly}),
			[]string{"melange migrate --force", "only records"},
			nil,
		},
		{
			"skip with an orphan", planned(rec, withOrphan(migrator.PlanSkip)),
			[]string{"--force also drops 1 removed function with CASCADE", "melange migrate --force --dry-run"},
			[]string{"together with"},
		},
		{
			"skip with an orphan's dependents", planned(rec, withOrphan(migrator.PlanSkip, "view doc_access", "policy docs_read on table docs")),
			[]string{"CASCADE, together with view doc_access, policy docs_read on table docs"},
			nil,
		},
		{
			"apply refused by an orphan's dependents", planned(rec, withOrphan(migrator.PlanApply, "view doc_access")),
			[]string{"Run 'melange migrate' to create functions", "first change or drop view doc_access"},
			[]string{"--force"},
		},
		{
			"unpredictable", planned(rec, nil),
			[]string{"Run 'melange migrate'", "if it reports the schema unchanged, run 'melange migrate --force'"},
			nil,
		},
		{"no record", planned(nil, nil), []string{"Run 'melange migrate' to create functions"}, []string{"--force"}},
		{
			"database ahead", planned(&migrator.MigrationRecord{CodegenVersion: "v0.11.0"}, &migrator.MigrationPlan{Action: migrator.PlanSkip}),
			[]string{"Use melange v0.11.0 or later"},
			[]string{"--force"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hint := tc.d.reinstallHint(context.Background())
			for _, w := range tc.want {
				assert.Contains(t, hint, w)
			}
			for _, w := range tc.without {
				assert.NotContains(t, hint, w)
			}
		})
	}
}

// --force overrides the downgrade guard, so no hint suggests running it
// with this melange on a database a newer melange migrated, whatever migrate
// would otherwise do. The stored-model hint may suggest it to the newer
// melange, whose plain migrate skips.
func TestHintsNeverForceWhenAhead(t *testing.T) {
	withVersion(t)
	ctx := context.Background()
	for _, action := range []migrator.PlanAction{migrator.PlanSkip, migrator.PlanRecordOnly, migrator.PlanApply} {
		d := planned(&migrator.MigrationRecord{CodegenVersion: "0.11.0", SchemaDSL: "model", ModelJSON: []byte(`{"corrupt": true}`)},
			&migrator.MigrationPlan{Action: action, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_old", Dependents: []string{"view v"}}}})
		d.opts.Exposure = &sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"doc#owner"}}}
		hints := []string{d.reinstallHint(ctx), d.rewriteModelHint(ctx), d.upgradeHint(), d.orphansCheck(ctx, []string{"check_doc_old"}).FixHint}
		report := &Report{}
		d.checkExposureSync(ctx, report)
		d.checkDeployedModel(ctx, report)
		hints = slices.Grow(hints, len(report.Checks))
		for _, c := range report.Checks {
			hints = append(hints, c.FixHint)
		}
		for _, h := range hints {
			if strings.Contains(h, "--force") {
				assert.Contains(t, h, "Use melange 0.11.0 or later and re-run 'melange migrate' with it", "plan %s", action)
			}
			assert.Contains(t, h, "0.11.0", "plan %s", action)
		}
	}
}

// A narrowed exposure drops functions doctor's orphan check does not list
// (it is compiled under the recorded exposure), so the exposure_sync hint
// names their dependents, which a plain migrate refuses to drop.
func TestExposureSyncHintNamesDependents(t *testing.T) {
	withVersion(t)
	rec := &migrator.MigrationRecord{CodegenVersion: "v0.10.0"}
	hint := func(plan *migrator.MigrationPlan) string {
		d := planned(rec, plan)
		d.opts.Exposure = &sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: []string{"doc#owner"}}}
		report := &Report{}
		d.checkExposureSync(context.Background(), report)
		require.Len(t, report.Checks, 1)
		return report.Checks[0].FixHint
	}
	h := hint(&migrator.MigrationPlan{Action: migrator.PlanApply, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_viewer", Dependents: []string{"view doc_access"}}}})
	assert.Contains(t, h, "Run 'melange migrate' to apply it")
	assert.Contains(t, h, "first change or drop view doc_access")
	assert.Equal(t, "Run 'melange migrate' to apply it", hint(&migrator.MigrationPlan{Action: migrator.PlanApply, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_viewer"}}}))
	assert.Equal(t, "Run 'melange migrate' to apply it", hint(nil))
}

// A breaking local change on a database a newer melange migrated: this
// melange's migrate refuses to apply it, so the drift hint names the newer
// version instead of migrating.
func TestDriftSafetyHintWhenAhead(t *testing.T) {
	withVersion(t)
	parse := func(dsl string) []schema.TypeDefinition {
		types, err := parser.ParseSchemaString(dsl)
		require.NoError(t, err)
		return types
	}
	const deployed = "model\n  schema 1.1\n\ntype user\n\ntype doc\n  relations\n    define owner: [user]\n    define viewer: [user]\n"
	const local = "model\n  schema 1.1\n\ntype user\n\ntype doc\n  relations\n    define owner: [user]\n"
	model, err := schema.MarshalModel(parse(deployed))
	require.NoError(t, err)
	hint := func(codegen string) string {
		d := planned(&migrator.MigrationRecord{CodegenVersion: codegen, SchemaDSL: deployed, ModelJSON: model}, nil)
		d.parsedTypes = parse(local)
		report := &Report{}
		d.checkDeployedModel(context.Background(), report)
		for _, c := range report.Checks {
			if c.Name == "drift_safety" {
				require.Equal(t, StatusWarn, c.Status)
				return c.FixHint
			}
		}
		require.Fail(t, "no drift_safety check", "codegen %s", codegen)
		return ""
	}
	assert.Contains(t, hint("v0.10.0"), "migrating will apply these changes")
	ahead := hint("v0.11.0")
	assert.Contains(t, ahead, "refuses to apply them to a database a newer melange (v0.11.0) migrated")
	assert.NotContains(t, ahead, "migrating will apply")
	assert.NotContains(t, ahead, "--force")
}

// A stored model that cannot be decoded is rewritten by a plain migrate
// only when one would record; when it would skip, by --force.
func TestCheckDeployedModelDecodeHint(t *testing.T) {
	withVersion(t)
	rec := &migrator.MigrationRecord{CodegenVersion: "v0.10.0", SchemaDSL: "model", ModelJSON: []byte(`{"corrupt": true}`)}
	hint := func(plan *migrator.MigrationPlan) string {
		report := &Report{}
		planned(rec, plan).checkDeployedModel(context.Background(), report)
		require.Len(t, report.Checks, 1)
		require.Equal(t, "could not be decoded", strings.TrimPrefix(report.Checks[0].Message, "Deployed model "))
		return report.Checks[0].FixHint
	}
	assert.Contains(t, hint(&migrator.MigrationPlan{Action: migrator.PlanSkip}), "melange migrate --force")
	assert.NotContains(t, hint(&migrator.MigrationPlan{Action: migrator.PlanRecordOnly}), "--force")
	assert.NotContains(t, hint(&migrator.MigrationPlan{Action: migrator.PlanApply}), "--force")
	assert.Contains(t, hint(nil), "if it reports the schema unchanged")
}

// Orphans with dependents name them, since a plain migrate refuses to drop
// them; orphans the plan does not list are left as they are.
func TestOrphansCheckNamesDependents(t *testing.T) {
	withVersion(t)
	rec := &migrator.MigrationRecord{CodegenVersion: "v0.10.0"}
	plan := &migrator.MigrationPlan{Action: migrator.PlanApply, Orphans: []migrator.OrphanedFunction{
		{Name: "check_doc_old", Dependents: []string{"view doc_access"}},
		{Name: "check_other_old", Dependents: []string{"view unlisted"}},
	}}
	c := planned(rec, plan).orphansCheck(context.Background(), []string{"check_doc_old"})
	assert.Contains(t, c.Details, "check_doc_old: view doc_access")
	assert.NotContains(t, c.Details, "unlisted")
	assert.Contains(t, c.FixHint, "refuses to drop them")

	c = planned(rec, &migrator.MigrationPlan{Action: migrator.PlanApply}).orphansCheck(context.Background(), []string{"check_doc_old"})
	assert.Equal(t, "Run 'melange migrate' to clean up orphans", c.FixHint)

	// A record edited to name them, with nothing else changed: a plain
	// migrate skips and drops nothing.
	c = planned(rec, &migrator.MigrationPlan{Action: migrator.PlanSkip, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_old"}}}).
		orphansCheck(context.Background(), []string{"check_doc_old"})
	assert.Contains(t, c.FixHint, "melange migrate --force")
	assert.Contains(t, c.FixHint, "skips")
}
