package schema

import "errors"

// ErrCyclicSchema is returned when the schema contains a cycle in the relation graph.
var ErrCyclicSchema = errors.New("melange/schema: cyclic schema")

// IsCyclicSchemaErr returns true if err is or wraps ErrCyclicSchema.
func IsCyclicSchemaErr(err error) bool {
	return errors.Is(err, ErrCyclicSchema)
}
