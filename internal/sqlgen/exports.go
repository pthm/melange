// This file re-exports types from subpackages for backward compatibility.
// New code should import the subpackages directly.

package sqlgen

import (
	"github.com/pthm/melange/internal/sqlgen/analysis"
	"github.com/pthm/melange/internal/sqlgen/inline"
	"github.com/pthm/melange/internal/sqlgen/plpgsql"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/internal/sqlgen/tuples"
)

// =============================================================================
// Re-exports from sqldsl package
// =============================================================================

// Expr interface and types
type (
	Expr              = sqldsl.Expr
	Param             = sqldsl.Param
	Col               = sqldsl.Col
	Lit               = sqldsl.Lit
	Raw               = sqldsl.Raw
	Int               = sqldsl.Int
	Bool              = sqldsl.Bool
	Null              = sqldsl.Null
	EmptyArray        = sqldsl.EmptyArray
	Func              = sqldsl.Func
	Alias             = sqldsl.Alias
	Paren             = sqldsl.Paren
	Concat            = sqldsl.Concat
	Position          = sqldsl.Position
	Substring         = sqldsl.Substring
	UsersetNormalized = sqldsl.UsersetNormalized
)

// Operators
type (
	Eq        = sqldsl.Eq
	Ne        = sqldsl.Ne
	Lt        = sqldsl.Lt
	Gt        = sqldsl.Gt
	Lte       = sqldsl.Lte
	Gte       = sqldsl.Gte
	Add       = sqldsl.Add
	Sub       = sqldsl.Sub
	In        = sqldsl.In
	NotIn     = sqldsl.NotIn
	AndExpr   = sqldsl.AndExpr
	OrExpr    = sqldsl.OrExpr
	NotExpr   = sqldsl.NotExpr
	Exists    = sqldsl.Exists
	NotExists = sqldsl.NotExists
	IsNull    = sqldsl.IsNull
	IsNotNull = sqldsl.IsNotNull
)

// Table expressions
type (
	TableExpr        = sqldsl.TableExpr
	TableRef         = sqldsl.TableRef
	FunctionCallExpr = sqldsl.FunctionCallExpr
)

// SQL statements and structures
type (
	JoinClause        = sqldsl.JoinClause
	SelectStmt        = sqldsl.SelectStmt
	IntersectSubquery = sqldsl.IntersectSubquery
	ValuesTable       = sqldsl.ValuesTable
	ValuesRow         = sqldsl.ValuesRow
	TypedValuesTable  = sqldsl.TypedValuesTable
	LateralFunction   = sqldsl.LateralFunction
	SQLer             = sqldsl.SQLer
	QueryBlock        = sqldsl.QueryBlock
)

// Userset types
type (
	UsersetObjectID          = sqldsl.UsersetObjectID
	UsersetRelation          = sqldsl.UsersetRelation
	HasUserset               = sqldsl.HasUserset
	NoUserset                = sqldsl.NoUserset
	SubstringUsersetRelation = sqldsl.SubstringUsersetRelation
	IsWildcard               = sqldsl.IsWildcard
)

// Ref types
type (
	SubjectRef = sqldsl.SubjectRef
	ObjectRef  = sqldsl.ObjectRef
)

// Common parameter constants
var (
	SubjectType = sqldsl.SubjectType
	SubjectID   = sqldsl.SubjectID
	ObjectType  = sqldsl.ObjectType
	ObjectID    = sqldsl.ObjectID
	Visited     = sqldsl.Visited
)

// Function aliases
var (
	ParamRef                        = sqldsl.ParamRef
	LitText                         = sqldsl.LitText
	And                             = sqldsl.And
	Or                              = sqldsl.Or
	Not                             = sqldsl.Not
	ExistsExpr                      = sqldsl.ExistsExpr
	TableAs                         = sqldsl.TableAs
	ClosureValuesTable              = sqldsl.ClosureValuesTable
	UsersetValuesTable              = sqldsl.UsersetValuesTable
	TypedClosureValuesTable         = sqldsl.TypedClosureValuesTable
	TypedUsersetValuesTable         = sqldsl.TypedUsersetValuesTable
	ClosureTable                    = sqldsl.ClosureTable
	UsersetTable                    = sqldsl.UsersetTable
	Ident                           = sqldsl.Ident
	RenderBlocks                    = sqldsl.RenderBlocks
	RenderUnionBlocks               = sqldsl.RenderUnionBlocks
	IndentLines                     = sqldsl.IndentLines
	WrapWithPagination              = sqldsl.WrapWithPagination
	WrapWithPaginationWildcardFirst = sqldsl.WrapWithPaginationWildcardFirst
	SubjectIDMatch                  = sqldsl.SubjectIDMatch
	NormalizedUsersetSubject        = sqldsl.NormalizedUsersetSubject
	SelectAs                        = sqldsl.SelectAs
	SubjectParams                   = sqldsl.SubjectParams
	LiteralObject                   = sqldsl.LiteralObject
)

// =============================================================================
// Re-exports from analysis package
// =============================================================================

// Schema type aliases (originally from pkg/schema via analysis)
type (
	TypeDefinition      = analysis.TypeDefinition
	RelationDefinition  = analysis.RelationDefinition
	SubjectTypeRef      = analysis.SubjectTypeRef
	ClosureRow          = analysis.ClosureRow
	IntersectionGroup   = analysis.IntersectionGroup
	ParentRelationCheck = analysis.ParentRelationCheck
)

