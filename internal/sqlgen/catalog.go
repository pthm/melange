package sqlgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/pthm/melange/internal/sqlgen/sqldsl"
)

// The catalog describes the SQL API a migration installs, for tools that
// show it (melange functions, studio): per relation, every function under
// its exact installed name, with its kind, signature, an example call and
// the functions it may call; why a family is not installed; and the
// dispatchers with the relations they answer for. It is built from the same
// naming helpers, eligibility and exposure closure as generation, never by
// parsing names, since one name can belong to several relations and long
// names are truncated and hashed.

// FunctionKind is what a generated function does.
type FunctionKind string

const (
	KindCheck           FunctionKind = "check"         // check_<t>_<r>
	KindCheckNoWildcard FunctionKind = "check_nw"      // check_<t>_<r>_nw
	KindListObjects     FunctionKind = "list_objects"  // list_<t>_<r>_obj
	KindListSubjects    FunctionKind = "list_subjects" // list_<t>_<r>_sub
	KindAccessible      FunctionKind = "accessible"    // accessible_<t>_<r>
	KindParentKey       FunctionKind = "parent_key"    // accessible_<t>_<r>_by_<link>
	KindExpand          FunctionKind = "expand"        // expand_<t>_<r>
	KindExplain         FunctionKind = "explain"       // explain_<t>_<r>
	KindDispatcher      FunctionKind = "dispatcher"    // check_permission and the other entry points
)

// FunctionKinds lists the kinds in the order a relation's functions are
// listed.
var FunctionKinds = []FunctionKind{
	KindCheck, KindCheckNoWildcard, KindListObjects, KindListSubjects, KindAccessible,
	KindParentKey, KindExpand, KindExplain, KindDispatcher,
}

// NotInstalledReason says why a relation has no function of a kind.
type NotInstalledReason string

const (
	// ReasonNoAccessPath: the relation has no access path, so nothing is
	// generated for it and the dispatchers have no case for it (check
	// denies, the lists return no rows).
	ReasonNoAccessPath NotInstalledReason = "no_access_path"
	// ReasonNotEligible: the explain or expand renderer does not support the
	// relation's rewrite; its dispatcher returns the no-entry result.
	ReasonNotEligible NotInstalledReason = "not_eligible"
	// ReasonFamilyOff: the family is off in sql.functions.
	ReasonFamilyOff NotInstalledReason = "family_off"
	// ReasonOnDemand: explain or expand is off in sql.functions, and the CLI
	// and studio compile it on demand.
	ReasonOnDemand NotInstalledReason = "on_demand"
	// ReasonNotExposed: the relation is not exposed, and no exposed relation
	// resolves through it; for explain, only that it is not exposed, since
	// explain_permission answers only for exposed relations.
	ReasonNotExposed NotInstalledReason = "not_exposed"
)

// Catalog is the SQL API a migration with Exposure installs for a model.
type Catalog struct {
	// Exposure is the normalized exposure the catalog was built for.
	Exposure Exposure `json:"exposure"`
	// Relations are in analysis order.
	Relations []CatalogRelation `json:"relations"`
	// Dispatchers are in DispatcherFunctionNames order, then the raising
	// helper when the exposure leaves something out.
	Dispatchers []CatalogFunction `json:"dispatchers"`
	// Collisions are names more than one function would be created under;
	// CREATE OR REPLACE keeps the last, so all but one owner lose theirs.
	Collisions []NameCollision `json:"collisions,omitempty"`
}

// CatalogRelation is one relation of the model and its functions.
type CatalogRelation struct {
	Type     string `json:"type"`
	Relation string `json:"relation"`
	// Exposed reports whether the exposure selects the relation. An
	// unexposed relation is internal (modeling only): its functions are
	// installed only when an exposed relation resolves through it, and the
	// public dispatchers raise M2003 for it, except expand_permission, which
	// answers for it when it is installed (Dispatchers lists it then).
	Exposed bool `json:"exposed"`
	// Dispatchers are the public dispatchers that answer for the relation
	// instead of raising.
	Dispatchers []string `json:"dispatchers"`
	// Examples call each of Dispatchers for the relation, in the same order.
	Examples []DispatcherCall `json:"examples,omitempty"`
	// DependsOn lists the relations the relation's functions may read, as
	// type#relation, from the model analysis.
	DependsOn []string `json:"depends_on,omitempty"`
	// Functions are the installed functions, in FunctionKinds order.
	Functions []CatalogFunction `json:"functions"`
	// NotInstalled says why a kind has no function, for each kind the
	// relation could have.
	NotInstalled []NotInstalled `json:"not_installed,omitempty"`
}

// DispatcherCall is a call of a public dispatcher for one relation.
type DispatcherCall struct {
	Dispatcher string `json:"dispatcher"`
	// Example runs as written; Usage, when set, shows how an application
	// uses the result.
	Example string `json:"example"`
	Usage   string `json:"usage,omitempty"`
}

