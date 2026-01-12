package sqlgen

// =============================================================================
// List Objects Dispatcher Render
// =============================================================================
// RenderListObjectsDispatcher renders the list_accessible_objects dispatcher function.
func RenderListObjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listObjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	return renderListDispatcher("list_accessible_objects", ListObjectsDispatcherArgs(), ListObjectsReturns(), cases), nil
}
