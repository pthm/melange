package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/pkg/migrator"
)

func clientCheck(t *testing.T, report *Report, name string) CheckResult {
	t.Helper()
	for _, c := range report.Checks {
		if c.Name == name {
			assert.Equal(t, "Generated Client", c.Category)
			return c
		}
	}
	t.Fatalf("no %s check in %+v", name, report.Checks)
	return CheckResult{}
}

func TestEmitClientFindings(t *testing.T) {
	notInstalled := sqlapi.ClientFinding{
		Code: "function_not_installed", Severity: sqlapi.SeverityError,
		Function: "accessible_document_viewer", Message: "accessible_document_viewer is not installed", Fix: "Run 'melange migrate'",
	}
	internal := sqlapi.ClientFinding{Code: "function_internal", Severity: sqlapi.SeverityWarning, Function: "accessible_folder_viewer", Message: "internal"}
	notExposed := sqlapi.ClientFinding{Code: "relation_not_exposed", Severity: sqlapi.SeverityError, Type: "document", Relation: "owner", Message: "not exposed"}
	info := sqlapi.ClientFinding{Code: "relation_internal", Severity: sqlapi.SeverityInfo, Type: "document", Relation: "parent", Message: "constant only"}

	t.Run("clean", func(t *testing.T) {
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go", Output: "internal/authz"},
			&sqlapi.ClientReport{Findings: []sqlapi.ClientFinding{info}, FunctionsChecked: 3, PairsChecked: 4}, nil)
		require.Len(t, report.Checks, 3)
		assert.Equal(t, 3, report.Passed)
		assert.Equal(t, "All 3 accessible function names the client uses are installed", clientCheck(t, report, "client_functions").Message)
		assert.Contains(t, clientCheck(t, report, "client_relations").Details, "relation_internal (info): constant only")
	})

	t.Run("drift and errors warn by default", func(t *testing.T) {
		report := &Report{}
		cc := &ClientCheck{Runtime: "go", Output: "internal/authz", Drift: []clientgen.FileDrift{{File: "schema_gen.go", Status: "differs", Line: 12}}}
		emitClientFindings(report, cc, &sqlapi.ClientReport{Findings: []sqlapi.ClientFinding{notInstalled, internal, notExposed}, Errors: 2, Warnings: 1}, nil)
		assert.Equal(t, 0, report.Errors)
		assert.Equal(t, 3, report.Warnings)
		file := clientCheck(t, report, "client_file")
		assert.Contains(t, file.Details, "schema_gen.go: differs (first difference at line 12)")
		fns := clientCheck(t, report, "client_functions")
		assert.Equal(t, "2 accessible function names the client uses are not installed as it expects", fns.Message)
		assert.Equal(t, "Run 'melange migrate'", fns.FixHint, "the first finding's fix")
		assert.Contains(t, fns.Details, "not the ones in the files that differ")
	})

	t.Run("errors fail when strict, warnings do not", func(t *testing.T) {
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go", Strict: true, FilesSkipped: "generate.client.output is not set"},
			&sqlapi.ClientReport{Findings: []sqlapi.ClientFinding{internal, notExposed}, Errors: 1, Warnings: 1}, nil)
		require.Len(t, report.Checks, 2, "no client_file check without the files")
		assert.Equal(t, StatusWarn, clientCheck(t, report, "client_functions").Status)
		assert.Equal(t, StatusFail, clientCheck(t, report, "client_relations").Status)
		assert.Contains(t, clientCheck(t, report, "client_functions").Details, "Generated files not compared: generate.client.output is not set")
	})

	t.Run("details are truncated", func(t *testing.T) {
		findings := make([]sqlapi.ClientFinding, 0, 12)
		for i := range 12 {
			f := notInstalled
			f.Function = fmt.Sprintf("accessible_doc_r%d", i)
			findings = append(findings, f)
		}
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go"}, &sqlapi.ClientReport{Findings: findings, Errors: 12}, nil)
		assert.Contains(t, clientCheck(t, report, "client_functions").Details, "... and 2 more")
	})

	t.Run("files and generation errors fail", func(t *testing.T) {
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go", FilesError: "permission denied"}, &sqlapi.ClientReport{}, nil)
		assert.Equal(t, StatusFail, clientCheck(t, report, "client_file").Status)

		report = &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go", GenerateError: "name collision"}, nil, nil)
		require.Len(t, report.Checks, 1, "nothing is compared without the names")
		assert.Equal(t, StatusFail, report.Checks[0].Status)
	})

	t.Run("database error warns", func(t *testing.T) {
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go", FilesSkipped: "no output"}, nil, errors.New("reading pg_proc: denied"))
		c := clientCheck(t, report, "client_functions")
		assert.Equal(t, StatusWarn, c.Status)
		assert.Contains(t, c.Details, "reading pg_proc: denied")
	})

	t.Run("json names", func(t *testing.T) {
		report := &Report{}
		emitClientFindings(report, &ClientCheck{Runtime: "go"}, &sqlapi.ClientReport{}, nil)
		out, err := json.Marshal(report)
		require.NoError(t, err)
		for _, name := range []string{"client_file", "client_functions", "client_relations"} {
			assert.Contains(t, string(out), `"name":"`+name+`"`)
		}
	})
}

// The client's migrate fixes come from doctor's hints, which know when a
// plain migrate is refused or skips.
func TestClientFixFollowsMigrate(t *testing.T) {
	withVersion(t)
	ctx := context.Background()
	migrate := sqlapi.ClientFinding{Remedy: sqlapi.RemedyMigrate, Fix: "run melange migrate, which installs it"}
	reinstall := sqlapi.ClientFinding{Remedy: sqlapi.RemedyReinstall, Fix: "melange migrate --force reinstalls it"}

	ahead := planned(&migrator.MigrationRecord{CodegenVersion: "v0.11.0"}, &migrator.MigrationPlan{Action: migrator.PlanApply})
	for _, f := range []sqlapi.ClientFinding{migrate, reinstall} {
		assert.Contains(t, ahead.clientFix(ctx, f), "Use melange v0.11.0 or later")
	}

	rec := &migrator.MigrationRecord{CodegenVersion: "v0.10.0"}
	apply := planned(rec, &migrator.MigrationPlan{Action: migrator.PlanApply, Orphans: []migrator.OrphanedFunction{{Name: "check_doc_old", Dependents: []string{"view doc_access"}}}})
	fix := apply.clientFix(ctx, migrate)
	assert.True(t, strings.HasPrefix(fix, "Run 'melange migrate' to apply"), fix)
	assert.Contains(t, fix, "first change or drop view doc_access")

	// No record: the database may be built from generate migration files.
	unrecorded := planned(nil, &migrator.MigrationPlan{Action: migrator.PlanApply})
	fix = unrecorded.clientFix(ctx, migrate)
	assert.True(t, strings.HasPrefix(fix, "Run 'melange migrate' to apply"), fix)
	assert.Contains(t, fix, "regenerate them with melange.yaml's sql section")
	assert.NotContains(t, apply.clientFix(ctx, migrate), "generate migration")

	skip := planned(rec, &migrator.MigrationPlan{Action: migrator.PlanSkip})
	assert.Contains(t, skip.clientFix(ctx, reinstall), "melange migrate --force")
	assert.Equal(t, "Regenerate the client", skip.clientFix(ctx, sqlapi.ClientFinding{Remedy: sqlapi.RemedyRegenerate, Fix: "regenerate the client"}))
}
