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

// FunctionsSQLFiles lists the function SQL files in application order.
// No generic functions are embedded; all functions are generated during migration.
var FunctionsSQLFiles = []SQLFile{}

// FunctionsSQL is retained for backwards compatibility, but is now empty.
var FunctionsSQL = ""

// ClosureSQL contains the melange_relation_closure table definition and indexes.
// This table stores the precomputed transitive closure of implied-by relations,
// enabling efficient role hierarchy resolution without recursive function calls.
//
// Applied via CREATE TABLE IF NOT EXISTS for idempotence.
//
//go:embed closure.sql
var ClosureSQL string

// SQLFile describes a named SQL payload for migration.
type SQLFile struct {
	Path     string
	Contents string
}
