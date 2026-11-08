// Package doctor provides health checks for melange authorization infrastructure.
//
// The doctor command validates that the authorization system is properly configured
// by checking schema files, database state, generated functions, and data health.
//
// Example usage:
//
//	d := doctor.New(db, "schemas")
//	report, err := d.Run(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//	report.Print(os.Stdout, true) // verbose=true
package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/pthm/melange/internal/cli/style"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/internal/update"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// Status represents the result of a health check.
type Status int

const (
	// StatusPass indicates the check passed.
	StatusPass Status = iota
	// StatusWarn indicates a non-critical issue.
	StatusWarn
	// StatusFail indicates a critical issue that will cause failures.
	StatusFail
)

// Symbol returns a status indicator symbol for terminal output.
func (s Status) Symbol() string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusWarn:
		return "⚠"
	case StatusFail:
		return "✗"
	default:
		return "?"
	}
}

// MarshalText renders the status as "pass", "warn", or "fail" in JSON output.
func (s Status) MarshalText() ([]byte, error) {
	switch s {
	case StatusPass:
		return []byte("pass"), nil
	case StatusWarn:
		return []byte("warn"), nil
	case StatusFail:
		return []byte("fail"), nil
	default:
		return nil, fmt.Errorf("unknown status %d", s)
	}
}

// CheckResult represents the outcome of a single health check.
type CheckResult struct {
	// Category groups related checks (e.g., "schema", "functions", "tuples").
	Category string `json:"category"`

	// Name is a short identifier for the check.
	Name string `json:"name"`

	// Status is the check outcome.
	Status Status `json:"status"`

	// Message is a human-readable description of the result.
	Message string `json:"message"`

	// Details provides additional information for verbose output.
	Details string `json:"details,omitempty"`

	// FixHint suggests how to resolve issues.
	FixHint string `json:"fix,omitempty"`
}

// Report contains all health check results.
type Report struct {
	Checks []CheckResult `json:"checks"`

	// Summary counts.
	Passed   int `json:"passed"`
	Warnings int `json:"warnings"`
	Errors   int `json:"errors"`
}

// AddCheck adds a check result and updates summary counts.
func (r *Report) AddCheck(check CheckResult) {
	r.Checks = append(r.Checks, check)
	switch check.Status {
	case StatusPass:
		r.Passed++
	case StatusWarn:
		r.Warnings++
	case StatusFail:
		r.Errors++
	}
}

// Print writes the report to the given writer, colored when w is a terminal
// and NO_COLOR is unset.
func (r *Report) Print(w io.Writer, verbose bool) {
	w = style.Writer(w)
	st := style.StatusStyles
	heading, dim := st.Bold, st.Muted
	statusStyle := map[Status]lipgloss.Style{StatusPass: st.Good, StatusWarn: st.Warn, StatusFail: st.Bad}

	// Group checks by category
	categories := make(map[string][]CheckResult)
	var categoryOrder []string
	for _, check := range r.Checks {
		if _, exists := categories[check.Category]; !exists {
			categoryOrder = append(categoryOrder, check.Category)
		}
		categories[check.Category] = append(categories[check.Category], check)
	}

	// Print each category
	for _, cat := range categoryOrder {
		_, _ = fmt.Fprintf(w, "\n%s\n", heading.Render(cat))
		for _, check := range categories[cat] {
			_, _ = fmt.Fprintf(w, "  %s %s\n", statusStyle[check.Status].Render(check.Status.Symbol()), check.Message)
			if verbose && check.Details != "" {
				// Indent details
				for _, line := range strings.Split(check.Details, "\n") {
					_, _ = fmt.Fprintf(w, "      %s\n", dim.Render(line))
				}
			}
			if check.Status != StatusPass && check.FixHint != "" {
				_, _ = fmt.Fprintf(w, "      %s %s\n", statusStyle[check.Status].Render("Fix:"), check.FixHint)
			}
		}
	}

	// Print summary, coloring only the counts that need attention.
	count := func(n int, label string, st Status) string {
		text := fmt.Sprintf("%d %s", n, label)
		if n == 0 {
			return text
		}
		return statusStyle[st].Render(text)
	}
	_, _ = fmt.Fprintf(w, "\n%s %s, %s, %s\n", heading.Render("Summary:"),
		count(r.Passed, "passed", StatusPass), count(r.Warnings, "warnings", StatusWarn), count(r.Errors, "errors", StatusFail))
}

// HasErrors returns true if any check failed.
func (r *Report) HasErrors() bool {
	return r.Errors > 0
}

// Options configures doctor behavior.
type Options struct {
	SkipPerformance bool

	// Advisories are the published security advisories to match against the
	// melange version that generated the deployed functions. Nil skips the
	// check (update checks disabled, or the fetch failed).
	Advisories []update.Advisory

	// Exposure is the SQL API exposure configured locally, the one migrate
	// would apply. Nil means none is configured: migrate keeps the recorded
	// exposure, so there is nothing to drift from. The expected functions
	// always follow the recorded exposure, which is what is installed.
	Exposure *sqlgen.Exposure
}

// Doctor performs health checks on the melange authorization infrastructure.
type Doctor struct {
	db             *sql.DB
	databaseSchema string
	schemaPath     string
	opts           Options

	// Cached data from checks (populated during Run)
	parsedTypes   []schema.TypeDefinition
	lastMigration *migrator.MigrationRecord
	expectedFuncs []string
	currentFuncs  []string
	tuplesInfo    *TuplesInfo
	viewDef       *ViewDefinition

	// analyses is the parsed schema run through sqlgen's analysis pipeline.
	// Lazily computed on first call to getAnalyses(); shared by checks that
	// need RelationAnalysis (generated-functions check, index recommendations).
	analyses []sqlgen.RelationAnalysis

	// schemaContent is the local schema text migrate would checksum, ""
	// when it could not be read. schemaPending and exposurePending record
	// that schema_sync and exposure_sync found changes a migrate applies.
	schemaContent                  string
	schemaPending, exposurePending bool

	// plan is what a plain migrate would do, from migratePlan; computed on
	// first use, only by a check that needs it for its fix hint.
	plan     *migrator.MigrationPlan
	planDone bool
}