// NotInstalled says why a relation has no function of Kind.
type NotInstalled struct {
	Kind   FunctionKind       `json:"kind"`
	Reason NotInstalledReason `json:"reason"`
	Detail string             `json:"detail"`
}

// CatalogFunction is one function a migration installs.
type CatalogFunction struct {
	// Name is the unqualified name the function is created under.
	Name string       `json:"name"`
	Kind FunctionKind `json:"kind"`
	// Public reports whether applications are meant to call it: the
	// dispatchers, the accessible functions of the relations the accessible
	// family exposes, and the parent-key functions. Every other function is
	// internal: a dispatcher routes to it, and its signature may change.
	Public bool `json:"public"`
	// Type and Relation name the relation; empty for a dispatcher.
	Type     string `json:"type,omitempty"`
	Relation string `json:"relation,omitempty"`
	// Link, ParentType and ParentRelations describe a parent-key function:
	// it returns ParentType ids, and reads ParentRelations on ParentType.
	Link            string   `json:"link,omitempty"`
	ParentType      string   `json:"parent_type,omitempty"`
	ParentRelations []string `json:"parent_relations,omitempty"`
	// Truncated reports whether the name was shortened to PostgreSQL's 63
	// bytes, with a hash of the type and relation.
	Truncated bool      `json:"truncated"`
	Signature Signature `json:"signature"`
	// Example is a call that runs as written, given what Note says it needs;
	// Usage, when set, shows how an application uses the result.
	Example string `json:"example,omitempty"`
	Usage   string `json:"usage,omitempty"`
	// Calls lists the installed functions the body may call, from the model
	// analysis. It can name a function the body resolves inline instead.
	Calls []string `json:"calls,omitempty"`
	// Answers lists, for a dispatcher, the relations it routes, as
	// type#relation.
	Answers []string `json:"answers,omitempty"`
	// RenderedWith names, for a dispatcher rendered in the same SQL as
	// another, that other one (the public entry point).
	RenderedWith string `json:"rendered_with,omitempty"`
	Note         string `json:"note,omitempty"`
}

// Signature is a function's declared arguments and result.
type Signature struct {
	Args     []Arg  `json:"args"`
	Returns  string `json:"returns"`
	Language string `json:"language"` // sql or plpgsql
	// Paged reports whether the function returns pages (p_limit, p_after,
	// next_cursor).
	Paged bool `json:"paged"`
}

// Arg is one argument, with its type as PostgreSQL prints it.
type Arg struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
}

// String is the signature as it reads in SQL.
func (s Signature) String() string {
	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = a.Name + " " + a.Type
		if a.Default != "" {
			args[i] += " DEFAULT " + a.Default
		}
	}
	return "(" + strings.Join(args, ", ") + ") RETURNS " + s.Returns
}

// IdentityArgs is the argument list as pg_get_function_identity_arguments
// prints it, which tells overloads apart.
func (s Signature) IdentityArgs() string {
	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = a.Name + " " + a.Type
	}
	return strings.Join(args, ", ")
}

// NameCollision is a name several functions would be created under.
type NameCollision struct {
	Name string `json:"name"`
	// Owners are type#relation (with the kind) or a dispatcher's name.
	Owners []string `json:"owners"`
}

// CatalogOptions configures BuildCatalog.
type CatalogOptions struct {
	Exposure Exposure
	// DatabaseSchema qualifies the example calls; empty or "public" leaves
	// them unqualified.
	DatabaseSchema string
}

// BuildCatalog returns the SQL API a migration under opts.Exposure installs
// for analyses, which must be the whole model.
func BuildCatalog(analyses []RelationAnalysis, opts CatalogOptions) (*Catalog, error) {
	if err := opts.Exposure.Validate(); err != nil {
		return nil, err
	}
	b := catalogBuilder{
		analyses:   analyses,
		lookup:     buildAnalysisLookup(analyses),
		c:          newExposureClosure(analyses, opts.Exposure),
		explain:    ComputeExplainEligibility(analyses),
		expand:     ComputeExpandEligibility(analyses),
		needsNW:    buildNoWildcardIndex(analyses),
		schema:     opts.DatabaseSchema,
		parentKeys: make(map[string]parentKeyRelation),
	}
	// Parent-key names are taken over the whole model; the exposure only
	// decides which are installed.
	for _, pk := range parentKeyRelations(analyses, nil) {
		b.parentKeys[pk.ObjectType+"."+pk.Relation] = pk
	}
	cat := &Catalog{Exposure: opts.Exposure.Normalized()}
	b.installed = b.installedNames()
	for i := range analyses {
		cat.Relations = append(cat.Relations, b.relation(&analyses[i]))
	}
	cat.Dispatchers = b.dispatchers()
	cat.Collisions = collisions(cat)
	return cat, nil
}

