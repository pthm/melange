package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/sqlapi"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
)

// ClientCheck is the generated client the Generated Client category checks:
// its files against what melange.yaml generates, and the names it emits
// against the database.
type ClientCheck struct {
	Runtime string
	// GenerateError is why the client could not be generated from
	// melange.yaml; nothing else is then checked.
	GenerateError string
	// Output is the directory the client is generated into, "" when none
	// is configured.
	Output string
	// Drift is the generated files that differ from a fresh generation.
	// FilesError is why they could not be compared, and FilesSkipped why
	// they were not (no output directory, as in a production image).
	Drift        []clientgen.FileDrift
	FilesError   string
	FilesSkipped string
	// Names are the names the client generated from melange.yaml emits.
	Names sqlapi.ClientNames
	// Local is the exposure the client is generated under: melange.yaml's
	// sql section, without the doctor command's exposure flags.
	Local sqlgen.Exposure
	// Strict reports a name the database does not answer for as a failure.
	// By default it is a warning, since the working tree may be ahead of
	// the database.
	Strict bool
}

// clientCategory is the category of the client checks.
const clientCategory = "Generated Client"

// checkGeneratedClient compares the configured client with the database.
func (d *Doctor) checkGeneratedClient(ctx context.Context, report *Report) {
	cc := d.opts.Client
	if cc == nil {
		return
	}
	if cc.GenerateError != "" {
		emitClientFindings(report, cc, nil, nil)
		return
	}
	cr, err := d.CompareClient(ctx, cc.Names, cc.Local)
	emitClientFindings(report, cc, cr, err)
}

// CompareClient checks the names a generated client emits against the
// database's last migration (sqlapi.CompareClient), with fix hints that
// follow what a plain migrate would do. Outside Run, it reads the local
// schema and the migration record itself; Options.Exposure should then be
// local, the exposure the hints' migrate applies.
func (d *Doctor) CompareClient(ctx context.Context, names sqlapi.ClientNames, local sqlgen.Exposure) (*sqlapi.ClientReport, error) {
	if !d.stateRead {
		if err := d.readClientState(ctx); err != nil {
			return nil, err
		}
	}
	// pg_proc is read by schema name; an unset one is the connection's
	// current schema, as for doctor's other queries.
	databaseSchema := d.databaseSchema
	if databaseSchema == "" {
		if err := d.db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&databaseSchema); err != nil {
			return nil, fmt.Errorf("reading the current schema: %w", err)
		}
	}
	// A database that records no model is described with the client's own
	// model, which doctor's --schema may not be.
	types := names.Types
	if types == nil {
		types = d.parsedTypes
	}
	r, rec, err := sqlapi.Deployed(ctx, d.db, databaseSchema, &sqlapi.Fallback{Types: types, Exposure: local}, sqlapi.CheckOptions{})
	if err != nil {
		return nil, err
	}
	cr := sqlapi.CompareClient(r, rec, names, local)
	hints := d.clientView(names, local)
	for i := range cr.Findings {
		cr.Findings[i].Fix = hints.clientFix(ctx, cr.Findings[i])
	}
	return cr, nil
}

// clientView is the doctor whose migrate hints fit the client's findings:
// one that predicts a migrate of the client's schema under its exposure.
// That is d itself unless doctor runs with another schema (--schema) or
// exposure flags; the copy shares d's database and migration record.
func (d *Doctor) clientView(names sqlapi.ClientNames, local sqlgen.Exposure) *Doctor {
	if names.Types == nil || (names.SchemaContent == d.schemaContent && d.opts.Exposure != nil && migrator.SameExposure(*d.opts.Exposure, local)) {
		return d
	}
	v := &Doctor{
		db:             d.db,
		databaseSchema: d.databaseSchema,
		opts:           Options{Exposure: &local},
		parsedTypes:    names.Types,
		schemaContent:  names.SchemaContent,
		lastMigration:  d.lastMigration,
		stateRead:      true,
	}
	v.setPending()
	return v
}

// readClientState reads what Run's first checks cache and the fix hints
// use: the local schema, its content and the last migration record.
func (d *Doctor) readClientState(ctx context.Context) error {
	d.stateRead = true
	m := migrator.NewMigrator(d.db, d.schemaPath)
	m.SetDatabaseSchema(d.databaseSchema)
	if d.schemaPath != "" {
		if types, err := parser.ParseSchema(m.SchemaPath()); err == nil {
			d.parsedTypes = types
		}
		if content, err := parser.ReadSchemaContent(m.SchemaPath()); err == nil {
			d.schemaContent = string(content)
		}
	}
	rec, err := m.GetLastMigration(ctx)
	if err != nil {
		return fmt.Errorf("reading the migration record: %w", err)
	}
	d.lastMigration = rec
	d.setPending()
	return nil
}

// setPending records whether a migrate of the local schema and exposure
// would apply changes against the last migration, as schema_sync and
// exposure_sync report them in Run.
func (d *Doctor) setPending() {
	rec := d.lastMigration
	if rec == nil {
		return
	}
	d.schemaPending = (d.schemaContent != "" && migrator.ComputeSchemaChecksum(d.schemaContent) != rec.SchemaChecksum) ||
		!migrator.SameCodegenVersion(rec.CodegenVersion, migrator.CodegenVersion())
	d.exposurePending = d.opts.Exposure != nil && !migrator.SameExposure(*d.opts.Exposure, rec.Exposure)
}

