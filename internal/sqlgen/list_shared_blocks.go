package sqlgen

import "slices"

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

	// Indirect marks a block whose objects the subject reaches through one
	// resolution step (a userset expansion or a cross-type parent lookup), so
	// a breadth-first walk seeds them one level later, as OpenFGA counts the
	// step toward its depth limit.
	Indirect bool
}

// withoutUsersetSubjectBlocks drops the blocks that only match userset
// subjects, UsersetSubjectOnly or guarded on one, reporting whether any were
// present.
func withoutUsersetSubjectBlocks(blocks []TypedQueryBlock) ([]TypedQueryBlock, bool) {
	plain := make([]TypedQueryBlock, 0, len(blocks))
	for _, b := range blocks {
		if !b.UsersetSubjectOnly && !requiresUsersetSubject(b.Query.Where) {
			plain = append(plain, b)
		}
	}
	return plain, len(plain) != len(blocks)
}

// requiresUsersetSubject reports whether where is a conjunction that already
// requires a userset query subject, as the self-candidate blocks' do.
func requiresUsersetSubject(where Expr) bool {
	and, ok := where.(AndExpr)
	return ok && slices.Contains(and.Exprs, Expr(HasUserset{Source: SubjectID}))
}

// BlockSet contains query blocks for a list function.
type BlockSet struct {
	Primary       []TypedQueryBlock
	Secondary     []TypedQueryBlock
	SecondarySelf *TypedQueryBlock
}
