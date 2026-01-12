package sqlgen

// CheckPermission represents a call to check_permission_internal.
// This is the core permission check expression used in queries.
type CheckPermission struct {
	Subject     SubjectRef
	Relation    string
	Object      ObjectRef
	Visited     Expr // nil for default empty array
	ExpectAllow bool // true = "= 1", false = "= 0"
}

// SQL renders the check_permission_internal call with comparison.
// Uses FuncCallEq internally to avoid fmt.Sprintf for SQL construction.
func (c CheckPermission) SQL() string {
	var visited Expr = EmptyArray{}
	if c.Visited != nil {
		visited = c.Visited
	}
	value := Int(1)
	if !c.ExpectAllow {
		value = Int(0)
	}
	return FuncCallEq{
		FuncName: "check_permission_internal",
		Args: []Expr{
			c.Subject.Type,
			c.Subject.ID,
			Lit(c.Relation),
			c.Object.Type,
			c.Object.ID,
			visited,
		},
		Value: value,
	}.SQL()
}

// CheckAccess creates a CheckPermission that expects access to be allowed.
// Uses SubjectParams() for subject and the given parameters for object.
func CheckAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: true,
	}
}

// CheckNoAccess creates a CheckPermission that expects access to be denied.
// Uses SubjectParams() for subject and the given parameters for object.
func CheckNoAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: false,
	}
}

// CheckPermissionCall represents a call to a custom permission check function.
// This is useful for calling specialized generated functions.
type CheckPermissionCall struct {
	FunctionName string
	Subject      SubjectRef
	Relation     string
	Object       ObjectRef
	ExpectAllow  bool
}

// SQL renders the function call with comparison.
// Uses FuncCallEq internally to avoid fmt.Sprintf for SQL construction.
func (c CheckPermissionCall) SQL() string {
	value := Int(1)
	if !c.ExpectAllow {
		value = Int(0)
	}
	return FuncCallEq{
		FuncName: c.FunctionName,
		Args: []Expr{
			c.Subject.Type,
			c.Subject.ID,
			Lit(c.Relation),
			c.Object.Type,
			c.Object.ID,
		},
		Value: value,
	}.SQL()
}