// migratePlan returns what an unforced `melange migrate` of the local schema
// and exposure would do, or nil when it cannot be predicted: no readable or
// valid local schema, or the prediction failed. A nil local exposure keeps
// the recorded one, as library migrate does. It compiles the model, so it
// runs only for a check that needs it, once.
func (d *Doctor) migratePlan(ctx context.Context) *migrator.MigrationPlan {
	if d.planDone {
		return d.plan
	}
	d.planDone = true
	if d.parsedTypes == nil || d.schemaContent == "" {
		return nil
	}
	m := migrator.NewMigrator(d.db, d.schemaPath)
	m.SetDatabaseSchema(d.databaseSchema)
	plan, err := m.Plan(ctx, d.parsedTypes, migrator.InternalMigrateOptions{
		SchemaContent: d.schemaContent,
		Exposure:      d.opts.Exposure,
	})
	if err == nil {
		d.plan = plan
	}
	return d.plan
}

// ahead reports whether a newer melange migrated the database, so this
// melange's migrate refuses to apply anything to it (DatabaseAheadError).
func (d *Doctor) ahead() bool {
	return d.lastMigration != nil && migrator.CodegenVersionAhead(d.lastMigration.CodegenVersion)
}

// recordedCodegen names the codegen version of the last migration.
func (d *Doctor) recordedCodegen() string {
	if d.lastMigration == nil || d.lastMigration.CodegenVersion == "" {
		return "(unrecorded)"
	}
	return d.lastMigration.CodegenVersion
}

// plainMigrateSkips is why a plain migrate fixes nothing on a database
// already migrated with this schema, exposure and codegen version, and only
// --force does.
const plainMigrateSkips = "a plain migrate skips while the schema, exposure and codegen version are unchanged"

// upgradeHint is the fix for a database a newer melange migrated: use that
// version. --force would override the downgrade guard, so it is never
// suggested.
func (d *Doctor) upgradeHint() string {
	v := d.recordedCodegen()
	return fmt.Sprintf("Use melange %s or later: a newer melange (%s) migrated the database, and this melange's migrate refuses to downgrade it", v, v)
}

// reinstallHint is the fix for generated functions missing from the
// database. A plain migrate re-creates them only when it applies; when it
// would skip or only record, --force is what reinstalls them.
func (d *Doctor) reinstallHint(ctx context.Context) string {
	const plain = "Run 'melange migrate' to create functions"
	switch {
	case d.ahead():
		return d.upgradeHint()
	case d.lastMigration == nil:
		return plain
	}
	plan := d.migratePlan(ctx)
	if plan == nil {
		return plain + "; if it reports the schema unchanged, run 'melange migrate --force'"
	}
	switch plan.Action {
	case migrator.PlanSkip:
		return "Run 'melange migrate --force' to reinstall them: " + plainMigrateSkips + forceCaveat(plan)
	case migrator.PlanRecordOnly:
		return "Run 'melange migrate --force' to reinstall them: a plain migrate generates the same SQL as the last migration, so it only records the new state" + forceCaveat(plan)
	case migrator.PlanRefusedAhead:
		return d.upgradeHint()
	}
	hint := plain
	if d.schemaPending || d.exposurePending {
		hint += "; it also applies the pending changes reported under Migration State"
	}
	return hint + refusedDependents(plan)
}

// applyHint is the fix for a pending change a plain migrate applies. It
// names the dependents of the functions that migrate drops, since it
// refuses to drop those; the plan is computed only here, on the warn path.
// On a database a newer melange migrated, migrate applies nothing, so the
// fix is upgradeHint.
func (d *Doctor) applyHint(ctx context.Context, hint string) string {
	if d.ahead() {
		return d.upgradeHint()
	}
	if plan := d.migratePlan(ctx); plan != nil && plan.Action == migrator.PlanApply {
		hint += refusedDependents(plan)
	}
	return hint
}

// refusedDependents is the clause naming the dependents an applying
// migration refuses to drop, or "" when it drops none with dependents.
func refusedDependents(plan *migrator.MigrationPlan) string {
	deps := dependents(plan)
	if len(deps) == 0 {
		return ""
	}
	return "; it refuses to drop removed functions that other objects depend on, so first change or drop " + strings.Join(deps, ", ")
}

// dependents lists the objects that depend on the plan's orphans.
func dependents(plan *migrator.MigrationPlan) []string {
	var deps []string
	for _, o := range plan.Orphans {
		deps = append(deps, o.Dependents...)
	}
	return deps
}

// forceCaveat says what --force drops besides reinstalling: the orphans an
// applying migration drops, with CASCADE, and their dependents by name,
// since losing an RLS policy widens access.
func forceCaveat(plan *migrator.MigrationPlan) string {
	if len(plan.Orphans) == 0 {
		return ""
	}
	noun := "functions"
	if len(plan.Orphans) == 1 {
		noun = "function"
	}
	together := ""
	if deps := dependents(plan); len(deps) > 0 {
		together = ", together with " + strings.Join(deps, ", ")
	}
	return fmt.Sprintf(" (--force also drops %d removed %s with CASCADE%s; preview with 'melange migrate --force --dry-run')",
		len(plan.Orphans), noun, together)
}

// getAnalyses returns the cached schema analyses or computes them on demand.
// Returns nil if the schema couldn't be parsed (checkSchemaFile already
// reported that as a separate error).
func (d *Doctor) getAnalyses() []sqlgen.RelationAnalysis {
	if d.analyses != nil {
		return d.analyses
	}
	if d.parsedTypes == nil {
		return nil
	}
	closureRows := schema.ComputeRelationClosure(d.parsedTypes)
	analyses := sqlgen.AnalyzeRelations(d.parsedTypes, closureRows)
	analyses = sqlgen.ComputeCanGenerate(analyses)
	d.analyses = analyses
	return analyses
}

// TuplesInfo contains information about the melange_tuples relation.
type TuplesInfo struct {
	Exists     bool
	RelKind    string // 'r' = table, 'v' = view, 'm' = materialized view
	RelKindStr string // human-readable
	Columns    []string
	RowCount   int64 // -1 if unknown/expensive to compute
}

// New creates a new Doctor instance.
func New(db *sql.DB, schemaPath string, opts ...Options) *Doctor {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	return &Doctor{
		db:         db,
		schemaPath: schemaPath,
		opts:       o,
	}
}

