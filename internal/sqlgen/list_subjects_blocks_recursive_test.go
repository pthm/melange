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

func TestClassifyParentRelation(t *testing.T) {
	folderParent := ListParentRelationData{
		Relation:                 "viewer",
		LinkingRelation:          "parent",
		AllowedLinkingTypesSlice: []string{"folder"},
	}

	tests := []struct {
		name string
		plan ListPlan
		want parentRelationStrategy
	}{
		{
			name: "nil AnalysisLookup defaults to closure path",
			plan: ListPlan{AnalysisLookup: nil},
			want: parentStrategyClosure,
		},
		{
			name: "missing target analysis falls to subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{}},
			want: parentStrategySubjectPool,
		},
		{
			name: "intersection at parent level forces subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					Features:   RelationFeatures{HasIntersection: true},
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "exclusion at parent level forces subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					Features:   RelationFeatures{HasExclusion: true},
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "complex userset patterns force subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType:                "folder",
					HasComplexUsersetPatterns: true,
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "self-referential same-linking parent stays on closure",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					ParentRelations: []ParentRelationInfo{{
						Relation:            "viewer",
						LinkingRelation:     "parent",
						AllowedLinkingTypes: []string{"folder"},
					}},
				},
			}},
			want: parentStrategyClosure,
		},
		{
			name: "cross-type nested TTU forces subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					ParentRelations: []ParentRelationInfo{{
						Relation:            "member",
						LinkingRelation:     "owner",
						AllowedLinkingTypes: []string{"group"},
					}},
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "self-referential but different linking relation forces subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					ParentRelations: []ParentRelationInfo{{
						Relation:            "viewer",
						LinkingRelation:     "container",
						AllowedLinkingTypes: []string{"folder"},
					}},
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "empty AllowedLinkingTypes on nested TTU forces subject_pool",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					ParentRelations: []ParentRelationInfo{{
						Relation:        "viewer",
						LinkingRelation: "parent",
					}},
				},
			}},
			want: parentStrategySubjectPool,
		},
		{
			name: "ClosureParentRelations also checked",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {
					ObjectType: "folder",
					ClosureParentRelations: []ParentRelationInfo{{
						Relation:            "member",
						LinkingRelation:     "owner",
						AllowedLinkingTypes: []string{"group"},
					}},
				},
			}},
			want: parentStrategySubjectPool,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyParentRelation(tt.plan, folderParent)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParentRelationAnalysis(t *testing.T) {
	tests := []struct {
		name    string
		plan    ListPlan
		parent  ListParentRelationData
		wantNil bool
	}{
		{
			name:    "nil lookup returns nil",
			plan:    ListPlan{AnalysisLookup: nil},
			parent:  ListParentRelationData{Relation: "viewer", AllowedLinkingTypesSlice: []string{"folder"}},
			wantNil: true,
		},
		{
			name:    "empty AllowedLinkingTypesSlice returns nil",
			plan:    ListPlan{AnalysisLookup: map[string]*RelationAnalysis{"folder.viewer": {}}},
			parent:  ListParentRelationData{Relation: "viewer"},
			wantNil: true,
		},
		{
			name: "missing key returns nil",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"other.viewer": {ObjectType: "other"},
			}},
			parent:  ListParentRelationData{Relation: "viewer", AllowedLinkingTypesSlice: []string{"folder"}},
			wantNil: true,
		},
		{
			name: "found returns analysis pointer",
			plan: ListPlan{AnalysisLookup: map[string]*RelationAnalysis{
				"folder.viewer": {ObjectType: "folder"},
			}},
			parent:  ListParentRelationData{Relation: "viewer", AllowedLinkingTypesSlice: []string{"folder"}},
			wantNil: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parentRelationAnalysis(tt.plan, tt.parent)
			if (got == nil) != tt.wantNil {
				t.Errorf("got nil=%v, want nil=%v", got == nil, tt.wantNil)
			}
		})
	}
}