// clientFix words a finding's fix with doctor's migrate hints, which say
// whether a plain migrate applies it, skips, or is refused.
func (d *Doctor) clientFix(ctx context.Context, f sqlapi.ClientFinding) string {
	switch f.Remedy {
	case sqlapi.RemedyMigrate:
		hint := d.applyHint(ctx, "Run 'melange migrate' to apply the local schema and melange.yaml's sql section")
		if d.lastMigration == nil {
			// No record: the database may be built from generated files.
			hint += "; for a database built from 'melange generate migration' files, regenerate them with melange.yaml's sql section and apply them"
		}
		return hint
	case sqlapi.RemedyReinstall:
		return d.reinstallHint(ctx)
	}
	if f.Fix == "" {
		return ""
	}
	return strings.ToUpper(f.Fix[:1]) + f.Fix[1:]
}

// emitClientFindings adds the Generated Client checks: client_file for the
// files, client_functions and client_relations for the names. cr is nil
// when the comparison failed with err.
func emitClientFindings(report *Report, cc *ClientCheck, cr *sqlapi.ClientReport, err error) {
	where := cc.Runtime + " client"
	if cc.Output != "" {
		where += " in " + cc.Output
	}
	switch {
	case cc.GenerateError != "":
		report.AddCheck(CheckResult{
			Category: clientCategory,
			Name:     "client_file",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Could not generate the %s from melange.yaml", where),
			Details:  cc.GenerateError,
			FixHint:  "Run 'melange generate client' to see the error",
		})
		return
	case cc.FilesError != "":
		report.AddCheck(CheckResult{
			Category: clientCategory,
			Name:     "client_file",
			Status:   StatusFail,
			Message:  fmt.Sprintf("Could not compare the %s with what melange.yaml generates", where),
			Details:  cc.FilesError,
			FixHint:  "Run 'melange generate client --check' to see the error",
		})
	case len(cc.Drift) > 0:
		lines := make([]string, len(cc.Drift))
		for i, f := range cc.Drift {
			lines[i] = f.File + ": " + f.Summary()
		}
		report.AddCheck(CheckResult{
			Category: clientCategory,
			Name:     "client_file",
			Status:   StatusWarn,
			Message:  fmt.Sprintf("%d generated client files differ from what melange.yaml generates (%s)", len(cc.Drift), where),
			Details:  truncatedJoin(lines, 10),
			FixHint:  "Run 'melange generate client' to regenerate them, and keep the relation selection in melange.yaml (generate.client.relations)",
		})
	case cc.FilesSkipped == "":
		report.AddCheck(CheckResult{
			Category: clientCategory,
			Name:     "client_file",
			Status:   StatusPass,
			Message:  fmt.Sprintf("Generated client files match melange.yaml (%s)", where),
		})
	}

	var notes []string
	if cc.FilesSkipped != "" {
		notes = append(notes, "Generated files not compared: "+cc.FilesSkipped)
	}
	if len(cc.Drift) > 0 {
		notes = append(notes, "The names checked are the ones melange.yaml generates, not the ones in the files that differ.")
	}
	if cr == nil {
		report.AddCheck(CheckResult{
			Category: clientCategory,
			Name:     "client_functions",
			Status:   StatusWarn,
			Message:  "Could not compare the client's names with the database",
			Details:  strings.Join(append(notes, err.Error()), "\n"),
		})
		return
	}
	notes = append(notes, cr.Notes...)

	var functions, relations []sqlapi.ClientFinding
	for _, f := range cr.Findings {
		if f.Function != "" || f.Code == "family_off" {
			functions = append(functions, f)
		} else {
			relations = append(relations, f)
		}
	}
	report.AddCheck(clientNamesCheck(cc, "client_functions", functions, notes,
		fmt.Sprintf("All %d accessible function names the client uses are installed", cr.FunctionsChecked),
		"accessible function names the client uses are not installed as it expects"))
	report.AddCheck(clientNamesCheck(cc, "client_relations", relations, nil,
		fmt.Sprintf("All %d relations the client has constants for are exposed", cr.PairsChecked),
		"relations the client has constants for are not exposed as it expects"))
}

// clientNamesCheck is the check for one kind of name: Pass with no error or
// warning, else Warn, or Fail for an error under Strict. Details list the
// findings, info included, then the notes.
func clientNamesCheck(cc *ClientCheck, name string, findings []sqlapi.ClientFinding, notes []string, pass, problem string) CheckResult {
	check := CheckResult{Category: clientCategory, Name: name, Status: StatusPass, Message: pass}
	var lines []string
	problems := 0
	for _, f := range findings {
		lines = append(lines, fmt.Sprintf("%s (%s): %s", f.Code, f.Severity, f.Message))
		if f.Severity == sqlapi.SeverityInfo {
			continue
		}
		if problems == 0 {
			check.FixHint = f.Fix
		}
		problems++
		if check.Status == StatusPass {
			check.Status = StatusWarn
		}
		if f.Severity == sqlapi.SeverityError && cc.Strict {
			check.Status = StatusFail
		}
	}
	if problems > 0 {
		check.Message = fmt.Sprintf("%d %s", problems, problem)
	}
	if len(lines) > 0 {
		notes = append([]string{truncatedJoin(lines, 10)}, notes...)
	}
	check.Details = strings.Join(notes, "\n")
	return check
}