// SetDatabaseSchema sets the PostgreSQL schema for melange objects.
func (d *Doctor) SetDatabaseSchema(databaseSchema string) {
	d.databaseSchema = databaseSchema
}

// DatabaseSchema returns the database schema.
func (d *Doctor) DatabaseSchema() string {
	return d.databaseSchema
}

// Run executes all health checks and returns a report.
func (d *Doctor) Run(ctx context.Context) (*Report, error) {
	report := &Report{}

	// Run checks in order, building up cached data
	d.checkSchemaFile(report)
	if err := d.checkMigrationState(ctx, report); err != nil {
		return nil, fmt.Errorf("checking migration state: %w", err)
	}
	d.checkDeployedModel(ctx, report)
	d.checkAdvisories(report)
	if err := d.checkGeneratedFunctions(ctx, report); err != nil {
		return nil, fmt.Errorf("checking generated functions: %w", err)
	}
	if err := d.checkTuplesSource(ctx, report); err != nil {
		return nil, fmt.Errorf("checking tuples source: %w", err)
	}
	if err := d.checkDataHealth(ctx, report); err != nil {
		return nil, fmt.Errorf("checking data health: %w", err)
	}

	// Performance checks (skip if disabled)
	if !d.opts.SkipPerformance && d.tuplesInfo != nil && d.tuplesInfo.Exists {
		switch d.tuplesInfo.RelKind {
		case "v":
			if err := d.checkViewDefinition(ctx, report); err != nil {
				return nil, fmt.Errorf("checking view definition: %w", err)
			}
			if err := d.checkExpressionIndexes(ctx, report); err != nil {
				return nil, fmt.Errorf("checking expression indexes: %w", err)
			}
			if err := d.checkSourceTableIndexes(ctx, report); err != nil {
				return nil, fmt.Errorf("checking source-table indexes: %w", err)
			}
			if err := d.checkSubjectLookupIndexes(ctx, report); err != nil {
				return nil, fmt.Errorf("checking subject-lookup indexes: %w", err)
			}
		case "r":
			if err := d.checkTableIndexes(ctx, report); err != nil {
				return nil, fmt.Errorf("checking table indexes: %w", err)
			}
		}
		if err := d.checkExpandFanoutAdvisory(ctx, report); err != nil {
			return nil, fmt.Errorf("checking Expand fan-out advisory: %w", err)
		}
	}

	return report, nil
}

// checkSchemaFile validates the schema file exists and is valid.
func (d *Doctor) checkSchemaFile(report *Report) {
	if d.schemaPath == "" {
		return // no local schema (studio without one): nothing to check
	}
	m := migrator.NewMigrator(d.db, d.schemaPath)
	m.SetDatabaseSchema(d.databaseSchema)

	schemaPath := m.SchemaPath()

	// Check file exists
	if !m.HasSchema() {
		report.AddCheck(CheckResult{
			Category: "Schema File",
			Name:     "exists",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Schema file not found at %s", schemaPath),
			FixHint:  "Create a schema.fga file in your schemas directory",
		})
		return
	}

	report.AddCheck(CheckResult{
		Category: "Schema File",
		Name:     "exists",
		Status:   StatusPass,
		Message:  fmt.Sprintf("Schema file exists at %s", schemaPath),
	})

	// Try to parse the schema
	types, err := parser.ParseSchema(schemaPath)
	if err != nil {
		report.AddCheck(CheckResult{
			Category: "Schema File",
			Name:     "valid",
			Status:   StatusFail,
			Message:  "Schema is invalid",
			Details:  err.Error(),
			FixHint:  "Run 'melange validate' to see each error",
		})
		return
	}

	d.parsedTypes = types

	// Count types and relations
	relationCount := 0
	for _, t := range types {
		relationCount += len(t.Relations)
	}

	report.AddCheck(CheckResult{
		Category: "Schema File",
		Name:     "valid",
		Status:   StatusPass,
		Message:  fmt.Sprintf("Schema is valid (%d types, %d relations)", len(types), relationCount),
	})

	// Check for cycles
	if err := schema.DetectCycles(types); err != nil {
		report.AddCheck(CheckResult{
			Category: "Schema File",
			Name:     "cycles",
			Status:   StatusFail,
			Message:  "Schema contains cyclic dependencies",
			Details:  err.Error(),
			FixHint:  "Review implied-by relationships for cycles",
		})
		return
	}

	report.AddCheck(CheckResult{
		Category: "Schema File",
		Name:     "cycles",
		Status:   StatusPass,
		Message:  "No cyclic dependencies detected",
	})
}

