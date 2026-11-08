package sqlgen

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
