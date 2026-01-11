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
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
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

func (s Status) String() string {
	switch s {
	case StatusPass:
		return "pass"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	default:
		return "unknown"
	}
}

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

// CheckResult represents the outcome of a single health check.
type CheckResult struct {
	// Category groups related checks (e.g., "schema", "functions", "tuples").
	Category string

	// Name is a short identifier for the check.
	Name string

	// Status is the check outcome.
	Status Status

	// Message is a human-readable description of the result.
	Message string

	// Details provides additional information for verbose output.
	Details string

	// FixHint suggests how to resolve issues.
	FixHint string
}

// Report contains all health check results.
type Report struct {
	Checks []CheckResult

	// Summary counts.
	Passed   int
	Warnings int
	Errors   int
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

// Print writes the report to the given writer.
func (r *Report) Print(w io.Writer, verbose bool) {
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
		_, _ = fmt.Fprintf(w, "\n%s\n", cat)
		for _, check := range categories[cat] {
			_, _ = fmt.Fprintf(w, "  %s %s\n", check.Status.Symbol(), check.Message)
			if verbose && check.Details != "" {
				// Indent details
				for _, line := range strings.Split(check.Details, "\n") {
					_, _ = fmt.Fprintf(w, "      %s\n", line)
				}
			}
			if check.Status != StatusPass && check.FixHint != "" {
				_, _ = fmt.Fprintf(w, "      Fix: %s\n", check.FixHint)
			}
		}
	}

	// Print summary
	_, _ = fmt.Fprintf(w, "\nSummary: %d passed, %d warnings, %d errors\n",
		r.Passed, r.Warnings, r.Errors)
}

// HasErrors returns true if any check failed.
func (r *Report) HasErrors() bool {
	return r.Errors > 0
}

// Doctor performs health checks on the melange authorization infrastructure.
type Doctor struct {
	db         *sql.DB
	schemaPath string

	// Cached data from checks (populated during Run)
	parsedTypes   []schema.TypeDefinition
	schemaContent string
	lastMigration *migrator.MigrationRecord
	expectedFuncs []string
	currentFuncs  []string
	tuplesInfo    *TuplesInfo
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
func New(db *sql.DB, schemaPath string) *Doctor {
	return &Doctor{
		db:         db,
		schemaPath: schemaPath,
	}
}

// Run executes all health checks and returns a report.
func (d *Doctor) Run(ctx context.Context) (*Report, error) {
	report := &Report{}

	// Run checks in order, building up cached data
	d.checkSchemaFile(report)
	if err := d.checkMigrationState(ctx, report); err != nil {
		return nil, fmt.Errorf("checking migration state: %w", err)
	}
	if err := d.checkGeneratedFunctions(ctx, report); err != nil {
		return nil, fmt.Errorf("checking generated functions: %w", err)
	}
	if err := d.checkTuplesSource(ctx, report); err != nil {
		return nil, fmt.Errorf("checking tuples source: %w", err)
	}
	if err := d.checkDataHealth(ctx, report); err != nil {
		return nil, fmt.Errorf("checking data health: %w", err)
	}

	return report, nil
}

// checkSchemaFile validates the schema file exists and is valid.
func (d *Doctor) checkSchemaFile(report *Report) {
	m := migrator.NewMigrator(d.db, d.schemaPath)
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
			Message:  "Schema has syntax errors",
			Details:  err.Error(),
			FixHint:  "Run 'fga model validate' to see detailed errors",
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

	// Check if migrations table exists
	var tableExists bool
	err := d.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'melange_migrations'
			AND n.nspname = current_schema()
		)
	`).Scan(&tableExists)
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
		// Read schema content for checksum
		schemaPath := m.SchemaPath()
		content, err := readFileContent(schemaPath)
		if err == nil {
			d.schemaContent = content
			currentChecksum := migrator.ComputeSchemaChecksum(content)

			switch {
			case currentChecksum != lastMigration.SchemaChecksum:
				report.AddCheck(CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusWarn,
					Message:  "Schema file has changed since last migration",
					Details:  fmt.Sprintf("File checksum: %s...\nDB checksum:   %s...", currentChecksum[:16], lastMigration.SchemaChecksum[:16]),
					FixHint:  "Run 'melange migrate' to apply changes",
				})
			case lastMigration.CodegenVersion != migrator.CodegenVersion:
				report.AddCheck(CheckResult{
					Category: "Migration State",
					Name:     "schema_sync",
					Status:   StatusWarn,
					Message:  "Codegen version has changed",
					Details:  fmt.Sprintf("Current: %s, DB: %s", migrator.CodegenVersion, lastMigration.CodegenVersion),
					FixHint:  "Run 'melange migrate' to regenerate functions",
				})
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

	return nil
}

// checkGeneratedFunctions validates that expected functions exist.
func (d *Doctor) checkGeneratedFunctions(ctx context.Context, report *Report) error {
	// Get current functions from database
	currentFuncs, err := d.getCurrentFunctions(ctx)
	if err != nil {
		return fmt.Errorf("getting current functions: %w", err)
	}
	d.currentFuncs = currentFuncs

	// Check dispatchers exist
	dispatchers := []string{
		"check_permission",
		"check_permission_internal",
		"check_permission_no_wildcard",
		"check_permission_no_wildcard_internal",
		"list_accessible_objects",
		"list_accessible_subjects",
	}

	currentSet := make(map[string]bool)
	for _, fn := range currentFuncs {
		currentSet[fn] = true
	}

	missingDispatchers := []string{}
	for _, d := range dispatchers {
		if !currentSet[d] {
			missingDispatchers = append(missingDispatchers, d)
		}
	}

	if len(missingDispatchers) > 0 {
		report.AddCheck(CheckResult{
			Category: "Generated Functions",
			Name:     "dispatchers",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Missing dispatcher functions: %s", strings.Join(missingDispatchers, ", ")),
			FixHint:  "Run 'melange migrate' to create functions",
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
	if d.parsedTypes != nil {
		closureRows := schema.ComputeRelationClosure(d.parsedTypes)
		analyses := sqlgen.AnalyzeRelations(d.parsedTypes, closureRows)
		analyses = sqlgen.ComputeCanGenerate(analyses)
		expectedFuncs := sqlgen.CollectFunctionNames(analyses)
		d.expectedFuncs = expectedFuncs

		expectedSet := make(map[string]bool)
		for _, fn := range expectedFuncs {
			expectedSet[fn] = true
		}

		// Find missing functions
		var missing []string
		for _, fn := range expectedFuncs {
			if !currentSet[fn] {
				missing = append(missing, fn)
			}
		}

		// Find orphan functions (in DB but not expected)
		var orphans []string
		for _, fn := range currentFuncs {
			if !expectedSet[fn] {
				orphans = append(orphans, fn)
			}
		}

		if len(missing) > 0 {
			sort.Strings(missing)
			details := strings.Join(missing, "\n")
			if len(missing) > 10 {
				details = strings.Join(missing[:10], "\n") + fmt.Sprintf("\n... and %d more", len(missing)-10)
			}
			report.AddCheck(CheckResult{
				Category: "Generated Functions",
				Name:     "missing",
				Status:   StatusFail,
				Message:  fmt.Sprintf("%d expected functions missing from database", len(missing)),
				Details:  details,
				FixHint:  "Run 'melange migrate' to create functions",
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
			details := strings.Join(orphans, "\n")
			if len(orphans) > 10 {
				details = strings.Join(orphans[:10], "\n") + fmt.Sprintf("\n... and %d more", len(orphans)-10)
			}
			report.AddCheck(CheckResult{
				Category: "Generated Functions",
				Name:     "orphans",
				Status:   StatusWarn,
				Message:  fmt.Sprintf("%d orphan functions from previous schema", len(orphans)),
				Details:  details,
				FixHint:  "Run 'melange migrate' to clean up orphans",
			})
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
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM melange_tuples`).Scan(&count)
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

	// If we have a schema, validate sample tuples
	if d.parsedTypes != nil && count > 0 {
		if err := d.validateSampleTuples(ctx, report); err != nil {
			return fmt.Errorf("validating sample tuples: %w", err)
		}
	}

	return nil
}

