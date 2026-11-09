package sqlgen

// CheckPermission is a check that Subject holds Relation on Object, compared
// to 1 (allow) or 0 (deny).
//
// It calls the specialized check function for the pair when Graph resolves
// one (relationGraph.checkFunction): the object type is a literal, or the
// enclosing query restricts it to FilteredType. That is the function the
// internal dispatcher's arm for the pair would run, with the same arguments,
// and every check function raises M2002 itself as the dispatcher does, so the
// direct call answers exactly as the dispatcher would. Otherwise (no Graph, a
// dynamic type, or a pair with no check function) it calls the internal
// dispatcher.
type CheckPermission struct {
	Schema      string
	Subject     SubjectRef
	Relation    string
	Object      ObjectRef
	Visited     Expr // nil uses empty array
	ExpectAllow bool // true compares "= 1", false compares "= 0"

	// Graph resolves the specialized function. Nil keeps the dispatcher.
	Graph *relationGraph
	// NoWildcard checks through check_permission_nw_internal, or the
	// function it routes the pair to.
	NoWildcard bool
	// FilteredType is the one type the enclosing query restricts a dynamic
	// Object.Type to, in a separate condition (link.subject_type IN ('t')).
	// The call then routes as if Object.Type were that literal.
	FilteredType string
}

func (c CheckPermission) SQL() string {
	call := FuncCallEq{Schema: c.Schema, Value: expectValue(c.ExpectAllow)}

	if fn, ok := c.specializedFunction(); ok {
		call.FuncName = fn
		call.Args = []Expr{c.Subject.Type, c.Subject.ID, c.Object.ID}
		if _, empty := c.Visited.(EmptyArray); c.Visited != nil && !empty {
			call.Args = append(call.Args, c.Visited)
		}
		return call.SQL()
	}

	visited := c.Visited
	if visited == nil {
		visited = EmptyArray{}
	}
	call.FuncName = "check_permission_internal"
	if c.NoWildcard {
		call.FuncName = "check_permission_nw_internal"
	}
	call.Args = []Expr{c.Subject.Type, c.Subject.ID, Lit(c.Relation), c.Object.Type, c.Object.ID, visited}
	return call.SQL()
}

// specializedFunction returns the check function the internal dispatcher
// would route this call to, when the object type is known when the SQL is
// generated.
func (c CheckPermission) specializedFunction() (string, bool) {
	objectType := c.FilteredType
	if lit, ok := c.Object.Type.(Lit); ok {
		objectType = string(lit)
	}
	if objectType == "" {
		return "", false
	}
	return c.Graph.checkFunction(objectType, c.Relation, c.NoWildcard)
}

// singleType returns the only entry of types, the types an IN condition on a
// dynamic object type allows, or "" when there are none or several. The
// result is a CheckPermission.FilteredType.
func singleType(types []string) string {
	if len(types) == 1 {
		return types[0]
	}
	return ""
}

// CheckAccess creates a CheckPermission that expects access to be allowed.
func CheckAccess(databaseSchema, relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: true,
	}
}

// CheckNoAccess creates a CheckPermission that expects access to be denied.
func CheckNoAccess(databaseSchema, relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: false,
	}
}

// CheckPermissionCall represents a call to a specialized permission check function.
type CheckPermissionCall struct {
	Schema       string
	FunctionName string
	Subject      SubjectRef
	Relation     string
	Object       ObjectRef
	ExpectAllow  bool
}

func (c CheckPermissionCall) SQL() string {
	return FuncCallEq{
		Schema:   c.Schema,
		FuncName: c.FunctionName,
		Args: []Expr{
			c.Subject.Type,
			c.Subject.ID,
			Lit(c.Relation),
			c.Object.Type,
			c.Object.ID,
		},
		Value: expectValue(c.ExpectAllow),
	}.SQL()
}

// expectValue returns Int(1) for allow, Int(0) for deny.
func expectValue(allow bool) Expr {
	if allow {
		return Int(1)
	}
	return Int(0)
}