// Analysis types
type (
	RelationFeatures       = analysis.RelationFeatures
	UsersetPattern         = analysis.UsersetPattern
	ParentRelationInfo     = analysis.ParentRelationInfo
	IntersectionPart       = analysis.IntersectionPart
	IntersectionGroupInfo  = analysis.IntersectionGroupInfo
	IndirectAnchorInfo     = analysis.IndirectAnchorInfo
	AnchorPathStep         = analysis.AnchorPathStep
	RelationAnalysis       = analysis.RelationAnalysis
	GenerationCapabilities = analysis.GenerationCapabilities
	ListStrategy           = analysis.ListStrategy
)

// ListStrategy constants
const (
	ListStrategyDirect         = analysis.ListStrategyDirect
	ListStrategyUserset        = analysis.ListStrategyUserset
	ListStrategyRecursive      = analysis.ListStrategyRecursive
	ListStrategyIntersection   = analysis.ListStrategyIntersection
	ListStrategyDepthExceeded  = analysis.ListStrategyDepthExceeded
	ListStrategySelfRefUserset = analysis.ListStrategySelfRefUserset
	ListStrategyComposed       = analysis.ListStrategyComposed
)

// Analysis functions
var (
	ComputeRelationClosure = analysis.ComputeRelationClosure
	AnalyzeRelations       = analysis.AnalyzeRelations
	ComputeCanGenerate     = analysis.ComputeCanGenerate
	DetermineListStrategy  = analysis.DetermineListStrategy
	BuildAnalysisLookup    = analysis.BuildAnalysisLookup
)

// =============================================================================
// Re-exports from tuples package
// =============================================================================

type TupleQuery = tuples.TupleQuery

var Tuples = tuples.Tuples

// =============================================================================
// Re-exports from plpgsql package
// =============================================================================

type (
	FuncArg         = plpgsql.FuncArg
	Decl            = plpgsql.Decl
	Stmt            = plpgsql.Stmt
	ReturnQuery     = plpgsql.ReturnQuery
	Return          = plpgsql.Return
	Assign          = plpgsql.Assign
	If              = plpgsql.If
	RawStmt         = plpgsql.RawStmt
	Raise           = plpgsql.Raise
	Comment         = plpgsql.Comment
	PlpgsqlFunction = plpgsql.PlpgsqlFunction
)

var (
	ListObjectsArgs            = plpgsql.ListObjectsArgs
	ListSubjectsArgs           = plpgsql.ListSubjectsArgs
	ListObjectsReturns         = plpgsql.ListObjectsReturns
	ListSubjectsReturns        = plpgsql.ListSubjectsReturns
	FunctionHeader             = plpgsql.FunctionHeader
	ListObjectsFunctionHeader  = plpgsql.ListObjectsFunctionHeader
	ListSubjectsFunctionHeader = plpgsql.ListSubjectsFunctionHeader
	ListObjectsDispatcherArgs  = plpgsql.ListObjectsDispatcherArgs
	ListSubjectsDispatcherArgs = plpgsql.ListSubjectsDispatcherArgs
)

// =============================================================================
// Re-exports from inline package
// =============================================================================

type InlineSQLData = inline.InlineSQLData

var BuildInlineSQLData = inline.BuildInlineSQLData

// Typed row builders (used by tests)
var (
	BuildClosureTypedRows = inline.BuildClosureTypedRows
	BuildUsersetTypedRows = inline.BuildUsersetTypedRows
	BuildClosureValues    = inline.BuildClosureValues
)

// =============================================================================
// Internal helper re-exports (lowercase for backward compatibility)
// =============================================================================

// indentLines is a lowercase alias for IndentLines (backward compatibility).
func indentLines(input, indent string) string {
	return sqldsl.IndentLines(input, indent)
}

// sqlf is a lowercase alias for Sqlf (backward compatibility for tests).
func sqlf(format string, args ...any) string {
	return sqldsl.Sqlf(format, args...)
}

// optf is a lowercase alias for Optf (backward compatibility for tests).
func optf(cond bool, format string, args ...any) string {
	return sqldsl.Optf(cond, format, args...)
}

// buildClosureTypedRows is a lowercase alias for BuildClosureTypedRows (backward compatibility for tests).
func buildClosureTypedRows(closureRows []ClosureRow) []ValuesRow {
	return inline.BuildClosureTypedRows(closureRows)
}

// buildUsersetTypedRows is a lowercase alias for BuildUsersetTypedRows (backward compatibility for tests).
func buildUsersetTypedRows(analyses []RelationAnalysis) []ValuesRow {
	return inline.BuildUsersetTypedRows(analyses)
}

// buildClosureValues is a lowercase alias for BuildClosureValues (backward compatibility for tests).
func buildClosureValues(closureRows []ClosureRow) string {
	return inline.BuildClosureValues(closureRows)
}

// wrapWithPagination is a lowercase alias for WrapWithPagination (backward compatibility).
func wrapWithPagination(query, idColumn string) string {
	return sqldsl.WrapWithPagination(query, idColumn)
}

// wrapWithPaginationWildcardFirst is a lowercase alias for WrapWithPaginationWildcardFirst (backward compatibility).
func wrapWithPaginationWildcardFirst(query string) string {
	return sqldsl.WrapWithPaginationWildcardFirst(query)
}
