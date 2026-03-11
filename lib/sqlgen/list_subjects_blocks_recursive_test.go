package sqlgen

import (
	"slices"
	"testing"
)

func TestCollectParentSatisfyingRelations(t *testing.T) {
	tests := []struct {
		name     string
		plan     ListPlan
		parent   ListParentRelationData
		expected []string
	}{
		{
			name: "nil lookup falls back to relation name",
			plan: ListPlan{AnalysisLookup: nil},
			parent: ListParentRelationData{
				Relation:                 "can_read",
				AllowedLinkingTypesSlice: []string{"organization"},
			},
			expected: []string{"can_read"},
		},
		{
			name: "implied relation expands to satisfying relations",
			plan: ListPlan{
				AnalysisLookup: map[string]*RelationAnalysis{
					"organization.can_read": {
						SatisfyingRelations: []string{"can_read", "member"},
					},
				},
			},
			parent: ListParentRelationData{
				Relation:                 "can_read",
				AllowedLinkingTypesSlice: []string{"organization"},
			},
			expected: []string{"can_read", "member"},
		},
		{
			name: "direct relation returns just itself",
			plan: ListPlan{
				AnalysisLookup: map[string]*RelationAnalysis{
					"organization.viewer": {
						SatisfyingRelations: []string{"viewer"},
					},
				},
			},
			parent: ListParentRelationData{
				Relation:                 "viewer",
				AllowedLinkingTypesSlice: []string{"organization"},
			},
			expected: []string{"viewer"},
		},
		{
			name: "multiple parent types deduplicates satisfying relations",
			plan: ListPlan{
				AnalysisLookup: map[string]*RelationAnalysis{
					"organization.can_read": {
						SatisfyingRelations: []string{"can_read", "member"},
					},
					"team.can_read": {
						SatisfyingRelations: []string{"can_read", "participant"},
					},
				},
			},
			parent: ListParentRelationData{
				Relation:                 "can_read",
				AllowedLinkingTypesSlice: []string{"organization", "team"},
			},
			expected: []string{"can_read", "member", "participant"},
		},
		{
			name: "missing parent type in lookup falls back to relation name",
			plan: ListPlan{
				AnalysisLookup: map[string]*RelationAnalysis{},
			},
			parent: ListParentRelationData{
				Relation:                 "can_read",
				AllowedLinkingTypesSlice: []string{"organization"},
			},
			expected: []string{"can_read"},
		},
		{
			name: "empty AllowedLinkingTypesSlice returns relation name",
			plan: ListPlan{
				AnalysisLookup: map[string]*RelationAnalysis{
					"organization.can_read": {
						SatisfyingRelations: []string{"can_read", "member"},
					},
				},
			},
			parent: ListParentRelationData{
				Relation:                 "can_read",
				AllowedLinkingTypesSlice: []string{},
			},
			expected: []string{"can_read"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectParentSatisfyingRelations(tt.plan, tt.parent)
			if !slices.Equal(got, tt.expected) {
				t.Errorf("got %v, want %v", got, tt.expected)
			}
		})
	}
}
