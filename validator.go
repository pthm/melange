package melange

// Validator provides schema-aware request validation without relying on
// database-backed model tables.
type Validator interface {
	ValidateUsersetSubject(subject Object) error
	ValidateCheckRequest(subject Object, relation Relation, object Object) error
	ValidateListUsersRequest(relation Relation, object Object, subjectType ObjectType) error
	ValidateContextualTuple(tuple ContextualTuple) error
}

// WithValidator supplies a schema-aware validator for request validation.
func WithValidator(v Validator) Option {
	return func(ch *Checker) {
		ch.validator = v
	}
}
