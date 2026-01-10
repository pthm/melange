package sqlgen

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

// SubjectFromCol creates a SubjectRef from table columns.
// Uses <table>.subject_type and <table>.subject_id.
func SubjectFromCol(table string) SubjectRef {
	return SubjectRef{
		Type: Col{Table: table, Column: "subject_type"},
		ID:   Col{Table: table, Column: "subject_id"},
	}
}

// ObjectRef represents an object reference (type + id).
// An object is always identified by a type and an id.
type ObjectRef struct {
	Type Expr
	ID   Expr
}

// ObjectParams creates an ObjectRef from the standard function parameters.
func ObjectParams() ObjectRef {
	return ObjectRef{
		Type: ObjectType,
		ID:   ObjectID,
	}
}

// ObjectFromCol creates an ObjectRef from table columns.
// Uses <table>.object_type and <table>.object_id.
func ObjectFromCol(table string) ObjectRef {
	return ObjectRef{
		Type: Col{Table: table, Column: "object_type"},
		ID:   Col{Table: table, Column: "object_id"},
	}
}

// SubjectAsObject creates an ObjectRef from a tuple's subject columns.
// Useful for following relationships where the subject is the next object.
// Uses <table>.subject_type and <table>.subject_id.
func SubjectAsObject(table string) ObjectRef {
	return ObjectRef{
		Type: Col{Table: table, Column: "subject_type"},
		ID:   Col{Table: table, Column: "subject_id"},
	}
}

// LiteralObject creates an ObjectRef with literal type and expression ID.
func LiteralObject(objectType string, id Expr) ObjectRef {
	return ObjectRef{
		Type: Lit(objectType),
		ID:   id,
	}
}

// LiteralSubject creates a SubjectRef with literal type and expression ID.
func LiteralSubject(subjectType string, id Expr) SubjectRef {
	return SubjectRef{
		Type: Lit(subjectType),
		ID:   id,
	}
}
