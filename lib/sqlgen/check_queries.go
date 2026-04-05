package sqlgen

// CheckPermissionExpr returns a typed expression for a check_permission call.
func CheckPermissionExpr(databaseSchema, functionName string, subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermissionCall{
		Schema:       databaseSchema,
		FunctionName: functionName,
		Subject:      subject,
		Relation:     relation,
		Object:       object,
		ExpectAllow:  expect,
	}
}

// CheckPermissionInternalExpr returns a typed expression for check_permission_internal.
func CheckPermissionInternalExpr(databaseSchema string, subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     subject,
		Relation:    relation,
		Object:      object,
		ExpectAllow: expect,
	}
}