// InstalledNames returns the name of every function the catalog lists, once
// per function: a name two functions collide on appears twice, as in
// CollectFunctionNamesWithOptions.
func (c *Catalog) InstalledNames() []string {
	var names []string
	for _, r := range c.Relations {
		for _, f := range r.Functions {
			names = append(names, f.Name)
		}
	}
	for _, f := range c.Dispatchers {
		names = append(names, f.Name)
	}
	return names
}

// Find returns the relation, or nil.
func (c *Catalog) Find(objectType, relation string) *CatalogRelation {
	for i := range c.Relations {
		if c.Relations[i].Type == objectType && c.Relations[i].Relation == relation {
			return &c.Relations[i]
		}
	}
	return nil
}

type catalogBuilder struct {
	analyses        []RelationAnalysis
	lookup          map[string]*RelationAnalysis
	c               *exposureClosure
	explain, expand map[string]map[string]bool
	needsNW         map[string]map[string]bool
	schema          string
	parentKeys      map[string]parentKeyRelation // "type.relation"
	installed       map[string]bool
}

// installedNames returns every installed per-relation function name, for
// the dependency edges, which name only installed functions.
func (b *catalogBuilder) installedNames() map[string]bool {
	set := make(map[string]bool)
	for i := range b.analyses {
		for _, f := range b.functions(&b.analyses[i]) {
			set[f.Name] = true
		}
	}
	return set
}

// relationInstalls says which of a relation's function families a migration
// generates; the accessible function goes with list objects.
type relationInstalls struct {
	check, listObjects, listSubjects, expand, explain bool
}

// installs returns which of a's families are generated, following the gates
// of CollectFunctionNamesWithOptions; TestCatalog_MatchesGenerated checks
// they agree. The parent-key function has its own gate (functions).
func (b *catalogBuilder) installs(a *RelationAnalysis) relationInstalls {
	t, r := a.ObjectType, a.Relation
	check := a.Capabilities.CheckAllowed && b.c.installsCheck(t, r)
	return relationInstalls{
		check:        check,
		listObjects:  a.Capabilities.ListAllowed && b.c.installsListObjects(t, r),
		listSubjects: a.Capabilities.ListAllowed && b.c.installsListSubjects(t, r),
		expand:       check && b.expand[t][r] && b.c.installsExpand(t, r),
		explain:      check && b.explain[t][r] && b.c.installsExplain(t, r),
	}
}

// functions returns the relation's installed functions, without examples
// or edges.
func (b *catalogBuilder) functions(a *RelationAnalysis) []CatalogFunction {
	t, r := a.ObjectType, a.Relation
	in := b.installs(a)
	var out []CatalogFunction
	add := func(kind FunctionKind, public bool) {
		out = append(out, CatalogFunction{Name: RelationFunctionName(kind, t, r), Kind: kind, Public: public, Type: t, Relation: r})
	}
	if in.check {
		add(KindCheck, false)
		if b.needsNW[t][r] {
			add(KindCheckNoWildcard, false)
		}
	}
	if in.listObjects {
		add(KindListObjects, false)
	}
	if in.listSubjects {
		add(KindListSubjects, false)
	}
	if in.listObjects {
		add(KindAccessible, b.c.public(familyAccessible, t, r))
	}
	if pk, ok := b.parentKeys[t+"."+r]; ok && b.c.installsParentKey(t, r) {
		f := CatalogFunction{
			Name: pk.functionName(), Kind: KindParentKey, Public: true, Type: t, Relation: r,
			Link: pk.Link, ParentType: pk.ParentType, ParentRelations: slices.Clone(pk.ParentRelations),
		}
		out = append(out, f)
	}
	if in.expand {
		add(KindExpand, false)
	}
	if in.explain {
		add(KindExplain, false)
	}
	return out
}

func (b *catalogBuilder) relation(a *RelationAnalysis) CatalogRelation {
	t, r := a.ObjectType, a.Relation
	rel := CatalogRelation{Type: t, Relation: r, Exposed: b.c == nil || b.c.exposed[t+"."+r]}
	refs := b.references(a)
	// A type or relation name can hold '.', so the names come from the
	// referenced analysis, not from splitting the key.
	for _, ref := range refs {
		if ra := b.lookup[ref]; ra != nil {
			rel.DependsOn = append(rel.DependsOn, ra.ObjectType+"#"+ra.Relation)
		}
	}
	for _, f := range b.functions(a) {
		f.Truncated = f.Name != untruncatedName(f)
		f.Signature = functionSignature(f.Kind)
		f.Example, f.Usage = b.example(a, f)
		f.Calls = b.calls(a, f, refs)
		switch f.Kind {
		case KindParentKey:
			f.Note = fmt.Sprintf("Returns %s ids, not %s ids: filter %s rows on their %s column. It does not cover a subject that is a userset on %s itself.",
				f.ParentType, t, t, f.Link, t)
		case KindAccessible:
			f.Note = accessibleNote
		}
		rel.Functions = append(rel.Functions, f)
	}
	rel.NotInstalled = b.notInstalled(a, rel.Functions)
	for _, name := range relationDispatcherNames {
		if b.routes(name, a) {
			rel.Dispatchers = append(rel.Dispatchers, name)
			rel.Examples = append(rel.Examples, DispatcherCall{Dispatcher: name, Example: b.dispatcherExample(name, a), Usage: b.dispatcherUsage(name, a)})
		}
	}
	return rel
}

