package sqlgen

import "fmt"

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
func (c CheckPermission) SQL() string {
	visited := "ARRAY[]::TEXT[]"
	if c.Visited != nil {
		visited = c.Visited.SQL()
	}
	result := "1"
	if !c.ExpectAllow {
		result = "0"
	}
	return fmt.Sprintf("check_permission_internal(%s, %s, '%s', %s, %s, %s) = %s",
		c.Subject.Type.SQL(),
		c.Subject.ID.SQL(),
		c.Relation,
		c.Object.Type.SQL(),
		c.Object.ID.SQL(),
		visited,
		result,
	)
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
func (c CheckPermissionCall) SQL() string {
	result := "1"
	if !c.ExpectAllow {
		result = "0"
	}
	return fmt.Sprintf("%s(%s, %s, '%s', %s, %s) = %s",
		c.FunctionName,
		c.Subject.Type.SQL(),
		c.Subject.ID.SQL(),
		c.Relation,
		c.Object.Type.SQL(),
		c.Object.ID.SQL(),
		result,
	)
}
