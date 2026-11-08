package sqlapi

import (
	"cmp"
	"context"
	"crypto/md5" //nolint:gosec // matches PostgreSQL's md5(prosrc); not a security boundary
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// The functions report: the SQL API catalog (sqlgen.BuildCatalog) of a
// model and exposure, and, against a database, what of it is installed.
// melange functions and studio's SQL API page print it.

// Status is how an expected function compares with the database.
type Status string

const (
	// StatusNotChecked: the report was built without a database.
	StatusNotChecked Status = "not_checked"
	// StatusInstalled: installed with the expected arguments and, when
	// bodies are compared, the body this melange generates.
	StatusInstalled Status = "installed"
	// StatusMissing: no function of that name in the schema.
	StatusMissing Status = "missing"
	// StatusSignatureDiffers: installed, but none with the expected
	// arguments.
	StatusSignatureDiffers Status = "signature_differs"
	// StatusBodyChanged: the installed body differs from the one migrate
	// generated and recorded, so it was changed in the database.
	StatusBodyChanged Status = "body_changed"
	// StatusCodegenDiffers: the installed body is the one the last
	// migration generated, and this melange generates another: the next
	// migrate replaces it.
	StatusCodegenDiffers Status = "codegen_differs"
)

// Report is a model's SQL API and, when checked, its state in a database.
type Report struct {
	// Source is "database" (the deployed model, checked against pg_proc) or
	// "file" (a schema file and the local exposure, not checked).
	Source string `json:"source"`
	// SchemaFile is the schema file the model was read from, when it was.
	SchemaFile     string `json:"schema_file,omitempty"`
	DatabaseSchema string `json:"database_schema"`
	// MelangeVersion is this melange's codegen version, which the names,
	// signatures and bodies are computed with.
	MelangeVersion string `json:"melange_version"`
	// DeployedCodegenVersion is the codegen version of the last migration;
	// CodegenSkew is set when it differs from MelangeVersion, and
	// CodegenAhead when it is newer, so this melange's migrate refuses to
	// replace or drop anything.
	DeployedCodegenVersion string `json:"deployed_codegen_version,omitempty"`
	CodegenSkew            bool   `json:"codegen_skew"`
	CodegenAhead           bool   `json:"codegen_ahead"`
	// Checked reports whether statuses come from the database.
	Checked bool `json:"checked"`
	// BodiesChecked reports whether bodies were compared too.
	BodiesChecked bool            `json:"bodies_checked"`
	Exposure      sqlgen.Exposure `json:"exposure"`
	Summary       Summary         `json:"summary"`
	// Relations are in model order.
	Relations   []Relation `json:"relations"`
	Dispatchers []Function `json:"dispatchers"`
	// Orphans are installed functions the last migration recorded that this
	// melange does not generate for the model; migrate drops them. When
	// CodegenAhead is set they are the newer melange's functions, which this
	// melange's migrate leaves alone.
	Orphans []InstalledFunction `json:"orphans,omitempty"`
	// Unmanaged are installed functions named like generated ones that no
	// migration recorded, such as an application's own explain_invoice.
	// Migrate never drops them.
	Unmanaged  []InstalledFunction    `json:"unmanaged,omitempty"`
	Collisions []sqlgen.NameCollision `json:"collisions,omitempty"`
	Notes      []string               `json:"notes,omitempty"`

	types []schema.TypeDefinition
}

// Relation is a relation of the model and its functions.
type Relation struct {
	Type         string                `json:"type"`
	Relation     string                `json:"relation"`
	Exposed      bool                  `json:"exposed"`
	Dispatchers  []string              `json:"dispatchers"`
	DependsOn    []string              `json:"depends_on,omitempty"`
	Functions    []Function            `json:"functions"`
	NotInstalled []sqlgen.NotInstalled `json:"not_installed,omitempty"`
}

// MarshalJSON writes the relations and dispatchers as arrays, [] when
// empty, as the CLI reference documents them.
func (r Report) MarshalJSON() ([]byte, error) {
	type report Report
	out := report(r)
	out.Relations, out.Dispatchers = nonNil(out.Relations), nonNil(out.Dispatchers)
	return json.Marshal(out)
}

// MarshalJSON writes the dispatchers and functions as arrays, [] when
// empty.
func (rel Relation) MarshalJSON() ([]byte, error) {
	type relation Relation
	out := relation(rel)
	out.Dispatchers, out.Functions = nonNil(out.Dispatchers), nonNil(out.Functions)
	return json.Marshal(out)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Function is a catalog function and its status.
type Function struct {
	sqlgen.CatalogFunction
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	// StaleOverloads are other functions of the same name, by their
	// arguments, left by an earlier signature.
	StaleOverloads []string `json:"stale_overloads,omitempty"`
}

// InstalledFunction is a function found in pg_proc.
type InstalledFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Summary counts the report's functions.
type Summary struct {
	Relations        int `json:"relations"`
	Exposed          int `json:"exposed"`
	Functions        int `json:"functions"`
	Installed        int `json:"installed"`
	Missing          int `json:"missing"`
	SignatureDiffers int `json:"signature_differs"`
	BodyChanged      int `json:"body_changed"`
	CodegenDiffers   int `json:"codegen_differs"`
	StaleOverloads   int `json:"stale_overloads"`
	Orphans          int `json:"orphans"`
	Unmanaged        int `json:"unmanaged"`
}

// Drift reports whether the database differs from what is expected.
func (s Summary) Drift() bool {
	return s.Missing+s.SignatureDiffers+s.BodyChanged+s.CodegenDiffers+s.StaleOverloads+s.Orphans > 0
}

// Describe returns the SQL API a migration of types under exposure into
// databaseSchema installs, unchecked.
func Describe(types []schema.TypeDefinition, exposure sqlgen.Exposure, databaseSchema string) (*Report, error) {
	analyses := sqlgen.ComputeCanGenerate(sqlgen.AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
	cat, err := sqlgen.BuildCatalog(analyses, sqlgen.CatalogOptions{Exposure: exposure, DatabaseSchema: databaseSchema})
	if err != nil {
		return nil, err
	}
	r := &Report{
		Source:         "file",
		DatabaseSchema: databaseSchema,
		MelangeVersion: migrator.CodegenVersion(),
		Exposure:       cat.Exposure,
		Collisions:     cat.Collisions,
		types:          types,
	}
	for _, cr := range cat.Relations {
		rel := Relation{Type: cr.Type, Relation: cr.Relation, Exposed: cr.Exposed, Dispatchers: cr.Dispatchers,
			DependsOn: cr.DependsOn, NotInstalled: cr.NotInstalled}
		for _, f := range cr.Functions {
			rel.Functions = append(rel.Functions, Function{CatalogFunction: f, Status: StatusNotChecked})
		}
		r.Relations = append(r.Relations, rel)
	}
	for _, d := range cat.Dispatchers {
		r.Dispatchers = append(r.Dispatchers, Function{CatalogFunction: d, Status: StatusNotChecked})
	}
	// Model order: analysis order groups relations differently.
	pos := make(map[string]int)
	for _, t := range types {
		for _, rel := range t.Relations {
			pos[t.Name+"#"+rel.Name] = len(pos)
		}
	}
	slices.SortStableFunc(r.Relations, func(a, b Relation) int {
		return cmp.Compare(pos[a.Type+"#"+a.Relation], pos[b.Type+"#"+b.Relation])
	})
	if len(cat.Collisions) > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("%d function names are generated for more than one relation; CREATE OR REPLACE keeps only the last, so the others answer with another relation's function. Rename one of the relations.", len(cat.Collisions)))
	}
	r.summarize()
	return r, nil
}

// Find returns the relation, or nil.
func (r *Report) Find(objectType, relation string) *Relation {
	for i := range r.Relations {
		if r.Relations[i].Type == objectType && r.Relations[i].Relation == relation {
			return &r.Relations[i]
		}
	}
	return nil
}

// Owners maps every function name to the relations it belongs to, as
// type#relation; a dispatcher's name maps to nothing.
func (r *Report) Owners() map[string][]string {
	out := make(map[string][]string)
	for _, rel := range r.Relations {
		for _, f := range rel.Functions {
			out[f.Name] = append(out[f.Name], rel.Type+"#"+rel.Relation)
		}
	}
	for _, d := range r.Dispatchers {
		if _, ok := out[d.Name]; !ok {
			out[d.Name] = nil
		}
	}
	return out
}

// ErrNoRecordedModel is returned by Deployed for a database whose last
// migration recorded no model, when there is no fallback to describe.
var ErrNoRecordedModel = errors.New("no recorded model")

// Fallback is the local model Deployed describes for a database that
// records none.
type Fallback struct {
	// Types are the local schema file's.
	Types []schema.TypeDefinition
	// Exposure is the local one (melange.yaml's sql section), used when no
	// migration is recorded at all, as for a database built from melange
	// generate migration files, which write no record.
	Exposure sqlgen.Exposure
}

// Deployed returns the report for the model a database's last migration
// recorded, under its recorded exposure, checked against pg_proc. With no
// recorded model it describes fallback: under the recorded exposure when a
// migration is recorded without its model (an older melange, which
// installed the default), else under fallback's exposure, as doctor does.
// With no fallback either it fails.
func Deployed(ctx context.Context, db migrator.Execer, databaseSchema string, fallback *Fallback, opts CheckOptions) (*Report, *migrator.MigrationRecord, error) {
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)
	rec, err := m.GetLastMigration(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the migration record: %w", err)
	}
	types, err := migrator.DeployedTypes(rec)
	if err != nil {
		types = nil
	}
	var exposure sqlgen.Exposure
	var note string
	switch {
	case types != nil:
		exposure = rec.Exposure
	case fallback == nil && rec == nil:
		return nil, rec, fmt.Errorf("%w: no melange migration found in schema %q", ErrNoRecordedModel, databaseSchema)
	case fallback == nil:
		return nil, rec, fmt.Errorf("%w: the last migration recorded none; re-run melange migrate with a current version to record it", ErrNoRecordedModel)
	case rec != nil:
		types, exposure = fallback.Types, rec.Exposure
		note = "The last migration recorded no model, so the local schema file is compared under the recorded exposure; run melange migrate with this melange to record the model."
	default:
		types, exposure = fallback.Types, fallback.Exposure
		note = "No migration is recorded (as for a database built from melange generate migration files), so the local schema file is compared under the local exposure."
	}
	r, err := Describe(types, exposure, databaseSchema)
	if err != nil {
		return nil, rec, err
	}
	r.Source = "database"
	if note != "" {
		r.Notes = append(r.Notes, note)
	}
	if err := r.Check(ctx, db, rec, opts); err != nil {
		return nil, rec, err
	}
	return r, rec, nil
}