// relationDispatcherNames are the public dispatchers, in the order a
// relation lists the ones that answer for it.
var relationDispatcherNames = []string{
	"check_permission", "check_permission_nw", "check_permission_bulk", "list_accessible_objects",
	accessibleObjectsDispatcherName, "list_accessible_subjects", "expand_permission", "explain_permission",
}

// routes reports whether the dispatcher routes the relation: for a public
// one, answers for it instead of raising.
func (b *catalogBuilder) routes(name string, a *RelationAnalysis) bool {
	t, r := a.ObjectType, a.Relation
	in := b.installs(a)
	switch name {
	case "check_permission", "check_permission_nw", "check_permission_bulk":
		return in.check && b.c.public(familyCheck, t, r)
	case "check_permission_internal", "check_permission_nw_internal":
		return in.check
	case "list_accessible_objects":
		return in.listObjects && b.c.public(familyListObjects, t, r)
	case accessibleObjectsDispatcherName:
		return in.listObjects && b.c.public(familyAccessible, t, r)
	case "list_accessible_subjects":
		return in.listSubjects && b.c.public(familyListSubjects, t, r)
	case listSubjectsInternalDispatcherName:
		return in.listSubjects
	case "explain_permission":
		return in.explain && b.c.public(familyExplain, t, r)
	case "explain_permission_internal":
		return in.explain
	case "expand_permission":
		return in.expand && b.c.public(familyExpand, t, r)
	case "expand_permission_internal":
		return in.expand
	}
	return false
}

func (b *catalogBuilder) has(fns []CatalogFunction, kind FunctionKind) bool {
	return slices.ContainsFunc(fns, func(f CatalogFunction) bool { return f.Kind == kind })
}