// checkMigrationState validates the migration tracking table and state.
func (d *Doctor) checkMigrationState(ctx context.Context, report *Report) error {
	m := migrator.NewMigrator(d.db, d.schemaPath)
	m.SetDatabaseSchema(d.databaseSchema)

	// Check if migrations table exists
	var tableExists bool
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`
			SELECT EXISTS (
				SELECT 1 FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE c.relname = 'melange_migrations'
				AND n.nspname = %s
			)
		`,
		d.postgresSchema(),
	)).Scan(&tableExists)
	if err != nil {
		return fmt.Errorf("checking migrations table: %w", err)
	}

	if !tableExists {
		report.AddCheck(CheckResult{
			Category: "Migration State",
			Name:     "table_exists",
			Status:   StatusWarn,
			Message:  "melange_migrations table does not exist",
			Details:  "Migration tracking is not set up",
			FixHint:  "Run 'melange migrate' to create it",
		})
		return nil
	}

	report.AddCheck(CheckResult{
		Category: "Migration State",
		Name:     "table_exists",
		Status:   StatusPass,
		Message:  "melange_migrations table exists",
	})

	// Get last migration
	lastMigration, err := m.GetLastMigration(ctx)
	if err != nil {
		return fmt.Errorf("getting last migration: %w", err)
	}

	if lastMigration == nil {
		report.AddCheck(CheckResult{
			Category: "Migration State",
			Name:     "migrated",
			Status:   StatusWarn,
			Message:  "No migration records found",
			FixHint:  "Run 'melange migrate' to apply the schema",
		})
		return nil
	}

	d.lastMigration = lastMigration

	report.AddCheck(CheckResult{
		Category: "Migration State",
		Name:     "migrated",
		Status:   StatusPass,
		Message:  fmt.Sprintf("Schema migrated (%d functions tracked)", len(lastMigration.FunctionNames)),
	})

	// Check if schema has changed since last migration
	if d.parsedTypes != nil {
		// Hash what migrate hashes: for a modular schema, the manifest plus
		// every module, not the fga.mod bytes alone.
		content, err := parser.ReadSchemaContent(m.SchemaPath())
		if err == nil {
			d.schemaContent = string(content)
			currentChecksum := migrator.ComputeSchemaChecksum(string(content))
			checksumDetails := func() string {
				return fmt.Sprintf("File checksum: %s...\nDB checksum:   %s...", currentChecksum[:16], lastMigration.SchemaChecksum[:16])
			}

			switch {
			case d.ahead():
				details := fmt.Sprintf("Current: %s, DB: %s", migrator.CodegenVersion(), lastMigration.CodegenVersion)
				if currentChecksum != lastMigration.SchemaChecksum {
					details += "\n" + checksumDetails()
				}
				report.AddCheck(CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusWarn,
					Message:  fmt.Sprintf("Database was migrated by a newer melange (%s)", lastMigration.CodegenVersion),
					Details:  details,
					FixHint:  d.upgradeHint(),
				})
			case currentChecksum != lastMigration.SchemaChecksum:
				d.schemaPending = true
				report.AddCheck(CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusWarn,
					Message:  "Schema file has changed since last migration",
					Details:  checksumDetails(),
					FixHint:  d.applyHint(ctx, "Run 'melange migrate' to apply changes"),
				})
			case !migrator.SameCodegenVersion(lastMigration.CodegenVersion, migrator.CodegenVersion()):
				d.schemaPending = true
				check := CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusWarn,
					Message:  "Codegen version has changed",
					Details:  fmt.Sprintf("Current: %s, DB: %s", migrator.CodegenVersion(), lastMigration.CodegenVersion),
				}
				// The new codegen version may generate the same SQL: migrate
				// then records the version and regenerates nothing. When it
				// applies, it may refuse to drop functions with dependents.
				if plan := d.migratePlan(ctx); plan != nil && plan.Action == migrator.PlanRecordOnly {
					check.Message = "Codegen version has changed (the generated SQL is unchanged)"
					check.FixHint = "Run 'melange migrate' to record this codegen version"
				} else {
					check.FixHint = d.applyHint(ctx, "Run 'melange migrate' to regenerate functions")
				}
				report.AddCheck(check)
			default:
				report.AddCheck(CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusPass,
					Message:  "Schema is in sync with database",
				})
			}
		}
	}

	d.checkExposureSync(ctx, report)
	return nil
}

// checkExposureSync compares the local exposure with the one the database
// was migrated with. An exposure-only change re-applies on the next migrate,
// so a difference is pending work, like a changed schema file.
func (d *Doctor) checkExposureSync(ctx context.Context, report *Report) {
	if d.opts.Exposure == nil || d.lastMigration == nil {
		return
	}
	if migrator.SameExposure(*d.opts.Exposure, d.lastMigration.Exposure) {
		report.AddCheck(CheckResult{
			Category: "Migration State",
			Name:     "exposure_sync",
			Status:   StatusPass,
			Message:  "SQL API exposure is in sync with database",
		})
		return
	}
	d.exposurePending = true
	// Narrowing drops the functions it no longer exposes, which doctor's
	// orphan check (compiled under the recorded exposure) does not list.
	report.AddCheck(CheckResult{
		Category: "Migration State",
		Name:     "exposure_sync",
		Status:   StatusWarn,
		Message:  "SQL API exposure differs from the one the database was migrated with",
		Details:  fmt.Sprintf("Local: %s\nDB:    %s", describeExposure(*d.opts.Exposure), describeExposure(d.lastMigration.Exposure)),
		FixHint:  d.applyHint(ctx, "Run 'melange migrate' to apply it"),
	})
}

// installedExposure is the exposure the installed functions were generated
// with: the recorded one, else the local one (nothing migrated yet), else
// the default.
func (d *Doctor) installedExposure() sqlgen.Exposure {
	switch {
	case d.lastMigration != nil:
		return d.lastMigration.Exposure
	case d.opts.Exposure != nil:
		return *d.opts.Exposure
	default:
		return sqlgen.Exposure{}
	}
}

// describeExposure renders an exposure for check details.
func describeExposure(e sqlgen.Exposure) string {
	if e.IsDefault() {
		return "default (every relation, every function family)"
	}
	b, err := json.Marshal(e.Normalized())
	if err != nil {
		return fmt.Sprintf("%+v", e)
	}
	return string(b)
}