// CheckOptions configures Check.
type CheckOptions struct {
	// Bodies compares each installed body with the one this melange
	// generates, which compiles the model (most of a second for a large
	// one, cached per process).
	Bodies bool
}

// installedRow is one pg_proc row; srcMD5 is the md5 of its body (prosrc)
// when bodies are compared.
type installedRow struct {
	name, args string
	srcMD5     *string
}

// Check reads the functions installed in the report's database schema and
// sets every status. rec is the last migration record, or nil.
func (r *Report) Check(ctx context.Context, db melange.Querier, rec *migrator.MigrationRecord, opts CheckOptions) error {
	rows, err := db.QueryContext(ctx, `
		SELECT p.proname, pg_get_function_identity_arguments(p.oid), CASE WHEN $2 THEN md5(p.prosrc) END
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1 AND `+migrator.GeneratedFunctionFilter+`
		ORDER BY p.proname, p.oid`, r.DatabaseSchema, opts.Bodies)
	if err != nil {
		return fmt.Errorf("reading pg_proc: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var installed []installedRow
	for rows.Next() {
		var row installedRow
		if err := rows.Scan(&row.name, &row.args, &row.srcMD5); err != nil {
			return fmt.Errorf("reading pg_proc: %w", err)
		}
		installed = append(installed, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading pg_proc: %w", err)
	}
	var gen *generated
	if opts.Bodies {
		if gen, err = r.bodiesFor(installed); err != nil {
			return fmt.Errorf("compiling the model to compare bodies: %w", err)
		}
	}
	r.classify(installed, rec, gen)
	return nil
}

// bodiesFor compiles the bodies to compare installed with: the model
// compiled into the report's schema and, when an installed body differs
// from that, compiled without a schema too. A migration through the Go API
// that leaves MigrateOptions.DatabaseSchema empty generates unqualified
// bodies and checksums, and the record does not say which it used.
func (r *Report) bodiesFor(installed []installedRow) (*generated, error) {
	gen, err := generatedFor(r.types, r.Exposure, r.DatabaseSchema)
	if err != nil || r.DatabaseSchema == "" {
		return gen, err
	}
	for _, row := range installed {
		if body, ok := gen.bodies[row.name]; ok && row.srcMD5 != nil && *row.srcMD5 != body {
			unqualified, err := generatedFor(r.types, r.Exposure, "")
			if err != nil {
				return nil, err
			}
			// A copy: gen is cached and shared.
			return &generated{bodies: gen.bodies, checksums: gen.checksums, unqualified: unqualified}, nil
		}
	}
	return gen, nil
}

// classify sets the statuses from the installed rows, the record and, when
// not nil, the bodies and checksums this melange generates.
func (r *Report) classify(installed []installedRow, rec *migrator.MigrationRecord, gen *generated) {
	r.Checked, r.BodiesChecked = true, gen != nil
	r.DeployedCodegenVersion, r.CodegenSkew, r.CodegenAhead = "", false, false
	if rec != nil {
		r.DeployedCodegenVersion = rec.CodegenVersion
		r.CodegenSkew = !migrator.SameCodegenVersion(rec.CodegenVersion, migrator.CodegenVersion())
		r.CodegenAhead = migrator.CodegenVersionAhead(rec.CodegenVersion)
	}
	byName := make(map[string][]installedRow)
	for _, row := range installed {
		byName[row.name] = append(byName[row.name], row)
	}
	set := func(f *Function) {
		f.Status, f.Detail, f.StaleOverloads = r.status(*f, byName[f.Name], rec, gen)
	}
	expected := make(map[string]bool)
	for i := range r.Relations {
		for j := range r.Relations[i].Functions {
			set(&r.Relations[i].Functions[j])
			expected[r.Relations[i].Functions[j].Name] = true
		}
	}
	for i := range r.Dispatchers {
		set(&r.Dispatchers[i])
		expected[r.Dispatchers[i].Name] = true
	}
	var recorded map[string]bool
	if rec != nil {
		recorded = make(map[string]bool, len(rec.FunctionNames))
		for _, n := range rec.FunctionNames {
			recorded[n] = true
		}
	}
	r.Orphans, r.Unmanaged = nil, nil
	for _, row := range installed {
		if expected[row.name] {
			continue
		}
		fn := InstalledFunction{Name: row.name, Arguments: row.args}
		if recorded[row.name] {
			r.Orphans = append(r.Orphans, fn)
		} else {
			r.Unmanaged = append(r.Unmanaged, fn)
		}
	}
	deployed, current := displayVersion(r.DeployedCodegenVersion), displayVersion(migrator.CodegenVersion())
	switch {
	case r.CodegenAhead:
		r.Notes = append(r.Notes, fmt.Sprintf("The functions were generated by melange %s, newer than this melange (%s), which expects the names, signatures and bodies it generates: its migrate refuses to replace or drop them, and the orphans are functions melange %s generates. Use melange %s or later before reading differences as drift.",
			deployed, current, deployed, deployed))
	case r.CodegenSkew:
		r.Notes = append(r.Notes, fmt.Sprintf("The functions were generated by melange %s and this is melange %s, which expects the names, signatures and bodies it generates: run melange migrate with this melange, or use melange %s, before reading differences as drift.",
			deployed, current, deployed))
	}
	if rec == nil && len(r.Unmanaged) > 0 {
		r.Notes = append(r.Notes, "No migration is recorded, so installed functions the model does not generate cannot be told apart from an application's own.")
	}
	r.summarize()
}

// status compares one expected function with the installed rows of its name.
func (r *Report) status(f Function, rows []installedRow, rec *migrator.MigrationRecord, gen *generated) (Status, string, []string) {
	if len(rows) == 0 {
		return StatusMissing, "not installed; " + r.reinstall(), nil
	}
	want := f.Signature.IdentityArgs()
	var match *installedRow
	var others []string
	for i := range rows {
		if match == nil && rows[i].args == want {
			match = &rows[i]
			continue
		}
		others = append(others, rows[i].args)
	}
	if match == nil {
		detail := fmt.Sprintf("installed as %s(%s), expected %s(%s); ", f.Name, strings.Join(others, "; "), f.Name, want)
		if r.CodegenAhead {
			return StatusSignatureDiffers, detail + r.reinstall(), nil
		}
		// CREATE OR REPLACE cannot rename an argument and adds an overload
		// for other argument types, so migrate cannot replace it.
		drops := make([]string, len(others))
		for i, args := range others {
			drops[i] = "DROP FUNCTION " + r.qualify(f.Name) + "(" + args + ");"
		}
		return StatusSignatureDiffers, detail + "migrate cannot replace a function whose arguments differ, so drop it first (" + strings.Join(drops, " ") + "), then " + r.reinstall(), nil
	}
	if gen == nil || match.srcMD5 == nil {
		return StatusInstalled, "", others
	}
	if _, ok := gen.bodies[f.Name]; !ok || gen.hasBody(f.Name, *match.srcMD5) {
		return StatusInstalled, "", others
	}
	entry := cmp.Or(f.RenderedWith, f.Name)
	recorded, hasRecorded := "", false
	if rec != nil {
		recorded, hasRecorded = rec.FunctionChecksums[entry]
	}
	if (hasRecorded && !gen.hasChecksum(entry, recorded)) || (!hasRecorded && r.CodegenSkew) {
		if r.CodegenAhead {
			return StatusCodegenDiffers, "the newer melange that migrated the database generates this body; this melange generates another, and its migrate refuses to downgrade it", others
		}
		return StatusCodegenDiffers, "the next melange migrate replaces it with the body this melange generates", others
	}
	return StatusBodyChanged, "the installed body is not the one melange migrate generated; " + r.reinstall(), others
}

// reinstall says how to put a function back as generated. --force is the
// first step only when this codegen version migrated the database: it
// overrides migrate's refusal to downgrade a newer one, and it drops orphans
// with CASCADE, which under skew are the other version's functions and their
// dependents. A plain migrate applies them first and refuses those drops.
func (r *Report) reinstall() string {
	switch {
	case r.CodegenAhead:
		v := displayVersion(r.DeployedCodegenVersion)
		return fmt.Sprintf("a newer melange (%s) migrated the database and this melange's migrate refuses to downgrade it: use melange %s or later", v, v)
	case r.CodegenSkew:
		return "run melange migrate with this melange, which applies what this codegen version generates; if it is still not as generated, melange migrate --force reinstalls it"
	}
	return "melange migrate --force reinstalls it (a plain migrate skips while the schema, exposure and codegen version are unchanged)"
}

// qualify returns a generated function's name in the report's database
// schema, as the catalog's example calls write it.
func (r *Report) qualify(name string) string {
	if r.DatabaseSchema == "" || r.DatabaseSchema == "public" {
		return name
	}
	return pq.QuoteIdentifier(r.DatabaseSchema) + "." + name
}

func (r *Report) summarize() {
	s := Summary{Orphans: len(r.Orphans), Unmanaged: len(r.Unmanaged)}
	count := func(f Function) {
		s.Functions++
		switch f.Status {
		case StatusInstalled:
			s.Installed++
		case StatusMissing:
			s.Missing++
		case StatusSignatureDiffers:
			s.SignatureDiffers++
		case StatusBodyChanged:
			s.BodyChanged++
		case StatusCodegenDiffers:
			s.CodegenDiffers++
		}
		s.StaleOverloads += len(f.StaleOverloads)
	}
	for _, rel := range r.Relations {
		s.Relations++
		if rel.Exposed {
			s.Exposed++
		}
		for _, f := range rel.Functions {
			count(f)
		}
	}
	for _, d := range r.Dispatchers {
		count(d)
	}
	r.Summary = s
}

// generated is what this melange generates for a model: the md5 of each
// function's body as PostgreSQL stores it (pg_proc.prosrc), so only digests
// cross the connection, and each checksummed entry's checksum, as migrate
// records them.
type generated struct {
	bodies    map[string]string
	checksums map[string]string
	// unqualified, when set, is the model compiled without a schema, whose
	// bodies and checksums match too (bodiesFor).
	unqualified *generated
}

// hasBody reports whether sum is the md5 of a body generated for name.
func (g *generated) hasBody(name, sum string) bool {
	for ; g != nil; g = g.unqualified {
		if g.bodies[name] == sum {
			return true
		}
	}
	return false
}

// hasChecksum reports whether sum is a checksum generated for entry.
func (g *generated) hasChecksum(entry, sum string) bool {
	for ; g != nil; g = g.unqualified {
		if g.checksums[entry] == sum {
			return true
		}
	}
	return false
}

// generatedCache keeps compiled models, so studio compiles one once.
var generatedCache = &cache[*generated]{max: 4}

// generatedFor compiles types under exposure into databaseSchema, as
// migrate does, on a cache miss.
func generatedFor(types []schema.TypeDefinition, exposure sqlgen.Exposure, databaseSchema string) (*generated, error) {
	key, err := json.Marshal(struct {
		Types    []schema.TypeDefinition
		Exposure sqlgen.Exposure
		Schema   string
	}{types, exposure.Normalized(), databaseSchema})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	k := hex.EncodeToString(sum[:])
	if g, ok := generatedCache.get(k); ok {
		return g, nil
	}
	closure := schema.ComputeRelationClosure(types)
	analyses := sqlgen.ComputeCanGenerate(sqlgen.AnalyzeRelations(types, closure))
	inline := sqlgen.BuildInlineSQLData(closure, analyses)
	opts := sqlgen.GenerateSQLOptions{Exposure: exposure}
	check, err := sqlgen.GenerateSQLWithOptions(analyses, inline, databaseSchema, opts)
	if err != nil {
		return nil, err
	}
	list, err := sqlgen.GenerateListSQLWithOptions(analyses, inline, databaseSchema, opts)
	if err != nil {
		return nil, err
	}
	named := append(sqlgen.CollectNamedFunctionsWithOptions(check, list, analyses, opts), sqlgen.CollectDispatcherFunctions(check, list)...)
	g := &generated{bodies: make(map[string]string), checksums: migrator.ComputeFunctionChecksums(named)}
	for _, nf := range named {
		for name, body := range functionBodies(nf.SQL) {
			sum := md5.Sum([]byte(body))
			g.bodies[name] = hex.EncodeToString(sum[:])
		}
	}
	generatedCache.put(k, g)
	return g, nil
}

// createFunction matches a generated CREATE FUNCTION up to its body, which
// generation always dollar-quotes with $$ and never contains $$.
var createFunction = regexp.MustCompile(`(?s)CREATE OR REPLACE FUNCTION\s+(?:"(?:[^"]|"")*"\.)?"?([A-Za-z0-9_]+)"?\s*\(.*?\)\s*RETURNS\s.*?\sAS \$\$(.*?)\$\$`)

// functionBodies returns the body of each function sql creates, by name,
// as PostgreSQL stores it in prosrc.
func functionBodies(sql string) map[string]string {
	out := make(map[string]string)
	for _, m := range createFunction.FindAllStringSubmatch(sql, -1) {
		out[m[1]] = m[2]
	}
	return out
}