// references returns the relations a may read, as lookup keys, without
// duplicates and in relationReferences order.
func (b *catalogBuilder) references(a *RelationAnalysis) []string {
	var out []string
	for _, ref := range relationReferences(a) {
		if ref != a.ObjectType+"."+a.Relation && !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	return out
}

// calls returns the installed functions f's body may call: functions of
// the kinds its renderer composes, for the relation itself and the ones it
// references, and the internal dispatchers it can recurse through.
func (b *catalogBuilder) calls(a *RelationAnalysis, f CatalogFunction, refs []string) []string {
	var out []string
	add := func(name string) {
		if b.installed[name] && name != f.Name && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	each := func(self bool, name func(t, r string) string) {
		if self {
			add(name(a.ObjectType, a.Relation))
		}
		for _, ref := range refs {
			if ra := b.lookup[ref]; ra != nil {
				add(name(ra.ObjectType, ra.Relation))
			}
		}
	}
	switch f.Kind {
	case KindCheck:
		each(false, functionName)
		each(false, functionNameNoWildcard)
		out = append(out, "check_permission_internal")
	case KindCheckNoWildcard:
		each(true, functionName)
		each(false, functionNameNoWildcard)
		out = append(out, "check_permission_internal")
	case KindListObjects:
		each(true, AccessibleObjectsFunctionName)
		each(false, listObjectsFunctionName)
		each(true, functionName)
		out = append(out, "check_permission_internal")
	case KindAccessible:
		each(false, AccessibleObjectsFunctionName)
		each(true, listObjectsFunctionName)
		each(true, functionName)
		out = append(out, "check_permission_internal")
	case KindListSubjects:
		each(false, listSubjectsFunctionName)
		each(true, functionName)
		each(true, functionNameNoWildcard)
		out = append(out, "check_permission_internal", "check_permission_nw_internal", listSubjectsInternalDispatcherName)
	case KindParentKey:
		for _, pr := range f.ParentRelations {
			add(AccessibleObjectsFunctionName(f.ParentType, pr))
		}
	case KindExplain:
		each(false, explainFunctionName)
		each(true, functionName)
		out = append(out, "explain_permission_internal", "check_permission_internal")
	}
	return out
}

// notInstalled says, for each kind the relation has no function of, why.
// The no-wildcard variant and the parent-key form exist only for some
// relations, so they are listed only when the relation would have one.
func (b *catalogBuilder) notInstalled(a *RelationAnalysis, fns []CatalogFunction) []NotInstalled {
	t, r := a.ObjectType, a.Relation
	reached := b.c == nil || b.c.reach[t+"."+r]
	exposed := b.c == nil || b.c.exposed[t+"."+r]
	var out []NotInstalled
	miss := func(kind FunctionKind, reason NotInstalledReason, detail string) {
		if !b.has(fns, kind) {
			out = append(out, NotInstalled{Kind: kind, Reason: reason, Detail: detail})
		}
	}
	notExposed := "the relation is not exposed, and no exposed relation resolves through it"
	// noPath says what a relation without an access path gets from the
	// public dispatcher of family: the no-entry result, or NotExposedErrCode
	// when the exposure leaves it out, as it does every relation it does not
	// expose, whether or not it has functions.
	noPath := func(what string, family apiFamily, dispatcher, result string) string {
		if !b.c.public(family, t, r) {
			result = "raises " + NotExposedErrCode + " for it, since it is not exposed"
		}
		return fmt.Sprintf("the relation has no access path, so no %s is generated; %s %s", what, dispatcher, result)
	}
	noCheck := noPath("check function", familyCheck, "check_permission", "denies it")
	// An unreached relation reports not exposed ahead of a family that is
	// off: turning the family on, or compiling explain and expand on demand
	// (which keeps the recorded relation patterns), installs nothing for it.
	lists := func(kind FunctionKind, on bool, off string) {
		switch {
		case !a.Capabilities.ListAllowed:
			family, dispatcher := familyListObjects, "list_accessible_objects"
			switch kind {
			case KindListSubjects:
				family, dispatcher = familyListSubjects, "list_accessible_subjects"
			case KindAccessible:
				family, dispatcher = familyAccessible, accessibleObjectsDispatcherName
			}
			miss(kind, ReasonNoAccessPath, noPath("list function", family, dispatcher, "returns no rows for it"))
		case !reached:
			miss(kind, ReasonNotExposed, notExposed)
		case !on:
			miss(kind, ReasonFamilyOff, off)
		}
	}
	trace := func(kind FunctionKind, eligible, on bool, dispatcher string) {
		switch {
		case !a.Capabilities.CheckAllowed:
			miss(kind, ReasonNoAccessPath, noCheck)
		case !reached:
			miss(kind, ReasonNotExposed, notExposed)
		case kind == KindExplain && !exposed:
			miss(kind, ReasonNotExposed, "the relation is not exposed, so explain_permission raises M2003 for it, also when compiled on demand")
		case !eligible:
			miss(kind, ReasonNotEligible, fmt.Sprintf("the %s renderer does not support the relation's rewrite, so %s returns its no-entry result for it", kind, dispatcher))
		case !on:
			miss(kind, ReasonOnDemand, fmt.Sprintf("sql.functions.%s is off; the CLI and studio compile it on demand", kind))
		}
	}
	switch {
	case !a.Capabilities.CheckAllowed:
		miss(KindCheck, ReasonNoAccessPath, noCheck)
	case !reached:
		miss(KindCheck, ReasonNotExposed, notExposed)
	}
	objectsOn := b.c == nil || b.c.listObjects || b.c.accessible || b.c.parentKey
	lists(KindListObjects, objectsOn, "sql.functions.list_objects, accessible and parent_key are all off")
	lists(KindListSubjects, b.c == nil || b.c.listSubjects, "sql.functions.list_subjects is off")
	lists(KindAccessible, objectsOn, "sql.functions.list_objects, accessible and parent_key are all off")
	if _, ok := b.parentKeys[t+"."+r]; ok {
		switch {
		case !exposed:
			miss(KindParentKey, ReasonNotExposed, "parent-key functions are installed only for exposed relations")
		case b.c != nil && !b.c.parentKey:
			miss(KindParentKey, ReasonFamilyOff, "sql.functions.parent_key is off")
		}
	}
	trace(KindExpand, b.expand[t][r], b.c == nil || b.c.expand, "expand_permission")
	trace(KindExplain, b.explain[t][r], b.c == nil || b.c.explain, "explain_permission")
	return out
}

// RelationFunctionName returns the name a relation's function of kind is
// created under, for every kind but parent_key, whose name also holds the
// link, and dispatcher. It does not say whether the function is generated:
// BuildCatalog does.
func RelationFunctionName(kind FunctionKind, objectType, relation string) string {
	switch kind {
	case KindCheck:
		return functionName(objectType, relation)
	case KindCheckNoWildcard:
		return functionNameNoWildcard(objectType, relation)
	case KindListObjects:
		return listObjectsFunctionName(objectType, relation)
	case KindListSubjects:
		return listSubjectsFunctionName(objectType, relation)
	case KindAccessible:
		return AccessibleObjectsFunctionName(objectType, relation)
	case KindExpand:
		return expandFunctionName(objectType, relation)
	case KindExplain:
		return explainFunctionName(objectType, relation)
	}
	return ""
}

// untruncatedName is f's name before SafeIdentifier shortens it.
func untruncatedName(f CatalogFunction) string {
	t, r := sqldsl.Ident(f.Type), sqldsl.Ident(f.Relation)
	switch f.Kind {
	case KindCheck:
		return "check_" + t + "_" + r
	case KindCheckNoWildcard:
		return "check_" + t + "_" + r + "_nw"
	case KindListObjects:
		return "list_" + t + "_" + r + "_obj"
	case KindListSubjects:
		return "list_" + t + "_" + r + "_sub"
	case KindAccessible:
		return "accessible_" + t + "_" + r
	case KindParentKey:
		return "accessible_" + t + "_" + sqldsl.Ident(f.Relation+"_by_"+f.Link)
	case KindExpand:
		return "expand_" + t + "_" + r
	case KindExplain:
		return "explain_" + t + "_" + r
	}
	return f.Name
}

// Per-kind signatures, as generation declares them. TestCatalog_Signatures
// checks them against every CREATE FUNCTION header generation renders.
var (
	argSubjectType = Arg{Name: "p_subject_type", Type: "text"}
	argSubjectID   = Arg{Name: "p_subject_id", Type: "text"}
	argRelation    = Arg{Name: "p_relation", Type: "text"}
	argObjectType  = Arg{Name: "p_object_type", Type: "text"}
	argObjectID    = Arg{Name: "p_object_id", Type: "text"}
	argVisited     = Arg{Name: "p_visited", Type: "text[]", Default: "ARRAY[]::text[]"}
	argLimit       = Arg{Name: "p_limit", Type: "integer", Default: "NULL"}
	argAfter       = Arg{Name: "p_after", Type: "text", Default: "NULL"}
	argMaxNodes    = Arg{Name: "p_max_nodes", Type: "integer", Default: "NULL"}
	argMaxLeaf     = Arg{Name: "p_max_leaf", Type: "integer", Default: "NULL"}
	argUserset     = Arg{Name: subjectKindParam, Type: "boolean", Default: "NULL"}
	argExpandType  = Arg{Name: "p_subject_type", Type: "text", Default: "NULL"}
)

const (
	returnsObjectPage  = "TABLE(object_id text, next_cursor text)"
	returnsSubjectPage = "TABLE(subject_id text, next_cursor text)"
	returnsObjectIDs   = "TABLE(object_id text)"
)

func functionSignature(kind FunctionKind) Signature {
	switch kind {
	case KindCheck, KindCheckNoWildcard:
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argObjectID, argVisited}, Returns: "integer", Language: "plpgsql"}
	case KindListObjects:
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argLimit, argAfter}, Returns: returnsObjectPage, Language: "plpgsql", Paged: true}
	case KindListSubjects:
		return Signature{Args: []Arg{argObjectID, argSubjectType, argLimit, argAfter}, Returns: returnsSubjectPage, Language: "plpgsql", Paged: true}
	case KindAccessible:
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argUserset}, Returns: returnsObjectIDs, Language: "sql"}
	case KindParentKey:
		return Signature{Args: []Arg{argSubjectType, argSubjectID}, Returns: returnsObjectIDs, Language: "plpgsql"}
	case KindExpand:
		return Signature{Args: []Arg{argObjectID, argExpandType, argMaxLeaf}, Returns: "jsonb", Language: "plpgsql"}
	case KindExplain:
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argObjectID, argVisited, argMaxNodes}, Returns: "jsonb", Language: "plpgsql"}
	}
	return Signature{}
}