// checkDeployedModel reports whether the database has recorded its authorization
// model, and — when the local schema differs — whether the pending change is
// breaking. It reuses the record checkMigrationState already fetched into
// d.lastMigration; it queries the database only to predict migrate for a
// fix hint (migratePlan).
func (d *Doctor) checkDeployedModel(ctx context.Context, report *Report) {
	// Only meaningful once a migration has been recorded.
	rec := d.lastMigration
	if rec == nil {
		return
	}

	if rec.SchemaDSL == "" {
		// A record without the model never matches, so a plain migrate
		// records it (phase 2) without re-applying.
		fixHint := "Re-run 'melange migrate' to record it (enables 'schema pull' and 'diff')"
		if d.ahead() {
			fixHint = d.upgradeHint()
		}
		report.AddCheck(CheckResult{
			Category: "Deployed Model",
			Name:     "recorded",
			Status:   StatusWarn,
			Message:  "Deployed model not recorded",
			Details:  "This database was migrated before model storage existed.",
			FixHint:  fixHint,
		})
		return
	}

	deployedTypes, err := schema.UnmarshalModel(rec.ModelJSON)
	if err != nil {
		// Degrade to a warning rather than aborting: doctor must still run
		// against the broken deployment it exists to diagnose.
		report.AddCheck(CheckResult{
			Category: "Deployed Model",
			Name:     "recorded",
			Status:   StatusWarn,
			Message:  "Deployed model could not be decoded",
			Details:  err.Error(),
			FixHint:  d.rewriteModelHint(ctx),
		})
		return
	}
	// Fall back to parsing the stored DSL when the JSON model is absent (older or
	// partial records), mirroring `melange diff`. A modular DSL is a non-parseable
	// bundle; on a parse failure deployedTypes stays empty and the drift advisory
	// below is skipped rather than run against an empty model.
	if len(deployedTypes) == 0 && rec.SchemaDSL != "" {
		if parsed, perr := parser.ParseRecordedSchemaString(rec.SchemaDSL); perr == nil {
			deployedTypes = parsed
		}
	}

	version := rec.MelangeVersion
	if version == "" {
		version = "unknown version"
	}
	report.AddCheck(CheckResult{
		Category: "Deployed Model",
		Name:     "recorded",
		Status:   StatusPass,
		Message:  fmt.Sprintf("Deployed model recorded (melange %s)", version),
	})

	// Breaking-drift advisory: classify a pending local change against the
	// deployed model. schema.Diff over the parsed types is the source of truth —
	// an empty diff means the local schema is in sync. Skip when either side has
	// no parsed model: without a previous model to compare, a removed relation
	// would misreport as additive (a false-safe advisory), so no advisory is
	// safer than a wrong one.
	if d.parsedTypes == nil || len(deployedTypes) == 0 {
		return
	}
	diff := schema.Diff(deployedTypes, d.parsedTypes)
	if diff.Empty() {
		return
	}
	additive, breaking := diff.Counts()
	if breaking == 0 {
		report.AddCheck(CheckResult{
			Category: "Deployed Model",
			Name:     "drift_safety",
			Status:   StatusPass,
			Message:  fmt.Sprintf("Local changes vs deployed are additive (%d)", additive),
		})
		return
	}
	fixHint := "Review with 'melange diff'; migrating will apply these changes"
	if d.ahead() {
		fixHint = fmt.Sprintf("Review with 'melange diff'; this melange's migrate refuses to apply them to a database a newer melange (%s) migrated", d.recordedCodegen())
	}
	report.AddCheck(CheckResult{
		Category: "Deployed Model",
		Name:     "drift_safety",
		Status:   StatusWarn,
		Message:  fmt.Sprintf("Local schema has %d breaking change(s) vs deployed", breaking),
		Details:  breakingDetails(diff),
		FixHint:  fixHint,
	})
}

// rewriteModelHint is the fix for a stored model that cannot be decoded.
// The phase 1 skip does not read model_json, so a plain migrate rewrites it
// only when the schema, exposure or codegen version changed.
func (d *Doctor) rewriteModelHint(ctx context.Context) string {
	if d.ahead() {
		return fmt.Sprintf("Use melange %s or later and re-run 'melange migrate' with it (with --force if it reports the schema unchanged)", d.recordedCodegen())
	}
	plan := d.migratePlan(ctx)
	switch {
	case plan == nil:
		return "Re-run 'melange migrate' to rewrite the stored model; if it reports the schema unchanged, run 'melange migrate --force'"
	case plan.Action == migrator.PlanSkip:
		return "Run 'melange migrate --force' to rewrite the stored model: " + plainMigrateSkips + forceCaveat(plan)
	case plan.Action == migrator.PlanRefusedAhead:
		return d.upgradeHint()
	}
	return "Re-run 'melange migrate' to rewrite the stored model"
}

// checkAdvisories warns when the deployed functions were generated by a melange
// version covered by a published security advisory. Upgrading the CLI or the Go
// module does not fix a database until it is migrated again, and no dependency
// scanner can see that gap, so this is the one place it surfaces.
func (d *Doctor) checkAdvisories(report *Report) {
	if d.opts.Advisories == nil || d.lastMigration == nil || !update.Comparable(d.lastMigration.MelangeVersion) {
		return
	}
	ver := d.lastMigration.MelangeVersion
	affected := update.Affecting(d.opts.Advisories, ver)
	if len(affected) == 0 {
		report.AddCheck(CheckResult{
			Category: "Deployed Model",
			Name:     "advisories",
			Status:   StatusPass,
			Message:  fmt.Sprintf("No published security advisories cover melange %s", ver),
		})
		return
	}
	ids := make([]string, 0, len(affected))
	lines := make([]string, 0, len(affected))
	for _, a := range affected {
		ids = append(ids, a.ID)
		lines = append(lines, fmt.Sprintf("- %s (%s, %s) %s\n  %s", a.ID, a.Severity, a.FixedIn(), a.Summary, a.URL))
	}
	report.AddCheck(CheckResult{
		Category: "Deployed Model",
		Name:     "advisories",
		Status:   StatusWarn,
		Message:  fmt.Sprintf("Deployed functions were generated by melange %s, covered by security advisories: %s", ver, strings.Join(ids, ", ")),
		Details:  strings.Join(lines, "\n"),
		FixHint:  "Upgrade to a fixed release and run 'melange migrate' to regenerate the functions (--verbose shows details)",
	})
}

// breakingDetails lists a diff's breaking changes, one bullet per line.
func breakingDetails(diff schema.SchemaDiff) string {
	summaries := diff.BreakingSummaries()
	lines := make([]string, 0, len(summaries))
	for _, s := range summaries {
		lines = append(lines, "- "+s)
	}
	return strings.Join(lines, "\n")
}

