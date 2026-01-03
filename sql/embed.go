// Package sql provides embedded SQL files for Melange infrastructure.
package sql

import (
	_ "embed"
)

// Embedded SQL files for Melange infrastructure.
// These are applied idempotently by the migrator.
//
// The SQL is embedded at compile time, ensuring the application binary
// contains all necessary schema components. This eliminates runtime
// dependencies on external SQL files.

// ModelSQL contains the melange_model table definition and indexes.
// Applied via CREATE TABLE IF NOT EXISTS for idempotence.
//
//go:embed model.sql
var ModelSQL string

// FunctionsSQL contains the PostgreSQL functions for permission checking:
//   - check_permission: evaluates if subject has relation on object
//   - list_accessible_objects: returns all objects subject has relation on
//   - has_tuple: checks for direct tuple existence
//
// Applied via CREATE OR REPLACE FUNCTION for idempotence.
//
//go:embed functions.sql
var FunctionsSQL string