// dispatcherSignature returns a dispatcher's signature under the closure.
func dispatcherSignature(name string, c *exposureClosure) Signature {
	check := []Arg{argSubjectType, argSubjectID, argRelation, argObjectType, argObjectID}
	switch name {
	case "check_permission", "check_permission_nw":
		return Signature{Args: check, Returns: "integer", Language: "sql"}
	case "check_permission_internal", "check_permission_nw_internal":
		return Signature{Args: append(slices.Clone(check), argVisited), Returns: "integer", Language: "plpgsql"}
	case "check_permission_bulk":
		args := []Arg{
			{Name: "p_subject_types", Type: "text[]"},
			{Name: "p_subject_ids", Type: "text[]"},
			{Name: "p_relations", Type: "text[]"},
			{Name: "p_object_types", Type: "text[]"},
			{Name: "p_object_ids", Type: "text[]"},
		}
		return Signature{Args: args, Returns: "TABLE(idx integer, allowed integer)", Language: "plpgsql"}
	case "explain_permission":
		return Signature{Args: append(slices.Clone(check), argMaxNodes), Returns: "jsonb", Language: "sql"}
	case "explain_permission_internal":
		return Signature{Args: append(slices.Clone(check), argVisited, argMaxNodes), Returns: "jsonb", Language: "plpgsql"}
	case "expand_permission", "expand_permission_internal":
		lang := "plpgsql"
		if name == "expand_permission" {
			lang = "sql"
		}
		return Signature{Args: []Arg{argObjectType, argObjectID, argRelation, argExpandType, argMaxLeaf}, Returns: "jsonb", Language: lang}
	case "list_accessible_objects":
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argRelation, argObjectType, argLimit, argAfter}, Returns: returnsObjectPage, Language: "plpgsql", Paged: true}
	case "list_accessible_subjects", listSubjectsInternalDispatcherName:
		return Signature{Args: []Arg{argObjectType, argObjectID, argRelation, argSubjectType, argLimit, argAfter}, Returns: returnsSubjectPage, Language: "plpgsql", Paged: true}
	case accessibleObjectsDispatcherName:
		lang := "sql"
		if c != nil && c.accessible != c.listObjects {
			lang = "plpgsql" // routes itself; see generateAccessibleObjectsDispatcher
		}
		return Signature{Args: []Arg{argSubjectType, argSubjectID, argRelation, argObjectType}, Returns: returnsObjectIDs, Language: lang}
	case UnexposedFunctionName:
		return Signature{Args: []Arg{{Name: "p_object_type", Type: "text"}, {Name: "p_relation", Type: "text"}}, Returns: "integer", Language: "plpgsql"}
	}
	return Signature{}
}