// checkGeneratedFunctions validates that expected functions exist.
func (d *Doctor) checkGeneratedFunctions(ctx context.Context, report *Report) error {
	// Get current functions from database
	currentFuncs, err := d.getCurrentFunctions(ctx)
	if err != nil {
		return fmt.Errorf("getting current functions: %w", err)
	}
	d.currentFuncs = currentFuncs

	// The expected functions follow the closure of the installed exposure;
	// under a restricted one that includes the raising helper the public
	// dispatchers call, so it is checked with them.
	analyses := d.getAnalyses()
	dispatchers := sqlgen.DispatcherFunctionNames
	if analyses != nil {
		exposure := d.installedExposure()
		d.expectedFuncs = sqlgen.CollectFunctionNamesWithOptions(analyses, sqlgen.GenerateSQLOptions{Exposure: exposure})
		if slices.Contains(d.expectedFuncs, sqlgen.UnexposedFunctionName) {
			dispatchers = append(slices.Clone(dispatchers), sqlgen.UnexposedFunctionName)
		}
		d.checkExposurePatterns(report, analyses)
	}

	currentSet := make(map[string]bool)
	for _, fn := range currentFuncs {
		currentSet[fn] = true
	}

	var missingDispatchers []string
	for _, name := range dispatchers {
		if !currentSet[name] {
			missingDispatchers = append(missingDispatchers, name)
		}
	}

	// The expected functions are this melange's; a newer one that migrated
	// the database may generate others.
	aheadNote := ""
	if d.ahead() {
		aheadNote = fmt.Sprintf("The expected functions are the ones this melange generates; melange %s may generate others.", d.recordedCodegen())
	}

	if len(missingDispatchers) > 0 {
		report.AddCheck(CheckResult{
			Category: "Generated Functions",
			Name:     "dispatchers",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Missing dispatcher functions: %s", strings.Join(missingDispatchers, ", ")),
			Details:  aheadNote,
			FixHint:  d.reinstallHint(ctx),
		})
	} else {
		report.AddCheck(CheckResult{
			Category: "Generated Functions",
			Name:     "dispatchers",
			Status:   StatusPass,
			Message:  "All dispatcher functions present",
		})
	}

	// If we have parsed types, check for expected functions
	if analyses != nil {
		expectedFuncs := d.expectedFuncs

		// Build expected set and find missing functions in a single pass.
		expectedSet := make(map[string]bool, len(expectedFuncs))
		var missing []string
		for _, fn := range expectedFuncs {
			expectedSet[fn] = true
			if !currentSet[fn] {
				missing = append(missing, fn)
			}
		}

		// Find orphan functions: installed, not expected, and recorded by
		// the last migration, which is what migrate drops. A function only
		// named like a generated one (an application's explain_invoice) is
		// not an orphan, and without a record nothing is, since migrate
		// then drops nothing.
		recorded := make(map[string]bool)
		if d.lastMigration != nil {
			for _, fn := range d.lastMigration.FunctionNames {
				recorded[fn] = true
			}
		}
		var orphans []string
		for _, fn := range currentFuncs {
			if !expectedSet[fn] && recorded[fn] {
				orphans = append(orphans, fn)
			}
		}

		if len(missing) > 0 {
			sort.Strings(missing)
			details := truncatedJoin(missing, 10)
			if aheadNote != "" {
				details += "\n" + aheadNote
			}
			report.AddCheck(CheckResult{
				Category: "Generated Functions",
				Name:     "missing",
				Status:   StatusFail,
				Message:  fmt.Sprintf("%d expected functions missing from database", len(missing)),
				Details:  details,
				FixHint:  d.reinstallHint(ctx),
			})
		} else {
			report.AddCheck(CheckResult{
				Category: "Generated Functions",
				Name:     "complete",
				Status:   StatusPass,
				Message:  fmt.Sprintf("All %d expected functions present", len(expectedFuncs)),
			})
		}

		if len(orphans) > 0 {
			sort.Strings(orphans)
			report.AddCheck(d.orphansCheck(ctx, orphans))
		}
	} else {
		// No schema to compare against, just report function count
		checkFuncs := 0
		listFuncs := 0
		for _, fn := range currentFuncs {
			if strings.HasPrefix(fn, "check_") {
				checkFuncs++
			} else if strings.HasPrefix(fn, "list_") {
				listFuncs++
			}
		}
		report.AddCheck(CheckResult{
			Category: "Generated Functions",
			Name:     "count",
			Status:   StatusPass,
			Message:  fmt.Sprintf("Found %d check functions, %d list functions", checkFuncs, listFuncs),
		})
	}

	return nil
}

// orphansCheck reports the orphans doctor found, with the fix migrate
// allows: it drops them, but refuses while other objects depend on them,
// and refuses everything when a newer melange migrated the database (whose
// functions they then are).
func (d *Doctor) orphansCheck(ctx context.Context, orphans []string) CheckResult {
	check := CheckResult{
		Category: "Generated Functions",
		Name:     "orphans",
		Status:   StatusWarn,
		Message:  fmt.Sprintf("%d orphan functions from previous schema", len(orphans)),
		Details:  truncatedJoin(orphans, 10),
		FixHint:  "Run 'melange migrate' to clean up orphans",
	}
	if d.ahead() {
		check.Message = fmt.Sprintf("%d functions recorded by a newer melange's migration that this melange does not generate", len(orphans))
		check.FixHint = d.upgradeHint()
		return check
	}
	plan := d.migratePlan(ctx)
	if plan == nil {
		return check
	}
	if plan.Action == migrator.PlanSkip {
		// The record names them although the schema, exposure and codegen
		// version are unchanged (edited by hand): only --force drops them.
		check.FixHint = "Run 'melange migrate --force' to drop them: " + plainMigrateSkips + forceCaveat(plan)
		return check
	}
	listed := make(map[string]bool, len(orphans))
	for _, fn := range orphans {
		listed[fn] = true
	}
	var lines []string
	for _, o := range plan.Orphans {
		if !listed[o.Name] {
			continue
		}
		for _, dep := range o.Dependents {
			lines = append(lines, o.Name+": "+dep)
		}
	}
	if len(lines) > 0 {
		check.Details += "\nDepended on by:\n" + strings.Join(lines, "\n")
		check.FixHint = "'melange migrate' refuses to drop them while other objects depend on them: change or drop the dependents, then run 'melange migrate' ('melange migrate --force' drops them with CASCADE, dependents included)"
	}
	return check
}

// checkExposurePatterns warns about relation patterns that match no relation
// of the local schema: usually a typo, or a relation since renamed. It checks
// the local exposure, else the recorded one.
func (d *Doctor) checkExposurePatterns(report *Report, analyses []sqlgen.RelationAnalysis) {
	exposure := d.installedExposure()
	if d.opts.Exposure != nil {
		exposure = *d.opts.Exposure
	}
	unmatched := exposure.UnmatchedPatterns(analyses)
	if len(unmatched) == 0 {
		return
	}
	report.AddCheck(CheckResult{
		Category: "Generated Functions",
		Name:     "exposure_patterns",
		Status:   StatusWarn,
		Message:  fmt.Sprintf("%d SQL API exposure pattern(s) match no relation in the schema", len(unmatched)),
		Details:  truncatedJoin(unmatched, 10),
		FixHint:  "Check the exposure's relation include/exclude patterns for typos or renamed relations",
	})
}

