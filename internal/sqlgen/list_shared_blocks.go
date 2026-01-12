package sqlgen

// =============================================================================
// List Blocks Layer (Shared Types)
// =============================================================================

// TypedQueryBlock represents a query with optional comments using typed DSL.
// Unlike QueryBlock which uses SQL string, this uses SelectStmt for DSL-first building.
type TypedQueryBlock struct {
	Comments []string   // Comment lines (without -- prefix)
	Query    SelectStmt // The query as typed DSL
}

// BlockSet contains the query blocks for a list function.
// This separates primary and secondary query paths for rendering flexibility.
type BlockSet struct {
	// Primary contains the main query blocks (UNION'd together)
	Primary []TypedQueryBlock

	// Secondary contains optional secondary path blocks (e.g., userset filter path)
	Secondary []TypedQueryBlock

	// SecondarySelf is an optional self-candidate block for userset filter
	SecondarySelf *TypedQueryBlock
}

// HasSecondary returns true if there are secondary blocks.
func (b BlockSet) HasSecondary() bool {
	return len(b.Secondary) > 0 || b.SecondarySelf != nil
}

// AllSecondary returns all secondary blocks including the self block.
func (b BlockSet) AllSecondary() []TypedQueryBlock {
	if b.SecondarySelf == nil {
		return b.Secondary
	}
	return append(b.Secondary, *b.SecondarySelf)
}
