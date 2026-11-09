package sqlgen

// CheckPermissionInternalExpr returns a typed CheckPermission expression,
// routed through g (see CheckPermission; g may be nil).
func CheckPermissionInternalExpr(databaseSchema string, g *relationGraph, subject SubjectRef, relation string, object ObjectRef, expect bool) Expr {
	return CheckPermission{
		Schema:      databaseSchema,
		Subject:     subject,
		Relation:    relation,
		Object:      object,
		ExpectAllow: expect,
		Graph:       g,
	}
}
