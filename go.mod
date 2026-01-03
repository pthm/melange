module github.com/pthm/melange

go 1.25.3

// The core melange module has no external dependencies beyond the standard library.
// This keeps the runtime footprint minimal for applications that only need
// permission checking.
//
// For schema parsing and code generation, use github.com/pthm/melange/tooling
// which depends on the OpenFGA parser.
