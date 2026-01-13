package sqlgen

// CheckPermissionExpr returns a typed expression for a check_permission call.
func CheckPermissionExpr(functionName string, subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermissionCall{
		FunctionName: functionName,
		Subject:      subject,
		Relation:     relation,
		Object:       object,
		ExpectAllow:  expect,
	}
}

// CheckPermissionInternalExpr returns a typed expression for check_permission_internal.
func CheckPermissionInternalExpr(subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermission{
		Subject:     subject,
		Relation:    relation,
		Object:      object,
		ExpectAllow: expect,
	}
}
