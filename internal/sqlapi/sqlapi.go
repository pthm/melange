// Package sqlapi tells the tools that call the public SQL functions for a
// relation the user names (melange explain and expand, studio) whether the
// database answers for it, from the exposure recorded by its last migration,
// so they can say why instead of surfacing a raw M2003 or 42883.
//
// When the recorded exposure has the explain or expand family off, the tools
// still explain and expand: Trace compiles the family's functions from the
// recorded model and creates them in pg_temp for one transaction that is
// always rolled back (see ondemand.go).
//
// It depends on the published runtime only through symbols every release
// has: a raised M2003 is recognized by its SQLSTATE.
package sqlapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/lib/pq"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// Family is a public function family a tool calls.
type Family struct {
	name     string // for messages: "explain"
	function string // its public dispatcher
	key      string // its sql.functions key; "" for check, which is always on
	enabled  func(sqlgen.FunctionFamilies) *bool
	public   func(sqlgen.PublicAPI, string, string) bool
	onDemand bool // compiled into pg_temp when the family is off
}

var (
	Check = Family{"check", "check_permission", "", nil, sqlgen.PublicAPI.Check, false}

	ListObjects = Family{
		"list objects", "list_accessible_objects", "list_objects",
		func(f sqlgen.FunctionFamilies) *bool { return f.ListObjects }, sqlgen.PublicAPI.ListObjects, false,
	}

	ListSubjects = Family{
		"list subjects", "list_accessible_subjects", "list_subjects",
		func(f sqlgen.FunctionFamilies) *bool { return f.ListSubjects }, sqlgen.PublicAPI.ListSubjects, false,
	}

	Explain = Family{
		"explain", "explain_permission", "explain",
		func(f sqlgen.FunctionFamilies) *bool { return f.Explain }, sqlgen.PublicAPI.Explain, true,
	}

	Expand = Family{
		"expand", "expand_permission", "expand",
		func(f sqlgen.FunctionFamilies) *bool { return f.Expand }, sqlgen.PublicAPI.Expand, true,
	}
)

// Name is the family's name for messages, such as "list objects".
func (f Family) Name() string { return f.name }

// API is what a database's public functions answer for. The zero value
// answers for everything, as a database with no recorded migration does.
type API struct {
	exposure sqlgen.Exposure
	types    []schema.TypeDefinition
	defined  map[string]bool // "type#relation"; nil when the model is unknown
	// public is resolved with the explain and expand families on, which is
	// what they answer for on demand; when installed they are on anyway.
	public sqlgen.PublicAPI

	// codegenVersion generated the installed functions; "" unless the API
	// comes from a record.
	codegenVersion string

	// badName is a name in types the schema language does not allow, or "";
	// a model with one is never compiled on demand.
	badName string
	// unreadable is set when the record has a model that cannot be read,
	// which is told apart from a record with none.
	unreadable bool
}

