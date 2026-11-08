// Package compiler provides public APIs for compiling OpenFGA schemas to SQL.
//
// It is a thin wrapper around lib/sqlgen that exposes only the types and
// functions needed by external consumers. The package covers two concerns:
//
//   - Check and list SQL generation: AnalyzeRelations, GenerateSQL,
//     GenerateListSQL, CollectFunctionNames, CollectNamedFunctions, and
//     their *WithOptions variants, which take GenerateSQLOptions (including
//     the Exposure that selects the generated SQL API).
//   - GenerateTraceSQL, which generates only the explain and expand
//     functions, into another schema, for running them on demand.
//   - Migration file generation: GenerateMigrationSQL and MigrationOptions,
//     which assemble versioned UP/DOWN SQL files from already-compiled output.
//
// For applying generated SQL directly to a database, use pkg/migrator instead.
package compiler

import (
	"github.com/pthm/melange/internal/sqlgen"
)

// GeneratedSQL contains all SQL generated from a schema for check functions.
type GeneratedSQL = sqlgen.GeneratedSQL

// ListGeneratedSQL contains all SQL generated for list functions.
type ListGeneratedSQL = sqlgen.ListGeneratedSQL

// RelationAnalysis contains the analyzed features of a relation.
type RelationAnalysis = sqlgen.RelationAnalysis

// InlineSQLData contains inline SQL data for generated functions.
type InlineSQLData = sqlgen.InlineSQLData

// GenerateSQL generates specialized check_permission functions from relation analyses.
var GenerateSQL = sqlgen.GenerateSQL

// GenerateListSQL generates specialized list functions from relation analyses.
var GenerateListSQL = sqlgen.GenerateListSQL

// AnalyzeRelations classifies all relations and gathers data needed for SQL generation.
var AnalyzeRelations = sqlgen.AnalyzeRelations

// ComputeCanGenerate computes which relations can have functions generated.
var ComputeCanGenerate = sqlgen.ComputeCanGenerate

// CollectFunctionNames returns all generated function names for tracking.
var CollectFunctionNames = sqlgen.CollectFunctionNames

// DispatcherFunctionNames lists the dispatcher functions generated for every
// model, in the order DOWN migrations drop them.
var DispatcherFunctionNames = sqlgen.DispatcherFunctionNames

// NamedFunction pairs a function name with its generated SQL body.
type NamedFunction = sqlgen.NamedFunction

// CollectNamedFunctions returns specialized functions paired with their SQL.
var CollectNamedFunctions = sqlgen.CollectNamedFunctions

// BuildInlineSQLData builds inline SQL data from closure and analyses.
var BuildInlineSQLData = sqlgen.BuildInlineSQLData

// GenerateSQLOptions tunes code generation for the *WithOptions functions.
// The zero value is the default.
type GenerateSQLOptions = sqlgen.GenerateSQLOptions

// Exposure selects the generated SQL API: the relations the public
// dispatchers answer for and the function families generated.
type Exposure = sqlgen.Exposure

// RelationPatterns selects an Exposure's exposed relations.
type RelationPatterns = sqlgen.RelationPatterns

// FunctionFamilies enables an Exposure's optional function families.
type FunctionFamilies = sqlgen.FunctionFamilies

// ValidateRelationPattern reports whether a relation pattern is valid.
var ValidateRelationPattern = sqlgen.ValidateRelationPattern

// NotExposedErrCode is the SQLSTATE public dispatchers raise for a relation
// the model defines but the exposure does not expose.
const NotExposedErrCode = sqlgen.NotExposedErrCode

// UnexposedFunctionName is the raising helper generated when an exposure
// leaves something out.
const UnexposedFunctionName = sqlgen.UnexposedFunctionName

// GenerateSQLWithOptions is GenerateSQL with options.
var GenerateSQLWithOptions = sqlgen.GenerateSQLWithOptions

// GenerateListSQLWithOptions is GenerateListSQL with options.
var GenerateListSQLWithOptions = sqlgen.GenerateListSQLWithOptions

// CollectFunctionNamesWithOptions is CollectFunctionNames with options.
var CollectFunctionNamesWithOptions = sqlgen.CollectFunctionNamesWithOptions

// CollectNamedFunctionsWithOptions is CollectNamedFunctions with options.
var CollectNamedFunctionsWithOptions = sqlgen.CollectNamedFunctionsWithOptions

// ParentKeyFunction describes a generated parent-key accessible function.
type ParentKeyFunction = sqlgen.ParentKeyFunction

// ParentKeyFunctions returns the parent-key functions generated under the
// default exposure.
var ParentKeyFunctions = sqlgen.ParentKeyFunctions

// ParentKeyFunctionsWithOptions returns the parent-key functions generated
// under opts.Exposure.
var ParentKeyFunctionsWithOptions = sqlgen.ParentKeyFunctionsWithOptions

// TraceSQL is a model's explain and expand functions with their
// dispatchers, generated apart from a migration.
type TraceSQL = sqlgen.TraceSQL

// TraceSQLOptions configures GenerateTraceSQL.
type TraceSQLOptions = sqlgen.TraceSQLOptions

// GenerateTraceSQL generates the explain and/or expand functions a
// migration with an exposure and those families on would install, created in
// a separate function schema (pg_temp, for explain and expand on demand).
var GenerateTraceSQL = sqlgen.GenerateTraceSQL
