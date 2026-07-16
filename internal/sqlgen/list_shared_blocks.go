package sqlgen

// TypedQueryBlock represents a query with optional comments.
// Uses SelectStmt for type-safe DSL construction.
type TypedQueryBlock struct {
	Comments []string
	Query    SelectStmt

	// Propagatable indicates whether results from this block should seed
	// the recursive step in a recursive CTE. Only results from relations
	// that participate in self-referential TTU patterns should propagate.
	// For example, with "can_view: viewer or folder_viewer" where only
	// viewer has "viewer from parent", blocks matching folder_viewer
	// should NOT propagate through the parent chain.
	Propagatable bool
}

// BlockSet contains query blocks for a list function.
type BlockSet struct {
	Primary       []TypedQueryBlock
	Secondary     []TypedQueryBlock
	SecondarySelf *TypedQueryBlock
}
