package sqlgen

// TypedQueryBlock represents a query with optional comments.
// Uses SelectStmt for type-safe DSL construction.
type TypedQueryBlock struct {
	Comments []string
	Query    SelectStmt
}

// BlockSet contains query blocks for a list function.
type BlockSet struct {
	Primary       []TypedQueryBlock
	Secondary     []TypedQueryBlock
	SecondarySelf *TypedQueryBlock
}
