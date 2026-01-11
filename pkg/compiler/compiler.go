// Package compiler provides public APIs for compiling OpenFGA schemas to SQL.
//
// This is a thin wrapper around internal/sqlgen that exposes only the public
// types and functions needed by external consumers. For migration functionality,
// use pkg/migrator instead.
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

// BuildInlineSQLData builds inline SQL data from closure and analyses.
var BuildInlineSQLData = sqlgen.BuildInlineSQLData