// publicDispatcherNames are the dispatchers applications call.
var publicDispatcherNames = []string{
	"check_permission", "check_permission_nw", "check_permission_bulk", "explain_permission", "expand_permission",
	accessibleObjectsDispatcherName, "list_accessible_objects", "list_accessible_subjects",
}

// dispatcherRenderedWith maps a dispatcher to the public one whose SQL
// (one CollectDispatcherFunctions entry) also creates it.
var dispatcherRenderedWith = map[string]string{
	"check_permission_internal":        "check_permission",
	"check_permission_nw_internal":     "check_permission_nw",
	"explain_permission_internal":      "explain_permission",
	"expand_permission_internal":       "expand_permission",
	listSubjectsInternalDispatcherName: "list_accessible_subjects",
	UnexposedFunctionName:              "check_permission",
}

// sqlWhenEmpty are the internal dispatchers rendered as LANGUAGE sql when
// they route no relation.
var sqlWhenEmpty = []string{"check_permission_internal", "check_permission_nw_internal", "explain_permission_internal", "expand_permission_internal"}

func (b *catalogBuilder) dispatchers() []CatalogFunction {
	names := slices.Clone(DispatcherFunctionNames)
	if b.c != nil {
		names = append(names, UnexposedFunctionName)
	}
	out := make([]CatalogFunction, 0, len(names))
	for _, name := range names {
		f := CatalogFunction{
			Name:         name,
			Kind:         KindDispatcher,
			Public:       slices.Contains(publicDispatcherNames, name),
			Signature:    dispatcherSignature(name, b.c),
			RenderedWith: dispatcherRenderedWith[name],
		}
		var first *RelationAnalysis
		for i := range b.analyses {
			if a := &b.analyses[i]; b.routes(name, a) {
				f.Answers = append(f.Answers, a.ObjectType+"#"+a.Relation)
				if first == nil {
					first = a
				}
			}
		}
		// With nothing to route, these internal dispatchers are a LANGUAGE
		// sql body returning the deny or no-entry result.
		if slices.Contains(sqlWhenEmpty, name) && len(f.Answers) == 0 {
			f.Signature.Language = "sql"
		}
		switch {
		case name == UnexposedFunctionName:
			f.Note = "Raises " + NotExposedErrCode + "; the public dispatchers call it for a relation the model defines but does not expose."
		case first != nil:
			f.Example = b.dispatcherExample(name, first)
		}
		if first != nil {
			f.Usage = b.dispatcherUsage(name, first)
		}
		out = append(out, f)
	}
	return out
}

// accessibleNote is the Note of an accessible function: the planner inlines
// its body, which can name melange_tuples unqualified, so the example runs
// only where the calling session's search_path finds melange_tuples.
const accessibleNote = "The planner inlines it into the calling query, which resolves melange_tuples through its own search_path: put the schema holding melange_tuples on the search_path before running the example. accessible_objects is not inlined and needs no search_path."

// Example values. The ids are placeholders; the calls run with any.
const (
	exampleSubjectID = "alice"
	exampleObjectID  = "1"
	examplePageSize  = "100"
)

// subjectType is the subject type example calls use: one the relation
// allows, or "user".
func (b *catalogBuilder) subjectType(a *RelationAnalysis) string {
	if a != nil {
		for _, st := range a.AllowedSubjectTypes {
			if st != "" {
				return st
			}
		}
	}
	return "user"
}

// qualify returns name as an example calls it.
func (b *catalogBuilder) qualify(name string) string {
	if b.schema == "" || b.schema == "public" {
		return name
	}
	return sqldsl.QuoteIdent(b.schema) + "." + name
}

// quoteArgs returns values as SQL string literals.
func quoteArgs(values ...string) string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = sqldsl.QuoteLiteral(v)
	}
	return strings.Join(out, ", ")
}

