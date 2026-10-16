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

	// UsersetSubjectOnly marks a block that can only match a userset query
	// subject (group:g#member). Renderers keep such blocks out of the query
	// for plain subjects, so that hot path is not slowed by blocks that
	// cannot match.
	UsersetSubjectOnly bool
}

// withoutUsersetSubjectBlocks drops UsersetSubjectOnly blocks, reporting
// whether any were present.
func withoutUsersetSubjectBlocks(blocks []TypedQueryBlock) ([]TypedQueryBlock, bool) {
	plain := make([]TypedQueryBlock, 0, len(blocks))
	for _, b := range blocks {
		if !b.UsersetSubjectOnly {
			plain = append(plain, b)
		}
	}
	return plain, len(plain) != len(blocks)
}

// listObjectsBodyBySubjectKind returns the function body: the plain-subject
// query, or, when usersetQuery is non-empty, a branch that runs it only for
// userset query subjects.
func listObjectsBodyBySubjectKind(plainQuery, usersetQuery string) []Stmt {
	if usersetQuery == "" {
		return []Stmt{ReturnQuery{Query: plainQuery}}
	}
	return []Stmt{If{
		Cond: HasUserset{Source: SubjectID},
		Then: []Stmt{ReturnQuery{Query: usersetQuery}},
		Else: []Stmt{ReturnQuery{Query: plainQuery}},
	}}
}

// BlockSet contains query blocks for a list function.
type BlockSet struct {
	Primary       []TypedQueryBlock
	Secondary     []TypedQueryBlock
	SecondarySelf *TypedQueryBlock
}