// checkTuplesSource validates the melange_tuples view/table.
func (d *Doctor) checkTuplesSource(ctx context.Context, report *Report) error {
	info, err := d.getTuplesInfo(ctx)
	if err != nil {
		return fmt.Errorf("getting tuples info: %w", err)
	}
	d.tuplesInfo = info

	if !info.Exists {
		report.AddCheck(CheckResult{
			Category: "Tuples Source",
			Name:     "exists",
			Status:   StatusFail,
			Message:  "melange_tuples does not exist",
			FixHint:  "Create a view/table named melange_tuples over your domain tables",
		})
		return nil
	}

	report.AddCheck(CheckResult{
		Category: "Tuples Source",
		Name:     "exists",
		Status:   StatusPass,
		Message:  fmt.Sprintf("melange_tuples exists (%s)", info.RelKindStr),
	})

	// Check required columns
	requiredCols := []string{"object_type", "object_id", "relation", "subject_type", "subject_id"}
	colSet := make(map[string]bool)
	for _, col := range info.Columns {
		colSet[col] = true
	}

	var missingCols []string
	for _, col := range requiredCols {
		if !colSet[col] {
			missingCols = append(missingCols, col)
		}
	}

	if len(missingCols) > 0 {
		report.AddCheck(CheckResult{
			Category: "Tuples Source",
			Name:     "columns",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Missing required columns: %s", strings.Join(missingCols, ", ")),
			Details:  fmt.Sprintf("Found columns: %s", strings.Join(info.Columns, ", ")),
			FixHint:  "Update melange_tuples to include all required columns",
		})
	} else {
		report.AddCheck(CheckResult{
			Category: "Tuples Source",
			Name:     "columns",
			Status:   StatusPass,
			Message:  "All required columns present",
		})
	}

	// For materialized views, suggest refresh consideration
	if info.RelKind == "m" {
		report.AddCheck(CheckResult{
			Category: "Tuples Source",
			Name:     "refresh",
			Status:   StatusWarn,
			Message:  "melange_tuples is a materialized view",
			Details:  "Materialized views require manual refresh to see data changes",
			FixHint:  "Ensure you have a refresh strategy (e.g., REFRESH MATERIALIZED VIEW CONCURRENTLY)",
		})
	}

	return nil
}

// checkDataHealth validates the data in melange_tuples.
func (d *Doctor) checkDataHealth(ctx context.Context, report *Report) error {
	if d.tuplesInfo == nil || !d.tuplesInfo.Exists {
		return nil // Already reported in tuples check
	}

	// Check if there's any data
	var count int64
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, d.prefixIdent("melange_tuples"))).Scan(&count)
	if err != nil {
		// May fail if columns are wrong - just skip
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "query",
			Status:   StatusWarn,
			Message:  "Could not query melange_tuples",
			Details:  err.Error(),
		})
		return nil
	}

	if count == 0 {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "data",
			Status:   StatusWarn,
			Message:  "melange_tuples is empty",
			Details:  "No authorization data to evaluate permissions against",
		})
	} else {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "data",
			Status:   StatusPass,
			Message:  fmt.Sprintf("melange_tuples contains %d tuples", count),
		})
	}

	// If we have a schema, validate tuple data against the model.
	if d.parsedTypes != nil && count > 0 {
		if err := d.validateTupleData(ctx, report); err != nil {
			return fmt.Errorf("validating tuple data: %w", err)
		}
	}

	return nil
}

// tupleSignature holds an aggregated (object_type, relation, subject_type) group
// with its tuple count, used for classifying orphaned tuples.
type tupleSignature struct {
	objectType  string
	relation    string
	subjectType string
	count       int64
}

