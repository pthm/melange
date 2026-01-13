// Re-exports types from subpackages for backward compatibility.
// New code should import the subpackages directly.

package sqlgen

import (
	"github.com/pthm/melange/internal/sqlgen/analysis"
	"github.com/pthm/melange/internal/sqlgen/inline"
	"github.com/pthm/melange/internal/sqlgen/plpgsql"
	"github.com/pthm/melange/internal/sqlgen/sqldsl"
	"github.com/pthm/melange/internal/sqlgen/tuples"
)

// sqldsl types
type (
	// Expressions
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

	// Operators
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
	CaseWhen  = sqldsl.CaseWhen
	CaseExpr  = sqldsl.CaseExpr

	// Table expressions
	TableExpr        = sqldsl.TableExpr
	TableRef         = sqldsl.TableRef
	FunctionCallExpr = sqldsl.FunctionCallExpr

	// Statements and structures
	JoinClause        = sqldsl.JoinClause
	SelectStmt        = sqldsl.SelectStmt
	IntersectSubquery = sqldsl.IntersectSubquery
	ValuesTable       = sqldsl.ValuesTable
	ValuesRow         = sqldsl.ValuesRow
	TypedValuesTable  = sqldsl.TypedValuesTable
	LateralFunction   = sqldsl.LateralFunction
	SQLer             = sqldsl.SQLer
	QueryBlock        = sqldsl.QueryBlock
	UnionAll          = sqldsl.UnionAll

	// Userset types
	UsersetObjectID          = sqldsl.UsersetObjectID
	UsersetRelation          = sqldsl.UsersetRelation
	HasUserset               = sqldsl.HasUserset
	NoUserset                = sqldsl.NoUserset
	SubstringUsersetRelation = sqldsl.SubstringUsersetRelation
	IsWildcard               = sqldsl.IsWildcard

	// Function call types
	FuncCallEq       = sqldsl.FuncCallEq
	FuncCallNe       = sqldsl.FuncCallNe
	InFunctionSelect = sqldsl.InFunctionSelect

	// Array types
	ArrayLiteral  = sqldsl.ArrayLiteral
	ArrayAppend   = sqldsl.ArrayAppend
	ArrayContains = sqldsl.ArrayContains
	ArrayLength   = sqldsl.ArrayLength

	// CTE types
	CTEDef        = sqldsl.CTEDef
	WithCTE       = sqldsl.WithCTE
	CommentedSQL  = sqldsl.CommentedSQL
	SelectIntoVar = sqldsl.SelectIntoVar

	// Ref types
	SubjectRef = sqldsl.SubjectRef
	ObjectRef  = sqldsl.ObjectRef
)

var (
	SubjectType                     = sqldsl.SubjectType
	SubjectID                       = sqldsl.SubjectID
	ObjectType                      = sqldsl.ObjectType
	ObjectID                        = sqldsl.ObjectID
	Visited                         = sqldsl.Visited
	ParamRef                        = sqldsl.ParamRef
	LitText                         = sqldsl.LitText
	And                             = sqldsl.And
	Or                              = sqldsl.Or
	Not                             = sqldsl.Not
	ExistsExpr                      = sqldsl.ExistsExpr
	TableAs                         = sqldsl.TableAs
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
	Sqlf                            = sqldsl.Sqlf
	Optf                            = sqldsl.Optf
	InternalPermissionCheckCall     = sqldsl.InternalPermissionCheckCall
	NoWildcardPermissionCheckCall   = sqldsl.NoWildcardPermissionCheckCall
	SpecializedCheckCall            = sqldsl.SpecializedCheckCall
	InternalCheckCall               = sqldsl.InternalCheckCall
	VisitedKey                      = sqldsl.VisitedKey
	VisitedWithKey                  = sqldsl.VisitedWithKey
	ListObjectsFunctionName         = sqldsl.ListObjectsFunctionName
	ListSubjectsFunctionName        = sqldsl.ListSubjectsFunctionName
	RecursiveCTE                    = sqldsl.RecursiveCTE
	SimpleCTE                       = sqldsl.SimpleCTE
	MultiCTE                        = sqldsl.MultiCTE
	MultiLineComment                = sqldsl.MultiLineComment
)

// analysis types
type (
	TypeDefinition         = analysis.TypeDefinition
	RelationDefinition     = analysis.RelationDefinition
	SubjectTypeRef         = analysis.SubjectTypeRef
	ClosureRow             = analysis.ClosureRow
	IntersectionGroup      = analysis.IntersectionGroup
	ParentRelationCheck    = analysis.ParentRelationCheck
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

const (
	ListStrategyDirect         = analysis.ListStrategyDirect
	ListStrategyUserset        = analysis.ListStrategyUserset
	ListStrategyRecursive      = analysis.ListStrategyRecursive
	ListStrategyIntersection   = analysis.ListStrategyIntersection
	ListStrategyDepthExceeded  = analysis.ListStrategyDepthExceeded
	ListStrategySelfRefUserset = analysis.ListStrategySelfRefUserset
	ListStrategyComposed       = analysis.ListStrategyComposed
)

var (
	ComputeRelationClosure = analysis.ComputeRelationClosure
	AnalyzeRelations       = analysis.AnalyzeRelations
	ComputeCanGenerate     = analysis.ComputeCanGenerate
	DetermineListStrategy  = analysis.DetermineListStrategy
	BuildAnalysisLookup    = analysis.BuildAnalysisLookup
)

// tuples types
type TupleQuery = tuples.TupleQuery

var Tuples = tuples.Tuples

// plpgsql types
type (
	FuncArg         = plpgsql.FuncArg
	Decl            = plpgsql.Decl
	Stmt            = plpgsql.Stmt
	ReturnQuery     = plpgsql.ReturnQuery
	Return          = plpgsql.Return
	ReturnValue     = plpgsql.ReturnValue
	ReturnInt       = plpgsql.ReturnInt
	Assign          = plpgsql.Assign
	If              = plpgsql.If
	SelectInto      = plpgsql.SelectInto
	RawStmt         = plpgsql.RawStmt
	Raise           = plpgsql.Raise
	Comment         = plpgsql.Comment
	PlpgsqlFunction = plpgsql.PlpgsqlFunction
	SqlFunction     = plpgsql.SqlFunction
)

var (
	ListObjectsArgs            = plpgsql.ListObjectsArgs
	ListSubjectsArgs           = plpgsql.ListSubjectsArgs
	ListObjectsReturns         = plpgsql.ListObjectsReturns
	ListSubjectsReturns        = plpgsql.ListSubjectsReturns
	ListObjectsFunctionHeader  = plpgsql.ListObjectsFunctionHeader
	ListSubjectsFunctionHeader = plpgsql.ListSubjectsFunctionHeader
	ListObjectsDispatcherArgs  = plpgsql.ListObjectsDispatcherArgs
	ListSubjectsDispatcherArgs = plpgsql.ListSubjectsDispatcherArgs
)

// inline types
type InlineSQLData = inline.InlineSQLData

var (
	BuildInlineSQLData    = inline.BuildInlineSQLData
	BuildClosureTypedRows = inline.BuildClosureTypedRows
	BuildUsersetTypedRows = inline.BuildUsersetTypedRows
)

// Package-internal lowercase aliases for pagination helpers used by render functions.
func wrapWithPagination(query, idColumn string) string {
	return sqldsl.WrapWithPagination(query, idColumn)
}

func wrapWithPaginationWildcardFirst(query string) string {
	return sqldsl.WrapWithPaginationWildcardFirst(query)
}