// validateSampleTuples checks that tuples reference valid types and relations.
func (d *Doctor) validateSampleTuples(ctx context.Context, report *Report) error {
	// Build valid type and relation sets
	validTypes := make(map[string]bool)
	validRelations := make(map[string]map[string]bool) // type -> relation -> bool
	for _, t := range d.parsedTypes {
		validTypes[t.Name] = true
		validRelations[t.Name] = make(map[string]bool)
		for _, r := range t.Relations {
			validRelations[t.Name][r.Name] = true
		}
	}

	// Sample distinct types and relations from tuples
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT object_type, relation
		FROM melange_tuples
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	var unknownTypes []string
	var unknownRelations []string
	seenTypes := make(map[string]bool)
	seenRelations := make(map[string]bool)

	for rows.Next() {
		var objType, relation string
		if err := rows.Scan(&objType, &relation); err != nil {
			return err
		}

		if !validTypes[objType] && !seenTypes[objType] {
			seenTypes[objType] = true
			unknownTypes = append(unknownTypes, objType)
		}

		key := objType + ":" + relation
		if validTypes[objType] && !validRelations[objType][relation] && !seenRelations[key] {
			seenRelations[key] = true
			unknownRelations = append(unknownRelations, key)
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if len(unknownTypes) > 0 {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "types",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d unknown object types in tuples", len(unknownTypes)),
			Details:  strings.Join(unknownTypes, ", "),
		})
	}

	if len(unknownRelations) > 0 {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "relations",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("Found %d unknown relations in tuples", len(unknownRelations)),
			Details:  strings.Join(unknownRelations, ", "),
		})
	}

	if len(unknownTypes) == 0 && len(unknownRelations) == 0 {
		report.AddCheck(CheckResult{
			Category: "Data Health",
			Name:     "valid",
			Status:   StatusPass,
			Message:  "All sampled tuples reference valid types and relations",
		})
	}

	return nil
}

// getCurrentFunctions returns all melange-generated function names.
func (d *Doctor) getCurrentFunctions(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = current_schema()
		AND (
			p.proname LIKE 'check_%'
			OR p.proname LIKE 'list_%'
		)
	`)
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
	err := d.db.QueryRowContext(ctx, `
		SELECT c.relkind
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'melange_tuples'
		AND n.nspname = current_schema()
		AND c.relkind IN ('r', 'v', 'm')
	`).Scan(&relKind)

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
	rows, err := d.db.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON a.attrelid = c.oid
		JOIN pg_namespace n ON c.relnamespace = n.oid
		WHERE c.relname = 'melange_tuples'
		AND n.nspname = current_schema()
		AND a.attnum > 0
		AND NOT a.attisdropped
		ORDER BY a.attnum
	`)
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

// readFileContent reads file content as string.
func readFileContent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
