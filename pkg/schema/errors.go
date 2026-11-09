package schema

import "errors"

// ErrCyclicSchema is returned when the schema contains an implied-by cycle:
// relations of one type that resolve through each other on the same object.
var ErrCyclicSchema = errors.New("melange/schema: cyclic schema")

// IsCyclicSchemaErr returns true if err is or wraps ErrCyclicSchema.
func IsCyclicSchemaErr(err error) bool {
	return errors.Is(err, ErrCyclicSchema)
}
