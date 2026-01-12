package sqlgen

import "fmt"

// =============================================================================
// Depth Exceeded Render Functions (List Subjects)
// =============================================================================
// RenderListSubjectsDepthExceededFunction renders a list_subjects function for a relation
// that exceeds the userset depth limit. The generated function raises M2002 immediately.
func RenderListSubjectsDepthExceededFunction(plan ListPlan) string {
	fn := PlpgsqlFunction{
		Name:    plan.FunctionName,
		Args:    ListSubjectsArgs(),
		Returns: ListSubjectsReturns(),
		Header: []string{
			fmt.Sprintf("Generated list_subjects function for %s.%s", plan.ObjectType, plan.Relation),
			fmt.Sprintf("Features: %s", plan.FeaturesString()),
			fmt.Sprintf("DEPTH EXCEEDED: Userset chain depth %d exceeds 25 level limit", plan.Analysis.MaxUsersetDepth),
		},
		Body: []Stmt{
			Comment{Text: fmt.Sprintf("This relation has userset chain depth %d which exceeds the 25 level limit.", plan.Analysis.MaxUsersetDepth)},
			Comment{Text: "Raise M2002 immediately without any computation."},
			Raise{Message: "resolution too complex", ErrCode: "M2002"},
		},
	}
	return fn.SQL()
}