// New resolves the recorded exposure against the deployed model's types.
// types may be nil when no model is recorded; then only a family that is
// off can be told apart, relations are assumed exposed, and explain and
// expand cannot be compiled on demand.
func New(exposure sqlgen.Exposure, types []schema.TypeDefinition) *API {
	a := &API{exposure: exposure, types: types}
	if types == nil {
		return a
	}
	a.badName = invalidName(reflect.ValueOf(types))
	a.defined = make(map[string]bool)
	for _, t := range types {
		for _, r := range t.Relations {
			a.defined[t.Name+"#"+r.Name] = true
		}
	}
	if traceOn := withTraceFamiliesOn(exposure); !traceOn.IsDefault() {
		analyses := sqlgen.ComputeCanGenerate(sqlgen.AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
		a.public = sqlgen.NewPublicAPI(analyses, traceOn)
	}
	return a
}

// withTraceFamiliesOn returns e with the explain and expand families on.
// Neither changes which relations the other families answer for.
func withTraceFamiliesOn(e sqlgen.Exposure) sqlgen.Exposure {
	e.Functions.Explain, e.Functions.Expand = nil, nil
	return e
}

// FromModel is New for a deployed model, which also carries what compiling
// explain and expand on demand needs. A nil model answers for everything.
func FromModel(model *migrator.DeployedModel) *API {
	if model == nil {
		return nil
	}
	a := New(model.Exposure, model.Types)
	a.codegenVersion = model.CodegenVersion
	return a
}

// Load reads the exposure and model recorded by the database's last
// migration. A database never migrated, or migrated before the exposure was
// recorded, has the default exposure. Load never fails: when the record
// cannot be read, the API answers for everything and the calls themselves
// report what is wrong.
func Load(ctx context.Context, db *sql.DB, databaseSchema string) *API {
	m := migrator.NewMigrator(db, "")
	m.SetDatabaseSchema(databaseSchema)
	rec, err := m.GetLastMigration(ctx)
	if err != nil || rec == nil {
		return &API{}
	}
	types, err := migrator.DeployedTypes(rec)
	if err != nil {
		types = nil
	}
	a := New(rec.Exposure, types)
	a.codegenVersion, a.unreadable = rec.CodegenVersion, err != nil
	return a
}

// Installed reports whether family's functions are installed in the
// database.
func (a *API) Installed(f Family) bool {
	if a == nil || f.enabled == nil {
		return true
	}
	on := f.enabled(a.exposure.Functions)
	return on == nil || *on
}

// OnDemand reports whether Trace compiles family's functions for each call:
// explain or expand with the family off in the recorded exposure, a recorded
// model, and the codegen version that generated the installed functions.
func (a *API) OnDemand(f Family) bool {
	return !a.Installed(f) && a.cannotCompile(f) == 0
}

// cannotCompile says why family's functions, not installed, cannot be
// compiled on demand, or 0 when they can.
func (a *API) cannotCompile(f Family) kind {
	switch {
	case !f.onDemand:
		return notInstalled
	case a.types == nil && a.unreadable:
		return unreadableModel
	case a.types == nil:
		return noModel
	case a.badName != "":
		return badModel
	case !migrator.SameCodegenVersion(a.codegenVersion, migrator.CodegenVersion()):
		return codegenSkew
	}
	return 0
}

// Unavailable returns why family's public function does not answer for the
// relation, or nil when it does, installed or on demand. A relation the
// model does not define is answered (with the unknown-pair result), as it
// always was, even with the family off: the public dispatchers are always
// generated.
func (a *API) Unavailable(f Family, objectType, relation string) *Unavailable {
	known := a != nil && a.defined != nil
	if known && !a.defined[objectType+"#"+relation] {
		return nil
	}
	if !a.Installed(f) {
		if k := a.cannotCompile(f); k != 0 {
			return a.unavailable(f, objectType, relation, k)
		}
	}
	if !known || f.public(a.public, objectType, relation) {
		return nil
	}
	return &Unavailable{Family: f, ObjectType: objectType, Relation: relation, kind: notExposed}
}

type kind int

const (
	notExposed      kind = iota + 1 // the relation is left out of sql.relations
	notInstalled                    // the family is off in sql.functions
	noModel                         // off, and no recorded model to compile it on demand from
	unreadableModel                 // off, and the recorded model cannot be read
	badModel                        // off, and the recorded model has a name the schema language does not allow
	codegenSkew                     // off, and recorded by another codegen version
	onDemandFailed                  // off, and creating it in pg_temp failed
	raised                          // the database raised M2003; which of the two is unknown
	missing                         // the public function does not exist (42883)
)

// unavailable returns an *Unavailable of kind k for the relation, with the
// record's codegen version (for a skew or a bad model) and its bad name.
func (a *API) unavailable(f Family, objectType, relation string, k kind) *Unavailable {
	u := &Unavailable{Family: f, ObjectType: objectType, Relation: relation, kind: k}
	if a != nil {
		u.recordedVersion, u.badName = a.codegenVersion, a.badName
	}
	return u
}

// dslName matches the type and relation names the schema language allows,
// and a few it does not (a trailing "." or "/"). None of its characters can
// end the comment, quoted literal or dollar-quoted body the compiled SQL
// writes a name into.
var dslName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./-]*$`)

// invalidName returns the first non-empty string in v that dslName does not
// match, or "". Every string in a model is a type or relation name, defined or
// referenced, so walking them all keeps up with new fields. model_json is
// recorded by melange migrate, which parsed it, but reading it back
// validates nothing, and anyone who can write melange_migrations can change
// it, so a name is checked before on demand compiles SQL from it.
func invalidName(v reflect.Value) string {
	var elems []reflect.Value
	switch v.Kind() {
	case reflect.String:
		if s := v.String(); s != "" && !dslName.MatchString(s) {
			return s
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			elems = append(elems, v.Elem())
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			elems = append(elems, v.Index(i))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			elems = append(elems, v.Field(i))
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			elems = append(elems, it.Key(), it.Value())
		}
	}
	for _, e := range elems {
		if s := invalidName(e); s != "" {
			return s
		}
	}
	return ""
}

// Unavailable says why a public function does not answer for a relation. It
// is an error, worded for the user, with how to change it.
type Unavailable struct {
	Family               Family
	ObjectType, Relation string
	kind                 kind
	function             string // missing: the function as the database named it, maybe schema-qualified
	recordedVersion      string // codegenSkew, badModel, unreadableModel: the codegen version the database records
	badName              string // badModel: the recorded name the schema language does not allow
	cause                error  // the error FromError recognized, or the on-demand failure
}

// Unwrap returns the database error FromError recognized, or the one that
// made compiling the functions on demand fail, or nil.
func (u *Unavailable) Unwrap() error { return u.cause }

// Label is a short status for a relation the tool skips: "not exposed" or
// "not installed" (the family is off or its function is missing).
func (u *Unavailable) Label() string {
	switch u.kind {
	case notInstalled, noModel, unreadableModel, badModel, codegenSkew, onDemandFailed, missing:
		return "not installed"
	default:
		return "not exposed"
	}
}

// ByDesign reports whether the function does not answer because of how the
// database was migrated: the relation is not exposed, or its family is off.
// Anything else (an unreadable or invalid recorded model, a codegen skew, a
// failed on-demand compile, a missing function) is a fault to fix.
func (u *Unavailable) ByDesign() bool {
	return u.kind == notExposed || u.kind == notInstalled || u.kind == raised
}

func (u *Unavailable) Error() string {
	rel := u.ObjectType + "#" + u.Relation
	// --include-relation replaces sql.relations.include, so on its own it
	// would unexpose every other relation.
	exposeHint := fmt.Sprintf("add %s to sql.relations.include in melange.yaml (or pass --include-relation %s with every pattern already included, since the flag replaces the list), check that sql.relations.exclude does not match it, and run melange migrate", rel, rel)
	// A studio tab asks for the family alone, with no relation.
	answers := ""
	if u.Relation != "" {
		answers = fmt.Sprintf(", so %s does not answer for %s", u.Family.function, rel)
	}
	installHint := fmt.Sprintf("turn sql.functions.%s back on in melange.yaml (or drop --no-%s) and run melange migrate",
		u.Family.key, strings.ReplaceAll(u.Family.key, "_", "-"))
	off := fmt.Sprintf("the %s functions are not installed%s: the database was migrated with sql.functions.%s off",
		u.Family.name, answers, u.Family.key)
	cannotCompile := off + ", and melange cannot compile them on demand because "
	switch u.kind {
	case notInstalled:
		return off + "; " + installHint
	case noModel:
		return cannotCompile + "its last migration recorded no model to compile them from; run melange migrate to record it, or " + installHint
	case unreadableModel:
		return cannotCompile + fmt.Sprintf("the model its last migration recorded cannot be read; %s, or %s",
			rerecordHint(u.recordedVersion), installHint)
	case badModel:
		return cannotCompile + fmt.Sprintf("the model its last migration recorded has a name the schema language does not allow (%q); %s, or %s",
			u.badName, rerecordHint(u.recordedVersion), installHint)
	case codegenSkew:
		recorded := displayVersion(u.recordedVersion)
		return cannotCompile + fmt.Sprintf("its functions were generated by melange %s and this is melange %s, whose %s functions may not match them; run melange migrate with this melange, use melange %s, or %s",
			recorded, displayVersion(migrator.CodegenVersion()), u.Family.name, recorded, installHint)
	case onDemandFailed:
		return fmt.Sprintf("%s on demand failed: the database was migrated with sql.functions.%s off, so melange creates the %s functions in pg_temp for each call, which needs a writable primary (not a read-only replica) and the TEMP privilege on the database: %s; connect to the primary, or %s",
			u.Family.name, u.Family.key, u.Family.name, causeMessage(u.cause), installHint)
	case raised:
		hint := exposeHint
		if u.Family.key != "" {
			hint += fmt.Sprintf(", with sql.functions.%s on", u.Family.key)
		}
		if u.Family.onDemand {
			// Raised although no record says the family is off: migrated
			// without a record, which on demand needs.
			hint += fmt.Sprintf(" (with it off, melange migrate records the model melange compiles the %s functions from on demand)", u.Family.name)
		}
		return fmt.Sprintf("%s is not exposed by the database's SQL API: %s", rel, hint)
	case missing:
		// Nothing here says what a plain migrate would do: it skips while
		// the schema, exposure and codegen version are unchanged.
		const reinstall = "run melange migrate (melange migrate --force if it reports the schema unchanged)"
		if strings.Contains(u.function, ".") {
			return fmt.Sprintf("%s is not installed in the database: check that --db-schema names the schema melange was migrated into, or %s", u.function, reinstall)
		}
		return fmt.Sprintf("%s is not installed in the database: %s", u.function, reinstall)
	default:
		return fmt.Sprintf("%s is not exposed by the database's SQL API, so %s does not answer for it: %s",
			rel, u.Family.function, exposeHint)
	}
}

// rerecordHint says how to record a model again over one that cannot be
// used. A plain migrate does not read the recorded model, so under the
// codegen version that recorded it, it skips; --force records it. A newer
// melange's record is refused, and an older one's is replaced.
func rerecordHint(recordedVersion string) string {
	recorded := displayVersion(recordedVersion)
	switch {
	case migrator.CodegenVersionAhead(recordedVersion):
		return fmt.Sprintf("use melange %s or later, which migrated the database, and run melange migrate with it to record the model again (with --force if it reports the schema unchanged)", recorded)
	case migrator.SameCodegenVersion(recordedVersion, migrator.CodegenVersion()):
		return "run melange migrate --force to record the model again (a plain migrate skips while the schema, exposure and codegen version are unchanged)"
	}
	return "run melange migrate with this melange to record the model again"
}

// FromError recognizes a public function call that failed because the
// relation is not exposed (M2003) or the family's public function is missing
// (42883 naming it), and returns it as an *Unavailable; it returns nil for
// any other error, including a 42883 for another function the call reached.
//
// M2003 is recognized by its SQLSTATE on the driver error, which the
// published runtime returns wrapped, and by the ValidationError that later
// runtimes map it to. A missing function is recognized by the database's
// message, since the runtime prefixes the error with the public function's
// name and maps some 42883s to ErrMissingFunction without the driver error.
func FromError(f Family, objectType, relation string, err error) *Unavailable {
	if err == nil {
		return nil
	}
	var refused *Unavailable
	if errors.As(err, &refused) {
		// From Trace, which does not know the relation.
		u := *refused
		if u.ObjectType == "" && u.Relation == "" {
			u.ObjectType, u.Relation = objectType, relation
		}
		return &u
	}
	u := &Unavailable{Family: f, ObjectType: objectType, Relation: relation, cause: err}
	var ve *melange.ValidationError
	switch code := sqlState(err); {
	case code == sqlgen.NotExposedErrCode,
		errors.As(err, &ve) && ve.Code == melange.ErrorCodeValidation && strings.HasSuffix(ve.Message, " is not exposed"):
		u.kind = raised
	case code == "42883" || errors.Is(err, melange.ErrMissingFunction):
		u.function = missingFunction(err)
		if u.function == "" || u.function[strings.LastIndex(u.function, ".")+1:] != f.function {
			return nil
		}
		u.kind = missing
	default:
		return nil
	}
	return u
}

// missingFunctionRE matches PostgreSQL's 42883 message for a function, as
// in "function myschema.expand_permission(unknown, ...) does not exist".
var missingFunctionRE = regexp.MustCompile(`function ([^\s(]+)\(.*\) does not exist`)

// missingFunction returns the function a 42883 message names, maybe
// schema-qualified, or "" when it names none (an operator, for one).
func missingFunction(err error) string {
	if m := missingFunctionRE.FindStringSubmatch(err.Error()); m != nil {
		return m[1]
	}
	return ""
}

// sqlState returns the SQLSTATE of the driver error in err's chain (lib/pq
// and pgx both have SQLState), or "".
func sqlState(err error) string {
	var se interface{ SQLState() string }
	if errors.As(err, &se) {
		return se.SQLState()
	}
	return ""
}

// displayVersion names a codegen version in a message.
func displayVersion(v string) string {
	if v == "" {
		return "(unrecorded)"
	}
	return v
}

// causeMessage is the database's message for err, without the driver's
// prefix, or err's text.
func causeMessage(err error) string {
	if err == nil {
		return "unknown error"
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Message
	}
	return err.Error()
}
