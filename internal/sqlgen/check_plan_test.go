package sqlgen

import (
	"testing"
)

// TestBuildCheckPlan_BackwardCompatWrapper covers the 4-arg BuildCheckPlan
// wrapper, which delegates to BuildCheckPlanWithOrdering with a nil
// complexity index. The wrapper exists to keep the original public signature
// compatible — direct callers of the new ordering-aware API use the 5-arg
// form. This test pins the wrapper so it stays exercised even if no internal
// caller uses it.
func TestBuildCheckPlan_BackwardCompatWrapper(t *testing.T) {
	a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true}, true)
	a.SatisfyingRelations = []string{"viewer"}

	plan := BuildCheckPlan(a, InlineSQLData{}, "", false)

	if plan.ObjectType != "doc" || plan.Relation != "viewer" {
		t.Errorf("plan identity wrong: %s.%s", plan.ObjectType, plan.Relation)
	}
	if plan.ComplexityByRelation != nil {
		t.Errorf("4-arg BuildCheckPlan must pass nil ordering map; got %v",
			plan.ComplexityByRelation)
	}
	if !plan.HasDirect {
		t.Error("plan should have HasDirect=true from input features")
	}
}

// TestBuildCheckPlan_NoWildcardVariant exercises the noWildcard flag, which
// changes the generated function name and dispatcher routing. This is a
// thin smoke test that confirms the variant flag flows through to the plan.
func TestBuildCheckPlan_NoWildcardVariant(t *testing.T) {
	a := mkAnalysis("doc", "viewer", RelationFeatures{HasDirect: true, HasWildcard: true}, false)
	a.SatisfyingRelations = []string{"viewer"}

	standard := BuildCheckPlan(a, InlineSQLData{}, "", false)
	noWildcard := BuildCheckPlan(a, InlineSQLData{}, "", true)

	if standard.NoWildcard {
		t.Error("standard plan should have NoWildcard=false")
	}
	if !noWildcard.NoWildcard {
		t.Error("noWildcard variant should have NoWildcard=true")
	}
	if standard.FunctionName == noWildcard.FunctionName {
		t.Errorf("noWildcard variant should produce a distinct function name; got both %q",
			standard.FunctionName)
	}
	// AllowWildcard should reflect the combination of features.HasWildcard and !noWildcard.
	if !standard.AllowWildcard {
		t.Error("standard plan with HasWildcard feature should AllowWildcard=true")
	}
	if noWildcard.AllowWildcard {
		t.Error("noWildcard variant must AllowWildcard=false even with HasWildcard feature")
	}
}
