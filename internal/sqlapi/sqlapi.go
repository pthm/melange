// Package sqlapi tells the tools that call the public SQL functions for a
// relation the user names (melange explain and expand, studio) whether the
// database answers for it, from the exposure recorded by its last migration,
// so they can say why instead of surfacing a raw M2003 or 42883.
//
// It depends on the published runtime only through symbols every release
// has: a raised M2003 is recognized by its SQLSTATE.
package sqlapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

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
}

var (
	Check = Family{"check", "check_permission", "", nil, sqlgen.PublicAPI.Check}

	ListObjects = Family{
		"list objects", "list_accessible_objects", "list_objects",
		func(f sqlgen.FunctionFamilies) *bool { return f.ListObjects }, sqlgen.PublicAPI.ListObjects,
	}

	ListSubjects = Family{
		"list subjects", "list_accessible_subjects", "list_subjects",
		func(f sqlgen.FunctionFamilies) *bool { return f.ListSubjects }, sqlgen.PublicAPI.ListSubjects,
	}

	Explain = Family{
		"explain", "explain_permission", "explain",
		func(f sqlgen.FunctionFamilies) *bool { return f.Explain }, sqlgen.PublicAPI.Explain,
	}

	Expand = Family{
		"expand", "expand_permission", "expand",
		func(f sqlgen.FunctionFamilies) *bool { return f.Expand }, sqlgen.PublicAPI.Expand,
	}
)

// Name is the family's name for messages, such as "list objects".
func (f Family) Name() string { return f.name }

// API is what a database's public functions answer for. The zero value
// answers for everything, as a database with no recorded migration does.
type API struct {
	exposure sqlgen.Exposure
	defined  map[string]bool // "type#relation"; nil when the model is unknown
	public   sqlgen.PublicAPI
}

// New resolves the recorded exposure against the deployed model's types.
// types may be nil when no model is recorded; then only a family that is
// off can be told apart, and relations are assumed exposed.
func New(exposure sqlgen.Exposure, types []schema.TypeDefinition) *API {
	a := &API{exposure: exposure}
	if types == nil {
		return a
	}
	a.defined = make(map[string]bool)
	for _, t := range types {
		for _, r := range t.Relations {
			a.defined[t.Name+"#"+r.Name] = true
		}
	}
	if !exposure.IsDefault() {
		analyses := sqlgen.ComputeCanGenerate(sqlgen.AnalyzeRelations(types, schema.ComputeRelationClosure(types)))
		a.public = sqlgen.NewPublicAPI(analyses, exposure)
	}
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
	types, err := schema.UnmarshalModel(rec.ModelJSON)
	if err != nil {
		types = nil
	}
	return New(rec.Exposure, types)
}

// Installed reports whether family's functions are installed.
func (a *API) Installed(f Family) bool {
	if a == nil || f.enabled == nil {
		return true
	}
	on := f.enabled(a.exposure.Functions)
	return on == nil || *on
}

// Unavailable returns why family's public function does not answer for the
// relation, or nil when it does. A relation the model does not define is
// answered (with the unknown-pair result), as it always was, even with the
// family off: the public dispatchers are always generated.
func (a *API) Unavailable(f Family, objectType, relation string) *Unavailable {
	known := a != nil && a.defined != nil
	if known && !a.defined[objectType+"#"+relation] {
		return nil
	}
	if !a.Installed(f) {
		return &Unavailable{Family: f, ObjectType: objectType, Relation: relation, kind: notInstalled}
	}
	if !known || f.public(a.public, objectType, relation) {
		return nil
	}
	return &Unavailable{Family: f, ObjectType: objectType, Relation: relation, kind: notExposed}
}

type kind int

const (
	notExposed   kind = iota // the relation is left out of sql.relations
	notInstalled             // the family is off in sql.functions
	raised                   // the database raised M2003; which of the two is unknown
	missing                  // the public function does not exist (42883)
)

// Unavailable says why a public function does not answer for a relation. It
// is an error, worded for the user, with how to change it.
type Unavailable struct {
	Family               Family
	ObjectType, Relation string
	kind                 kind
	function             string // missing: the function as the database named it, maybe schema-qualified
	cause                error  // the error FromError recognized
}

// Unwrap returns the database error FromError recognized, or nil.
func (u *Unavailable) Unwrap() error { return u.cause }

// Label is a short status for a relation the tool skips: "not exposed" or
// "not installed" (the family is off or its function is missing).
func (u *Unavailable) Label() string {
	switch u.kind {
	case notInstalled, missing:
		return "not installed"
	default:
		return "not exposed"
	}
}

func (u *Unavailable) Error() string {
	rel := u.ObjectType + "#" + u.Relation
	// --include-relation replaces sql.relations.include, so on its own it
	// would unexpose every other relation.
	exposeHint := fmt.Sprintf("add %s to sql.relations.include in melange.yaml (or pass --include-relation %s with every pattern already included, since the flag replaces the list), check that sql.relations.exclude does not match it, and run melange migrate", rel, rel)
	switch u.kind {
	case notInstalled:
		// A studio tab asks for the family alone, with no relation.
		answers := ""
		if u.Relation != "" {
			answers = fmt.Sprintf(", so %s does not answer for %s", u.Family.function, rel)
		}
		return fmt.Sprintf("the %s functions are not installed%s: the database was migrated with sql.functions.%s off; turn it back on in melange.yaml (or drop --no-%s) and run melange migrate",
			u.Family.name, answers, u.Family.key, strings.ReplaceAll(u.Family.key, "_", "-"))
	case raised:
		hint := exposeHint
		if u.Family.key != "" {
			hint += fmt.Sprintf(", with sql.functions.%s on", u.Family.key)
		}
		return fmt.Sprintf("%s is not exposed by the database's SQL API: %s", rel, hint)
	case missing:
		if strings.Contains(u.function, ".") {
			return fmt.Sprintf("%s is not installed in the database: check that --db-schema names the schema melange was migrated into, or run melange migrate", u.function)
		}
		return fmt.Sprintf("%s is not installed in the database: run melange migrate", u.function)
	default:
		return fmt.Sprintf("%s is not exposed by the database's SQL API, so %s does not answer for it: %s",
			rel, u.Family.function, exposeHint)
	}
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
