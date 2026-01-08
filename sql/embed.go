// Package sql provides embedded SQL files for Melange infrastructure.
package sql

import (
	_ "embed"
	"strings"
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
//   - list_accessible_subjects: returns all subjects with access to an object
//
// Applied via CREATE OR REPLACE FUNCTION for idempotence.
// The list functions are stubs that get replaced by generated dispatchers
// during migration.
//
//go:embed functions/01_userset_helpers.sql
var functionsUsersetHelpersSQL string

//go:embed functions/03_exclusions.sql
var functionsExclusionsSQL string

//go:embed functions/04_permissions.sql
var functionsPermissionsSQL string

//go:embed functions/05_queries.sql
var functionsQueriesSQL string

// FunctionsSQLFiles lists the function SQL files in application order.
var FunctionsSQLFiles = []SQLFile{
	{Path: "functions/01_userset_helpers.sql", Contents: functionsUsersetHelpersSQL},
	{Path: "functions/03_exclusions.sql", Contents: functionsExclusionsSQL},
	{Path: "functions/04_permissions.sql", Contents: functionsPermissionsSQL},
	{Path: "functions/05_queries.sql", Contents: functionsQueriesSQL},
}

// FunctionsSQL concatenates all function SQL files for backwards compatibility.
var FunctionsSQL = strings.Join([]string{
	functionsUsersetHelpersSQL,
	functionsExclusionsSQL,
	functionsPermissionsSQL,
	functionsQueriesSQL,
}, "\n\n")

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
