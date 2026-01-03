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
	// ErrNoTuplesTable is returned when the melange_tuples view doesn't exist.
	// This typically means the application hasn't created the view over its
	// domain tables. See the melange documentation for view creation examples.
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

// PostgreSQL error codes for error mapping.
// These codes are used in checkPermission to detect missing schema components
// and wrap them in sentinel errors for easier application-level handling.
const (
	pgUndefinedTable    = "42P01" // undefined_table
	pgUndefinedFunction = "42883" // undefined_function
)