// validateTupleData checks all tuples against the schema model and reports
// unknown object types, relations, subject types, and invalid subject type
// assignments. This replaces the earlier sampling-based check with a
// comprehensive GROUP BY aggregation.
func (d *Doctor) validateTupleData(ctx context.Context, report *Report) error {
	// Build validity maps from parsed schema.
	validTypes := make(map[string]bool)
	validRelations := make(map[string]map[string]bool)
	// allowedSubjects: objectType -> relation -> set of allowed subject types.
	// Only populated for relations with explicit SubjectTypeRefs (direct assignments).
	allowedSubjects := make(map[string]map[string]map[string]bool)

	for _, t := range d.parsedTypes {
		validTypes[t.Name] = true
		rels := make(map[string]bool, len(t.Relations))
		for _, r := range t.Relations {
			rels[r.Name] = true
			if len(r.SubjectTypeRefs) > 0 {
				allowed := make(map[string]bool, len(r.SubjectTypeRefs))
				for _, ref := range r.SubjectTypeRefs {
					allowed[ref.Type] = true
				}
				if allowedSubjects[t.Name] == nil {
					allowedSubjects[t.Name] = make(map[string]map[string]bool)
				}
				allowedSubjects[t.Name][r.Name] = allowed
			}
		}
		validRelations[t.Name] = rels
	}

	// Single aggregation query returns all distinct tuple signatures with counts.
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(
		`
			SELECT object_type, relation, subject_type, COUNT(*) AS cnt
			FROM %s
			GROUP BY object_type, relation, subject_type
		`,
		d.prefixIdent("melange_tuples"),
	))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var sigs []tupleSignature
	for rows.Next() {
		var s tupleSignature
		if err := rows.Scan(&s.objectType, &s.relation, &s.subjectType, &s.count); err != nil {
			return err
		}
		sigs = append(sigs, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	findings := classifyTupleSignatures(sigs, validTypes, validRelations, allowedSubjects)
	emitTupleFindings(report, findings)
	return nil
}

// tupleFindings holds categorized orphaned tuple data.
type tupleFindings struct {
	unknownObjectTypes []string // "widget (100 tuples)"
	unknownRelations   []string // "document:publish (42 tuples)"
	unknownSubjects    []string // "device (30 tuples)"
	invalidSubjects    []string // "organization:member subject_type=team (5 tuples)"

	unknownObjectTypeCount int64
	unknownRelationCount   int64
	unknownSubjectCount    int64
	invalidSubjectCount    int64
}

// classifyTupleSignatures categorizes aggregated tuple signatures against the
// schema validity maps. This is a pure function for testability.
func classifyTupleSignatures(
	sigs []tupleSignature,
	validTypes map[string]bool,
	validRelations map[string]map[string]bool,
	allowedSubjects map[string]map[string]map[string]bool,
) tupleFindings {
	var f tupleFindings

	// Accumulate counts per unique key. Multiple signatures can share the same
	// unknown value (e.g., same unknown object_type with different relations).
	objTypeCounts := make(map[string]int64)
	relationCounts := make(map[string]int64)
	subjectCounts := make(map[string]int64)

	for _, s := range sigs {
		// Unknown object type.
		if !validTypes[s.objectType] {
			objTypeCounts[s.objectType] += s.count
			f.unknownObjectTypeCount += s.count
			continue // Skip further checks — relation/subject are meaningless.
		}

		// Unknown relation for this object type.
		if !validRelations[s.objectType][s.relation] {
			key := s.objectType + ":" + s.relation
			relationCounts[key] += s.count
			f.unknownRelationCount += s.count
			continue // Skip subject check — relation is already invalid.
		}

		// Unknown subject type.
		if !validTypes[s.subjectType] {
			subjectCounts[s.subjectType] += s.count
			f.unknownSubjectCount += s.count
			continue
		}

		// Invalid subject type for this relation (only when relation has explicit constraints).
		if allowed := allowedSubjects[s.objectType][s.relation]; allowed != nil {
			if !allowed[s.subjectType] {
				key := fmt.Sprintf("%s:%s subject_type=%s", s.objectType, s.relation, s.subjectType)
				f.invalidSubjects = append(f.invalidSubjects, fmt.Sprintf("%s (%d tuples)", key, s.count))
				f.invalidSubjectCount += s.count
			}
		}
	}

	// Build display strings from accumulated counts.
	for val, count := range objTypeCounts {
		f.unknownObjectTypes = append(f.unknownObjectTypes, fmt.Sprintf("%s (%d tuples)", val, count))
	}
	for val, count := range relationCounts {
		f.unknownRelations = append(f.unknownRelations, fmt.Sprintf("%s (%d tuples)", val, count))
	}
	for val, count := range subjectCounts {
		f.unknownSubjects = append(f.unknownSubjects, fmt.Sprintf("%s (%d tuples)", val, count))
	}

	return f
}

// emitTupleFindings adds check results to the report based on classified findings.
func emitTupleFindings(report *Report, f tupleFindings) {
	anyIssue := false

	if len(f.unknownObjectTypes) > 0 {
		anyIssue = true
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "unknown_object_types",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d unknown object types affecting %d tuples", len(f.unknownObjectTypes), f.unknownObjectTypeCount),
			Details:  truncatedJoin(f.unknownObjectTypes, 10),
			FixHint:  "Update melange_tuples to exclude rows with unknown object types, or add missing types to schema.fga",
		})
	}

	if len(f.unknownRelations) > 0 {
		anyIssue = true
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "unknown_relations",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d unknown relations affecting %d tuples", len(f.unknownRelations), f.unknownRelationCount),
			Details:  truncatedJoin(f.unknownRelations, 10),
			FixHint:  "Update melange_tuples to exclude rows with unknown relations, or add missing relations to schema.fga",
		})
	}

	if len(f.unknownSubjects) > 0 {
		anyIssue = true
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "unknown_subject_types",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d unknown subject types affecting %d tuples", len(f.unknownSubjects), f.unknownSubjectCount),
			Details:  truncatedJoin(f.unknownSubjects, 10),
			FixHint:  "Update melange_tuples to exclude rows with unknown subject types, or add missing types to schema.fga",
		})
	}

	if len(f.invalidSubjects) > 0 {
		anyIssue = true
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "invalid_subject_types",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d invalid subject type assignments affecting %d tuples", len(f.invalidSubjects), f.invalidSubjectCount),
			Details:  truncatedJoin(f.invalidSubjects, 10),
			FixHint:  "These tuples have subject types not allowed by the relation definition in schema.fga",
		})
	}

	if !anyIssue {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "valid",
			Status:   StatusPass,
			Message:  "All tuples reference valid types, relations, and subject types",
		})
	}
}

// getCurrentFunctions returns all melange-generated function names.
func (d *Doctor) getCurrentFunctions(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(
		`
			SELECT p.proname
			FROM pg_proc p
			JOIN pg_namespace n ON p.pronamespace = n.oid
			WHERE n.nspname = %s
			AND %s
		`,
		d.postgresSchema(), migrator.GeneratedFunctionFilter,
	))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var functions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		functions = append(functions, name)
	}
	return functions, rows.Err()
}

// getTuplesInfo retrieves information about the melange_tuples relation.
func (d *Doctor) getTuplesInfo(ctx context.Context) (*TuplesInfo, error) {
	info := &TuplesInfo{RowCount: -1}

	// Check if relation exists and get type
	var relKind string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`
			SELECT c.relkind
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'melange_tuples'
			AND n.nspname = %s
			AND c.relkind IN ('r', 'v', 'm')
		`,
		d.postgresSchema(),
	)).Scan(&relKind)

	if err == sql.ErrNoRows {
		return info, nil
	}
	if err != nil {
		return nil, err
	}

	info.Exists = true
	info.RelKind = relKind
	switch relKind {
	case "r":
		info.RelKindStr = "table"
	case "v":
		info.RelKindStr = "view"
	case "m":
		info.RelKindStr = "materialized view"
	}

	// Get columns
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(
		`
			SELECT a.attname
			FROM pg_attribute a
			JOIN pg_class c ON a.attrelid = c.oid
			JOIN pg_namespace n ON c.relnamespace = n.oid
			WHERE c.relname = 'melange_tuples'
			AND n.nspname = %s
			AND a.attnum > 0
			AND NOT a.attisdropped
			ORDER BY a.attnum
		`,
		d.postgresSchema(),
	))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return nil, err
		}
		info.Columns = append(info.Columns, col)
	}

	return info, rows.Err()
}

// truncatedJoin joins items with newlines, truncating after maxItems with a
// summary of the remainder. Returns all items when maxItems <= 0.
func truncatedJoin(items []string, maxItems int) string {
	if maxItems <= 0 || len(items) <= maxItems {
		return strings.Join(items, "\n")
	}
	return strings.Join(items[:maxItems], "\n") + fmt.Sprintf("\n... and %d more", len(items)-maxItems)
}

func (d *Doctor) prefixIdent(identifier string) string {
	return sqldsl.PrefixIdent(identifier, d.databaseSchema)
}

func (d *Doctor) postgresSchema() string {
	return sqldsl.PostgresSchemaExpr(d.databaseSchema)
}
