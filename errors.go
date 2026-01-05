package melange

import "errors"

// Sentinel errors for common failure modes during permission checks.
// These errors indicate setup issues, not permission denials. Permission checks
// return (false, nil) for denied access. These errors mean the authorization
// system cannot function due to missing schema components.
//
// Use the Is*Err helper functions to check for specific errors and provide
// helpful setup messages to users.
var (
	// ErrNoTuplesTable is returned when the melange_tuples relation doesn't exist.
	// This typically means the application hasn't created the view (or table/materialized view)
	// over its domain tables. See the melange documentation for view creation examples.
	ErrNoTuplesTable = errors.New("melange: melange_tuples view/table not found")

	// ErrMissingModel is returned when the melange_model table doesn't exist.
	// Run `melange migrate` to create the table and load the FGA schema.
	ErrMissingModel = errors.New("melange: melange_model table missing")

	// ErrEmptyModel is returned when the melange_model table exists but is empty.
	// This means the schema hasn't been loaded. Run `melange migrate` to
	// parse the .fga file and populate the model.
	ErrEmptyModel = errors.New("melange: authorization model empty")

	// ErrInvalidSchema is returned when schema parsing fails.
	// Check the .fga file syntax using `fga model validate` from the OpenFGA CLI.
	ErrInvalidSchema = errors.New("melange: invalid schema")

	// ErrMissingFunction is returned when a required PostgreSQL function doesn't exist.
	// Run `melange migrate` to create the check_permission and list_accessible_* functions.
	ErrMissingFunction = errors.New("melange: authorization function missing")

	// ErrContextualTuplesUnsupported is returned when contextual tuples are used
	// with a Checker that cannot execute statements on a single connection.
	ErrContextualTuplesUnsupported = errors.New("melange: contextual tuples require *sql.DB, *sql.Tx, or *sql.Conn")

	// ErrInvalidContextualTuple is returned when contextual tuples fail validation.
	ErrInvalidContextualTuple = errors.New("melange: contextual tuple invalid")

	// ErrCyclicSchema is returned when the schema contains a cycle in the relation graph.
	// Cycles in implied-by or parent relations would cause infinite recursion at runtime.
	// Fix the schema by removing one of the relationships forming the cycle.
	ErrCyclicSchema = errors.New("melange: cyclic schema")
)

// IsNoTuplesTableErr returns true if err is or wraps ErrNoTuplesTable.
func IsNoTuplesTableErr(err error) bool {
	return errors.Is(err, ErrNoTuplesTable)
}

// IsMissingModelErr returns true if err is or wraps ErrMissingModel.
func IsMissingModelErr(err error) bool {
	return errors.Is(err, ErrMissingModel)
}

// IsEmptyModelErr returns true if err is or wraps ErrEmptyModel.
func IsEmptyModelErr(err error) bool {
	return errors.Is(err, ErrEmptyModel)
}

// IsInvalidSchemaErr returns true if err is or wraps ErrInvalidSchema.
func IsInvalidSchemaErr(err error) bool {
	return errors.Is(err, ErrInvalidSchema)
}

// IsMissingFunctionErr returns true if err is or wraps ErrMissingFunction.
func IsMissingFunctionErr(err error) bool {
	return errors.Is(err, ErrMissingFunction)
}

// IsCyclicSchemaErr returns true if err is or wraps ErrCyclicSchema.
func IsCyclicSchemaErr(err error) bool {
	return errors.Is(err, ErrCyclicSchema)
}

// PostgreSQL error codes for error mapping.
// These codes are used in checkPermission to detect missing schema components
// and wrap them in sentinel errors for easier application-level handling.
const (
	pgUndefinedTable    = "42P01" // undefined_table
	pgUndefinedFunction = "42883" // undefined_function

	// Custom Melange error codes (must not conflict with PostgreSQL codes)
	// These are prefixed with 'M' to distinguish them from PG error codes.
	pgResolutionTooComplex = "M2002" // resolution depth exceeded
)

// OpenFGA error codes for compatibility with the OpenFGA API.
// These are used in ValidationError to provide OpenFGA-compatible error responses.
const (
	// ErrorCodeValidation indicates an invalid request (bad relation, type, etc.).
	ErrorCodeValidation = 2000

	// ErrorCodeAuthorizationModelNotFound indicates the model doesn't exist.
	ErrorCodeAuthorizationModelNotFound = 2001

	// ErrorCodeResolutionTooComplex indicates depth/complexity exceeded.
	ErrorCodeResolutionTooComplex = 2002
)

// ValidationError represents an OpenFGA-compatible validation error.
// It contains an error code and message matching OpenFGA's error semantics.
type ValidationError struct {
	// Code is the OpenFGA error code (e.g., 2000 for validation errors).
	Code int

	// Message describes the validation failure.
	Message string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	return e.Message
}

// ErrorCode returns the OpenFGA error code.
func (e *ValidationError) ErrorCode() int {
	return e.Code
}

// IsValidationError returns true if err is or wraps a ValidationError.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// GetValidationErrorCode extracts the error code from a ValidationError.
// Returns 0 if err is not a ValidationError.
func GetValidationErrorCode(err error) int {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve.Code
	}
	return 0
}