// example returns a call of f that runs as written (an accessible function
// also needs accessibleNote's search_path), and how an application
// uses the result. Arguments follow each kind's own order: check, explain,
// accessible and list_objects take the subject first, list_subjects and
// expand the object.
func (b *catalogBuilder) example(a *RelationAnalysis, f CatalogFunction) (example, usage string) {
	st := b.subjectType(a)
	fn := b.qualify(f.Name)
	switch f.Kind {
	case KindCheck, KindCheckNoWildcard, KindExplain:
		return fmt.Sprintf("SELECT %s(%s);", fn, quoteArgs(st, exampleSubjectID, exampleObjectID)), ""
	case KindListObjects:
		return fmt.Sprintf("SELECT object_id, next_cursor FROM %s(%s, %s, NULL);", fn, quoteArgs(st, exampleSubjectID), examplePageSize), ""
	case KindListSubjects:
		return fmt.Sprintf("SELECT subject_id, next_cursor FROM %s(%s, %s, NULL);", fn, quoteArgs(exampleObjectID, st), examplePageSize), ""
	case KindAccessible:
		return fmt.Sprintf("SELECT object_id FROM %s(%s);", fn, quoteArgs(st, exampleSubjectID)),
			fmt.Sprintf("WHERE id IN (SELECT object_id FROM %s(%s))", fn, quoteArgs(st, exampleSubjectID))
	case KindParentKey:
		return fmt.Sprintf("SELECT object_id FROM %s(%s);", fn, quoteArgs(st, exampleSubjectID)),
			fmt.Sprintf("WHERE <%s column> IN (SELECT object_id FROM %s(%s))", f.Link, fn, quoteArgs(st, exampleSubjectID))
	case KindExpand:
		return fmt.Sprintf("SELECT %s(%s);", fn, quoteArgs(exampleObjectID)), ""
	}
	return "", ""
}

// dispatcherExample returns a call of the dispatcher for relation a.
func (b *catalogBuilder) dispatcherExample(name string, a *RelationAnalysis) string {
	st, t, r := b.subjectType(a), a.ObjectType, a.Relation
	fn := b.qualify(name)
	switch name {
	case "check_permission", "check_permission_nw", "check_permission_internal", "check_permission_nw_internal", "explain_permission", "explain_permission_internal":
		return fmt.Sprintf("SELECT %s(%s);", fn, quoteArgs(st, exampleSubjectID, r, t, exampleObjectID))
	case "check_permission_bulk":
		arr := func(v string) string { return "ARRAY[" + sqldsl.QuoteLiteral(v) + "]" }
		return fmt.Sprintf("SELECT idx, allowed FROM %s(%s, %s, %s, %s, %s);", fn, arr(st), arr(exampleSubjectID), arr(r), arr(t), arr(exampleObjectID))
	case "expand_permission", "expand_permission_internal":
		return fmt.Sprintf("SELECT %s(%s);", fn, quoteArgs(t, exampleObjectID, r))
	case "list_accessible_objects":
		return fmt.Sprintf("SELECT object_id, next_cursor FROM %s(%s, %s, NULL);", fn, quoteArgs(st, exampleSubjectID, r, t), examplePageSize)
	case accessibleObjectsDispatcherName:
		return fmt.Sprintf("SELECT object_id FROM %s(%s);", fn, quoteArgs(st, exampleSubjectID, r, t))
	case "list_accessible_subjects", listSubjectsInternalDispatcherName:
		return fmt.Sprintf("SELECT subject_id, next_cursor FROM %s(%s, %s, NULL);", fn, quoteArgs(t, exampleObjectID, r, st), examplePageSize)
	}
	return ""
}

// dispatcherUsage returns how an application uses the dispatcher's result
// for relation a, for the one dispatcher that has such a form.
func (b *catalogBuilder) dispatcherUsage(name string, a *RelationAnalysis) string {
	if name != accessibleObjectsDispatcherName {
		return ""
	}
	return fmt.Sprintf("WHERE id IN (SELECT object_id FROM %s(%s))", b.qualify(name), quoteArgs(b.subjectType(a), exampleSubjectID, a.Relation, a.ObjectType))
}

// collisions returns the names more than one function has.
func collisions(cat *Catalog) []NameCollision {
	owners := make(map[string][]string)
	var order []string
	own := func(name, owner string) {
		if _, ok := owners[name]; !ok {
			order = append(order, name)
		}
		owners[name] = append(owners[name], owner)
	}
	for _, r := range cat.Relations {
		for _, f := range r.Functions {
			own(f.Name, r.Type+"#"+r.Relation+" ("+string(f.Kind)+")")
		}
	}
	for _, f := range cat.Dispatchers {
		own(f.Name, "dispatcher")
	}
	var out []NameCollision
	for _, name := range order {
		if len(owners[name]) > 1 {
			out = append(out, NameCollision{Name: name, Owners: owners[name]})
		}
	}
	return out
}
