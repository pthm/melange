package sqlgen

// =============================================================================
// List Subjects Dispatcher Render
// =============================================================================
// RenderListSubjectsDispatcher renders the list_accessible_subjects dispatcher function.
func RenderListSubjectsDispatcher(analyses []RelationAnalysis) (string, error) {
	var cases []ListDispatcherCase
	for _, a := range analyses {
		if !a.Capabilities.ListAllowed {
			continue
		}
		cases = append(cases, ListDispatcherCase{
			ObjectType:   a.ObjectType,
			Relation:     a.Relation,
			FunctionName: listSubjectsFunctionName(a.ObjectType, a.Relation),
		})
	}

	return renderListDispatcher("list_accessible_subjects", ListSubjectsDispatcherArgs(), ListSubjectsReturns(), cases), nil
}
