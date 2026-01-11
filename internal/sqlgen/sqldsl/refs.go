package sqldsl

// SubjectRef represents a subject reference (type + id).
// A subject is always identified by a type and an id.
type SubjectRef struct {
	Type Expr
	ID   Expr
}

// SubjectParams creates a SubjectRef from the standard function parameters.
func SubjectParams() SubjectRef {
	return SubjectRef{
		Type: SubjectType,
		ID:   SubjectID,
	}
}

// ObjectRef represents an object reference (type + id).
// An object is always identified by a type and an id.
type ObjectRef struct {
	Type Expr
	ID   Expr
}

// LiteralObject creates an ObjectRef with literal type and expression ID.
func LiteralObject(objectType string, id Expr) ObjectRef {
	return ObjectRef{
		Type: Lit(objectType),
		ID:   id,
	}
}
