package sqlgen

// CheckPermission represents a call to check_permission_internal.
type CheckPermission struct {
	Subject     SubjectRef
	Relation    string
	Object      ObjectRef
	Visited     Expr // nil uses empty array
	ExpectAllow bool // true compares "= 1", false compares "= 0"
}

func (c CheckPermission) SQL() string {
	visited := c.Visited
	if visited == nil {
		visited = EmptyArray{}
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
		Value: expectValue(c.ExpectAllow),
	}.SQL()
}

// CheckAccess creates a CheckPermission that expects access to be allowed.
func CheckAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: true,
	}
}

// CheckNoAccess creates a CheckPermission that expects access to be denied.
func CheckNoAccess(relation, objectType string, objectID Expr) CheckPermission {
	return CheckPermission{
		Subject:     SubjectParams(),
		Relation:    relation,
		Object:      LiteralObject(objectType, objectID),
		ExpectAllow: false,
	}
}

// CheckPermissionCall represents a call to a specialized permission check function.
type CheckPermissionCall struct {
	FunctionName string
	Subject      SubjectRef
	Relation     string
	Object       ObjectRef
	ExpectAllow  bool
}

func (c CheckPermissionCall) SQL() string {
	return FuncCallEq{
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
