package schema

import (
	"testing"
)

func TestRelationFeaturesString(t *testing.T) {
	tests := []struct {
		name string
		f    RelationFeatures
		want string
	}{
		{
			name: "none",
			f:    RelationFeatures{},
			want: "None",
		},
		{
			name: "direct only",
			f:    RelationFeatures{HasDirect: true},
			want: "Direct",
		},
		{
			name: "multiple features",
			f:    RelationFeatures{HasDirect: true, HasUserset: true, HasRecursive: true},
			want: "Direct+Userset+Recursive",
		},
		{
			name: "all features",
			f: RelationFeatures{
				HasDirect: true, HasImplied: true, HasWildcard: true,
				HasUserset: true, HasRecursive: true, HasExclusion: true, HasIntersection: true,
			},
			want: "Direct+Implied+Wildcard+Userset+Recursive+Exclusion+Intersection",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.String(); got != tt.want {
				t.Errorf("RelationFeatures.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRelationFeaturesCanGenerate(t *testing.T) {
	tests := []struct {
		name string
		f    RelationFeatures
		want bool
	}{
		{
			name: "no features",
			f:    RelationFeatures{},
			want: false,
		},
		{
			name: "direct only",
			f:    RelationFeatures{HasDirect: true},
			want: true,
		},
		{
			name: "complex combination",
			f:    RelationFeatures{HasUserset: true, HasRecursive: true, HasExclusion: true},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.CanGenerate(); got != tt.want {
				t.Errorf("CanGenerate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRelationFeaturesNeedsCycleDetection(t *testing.T) {
	tests := []struct {
		name string
		f    RelationFeatures
		want bool
	}{
		{
			name: "no recursive",
			f:    RelationFeatures{HasDirect: true, HasUserset: true},
			want: false,
		},
		{
			name: "with recursive",
			f:    RelationFeatures{HasDirect: true, HasRecursive: true},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.NeedsCycleDetection(); got != tt.want {
				t.Errorf("NeedsCycleDetection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFeatures_Direct(t *testing.T) {
	// define owner: [user]
	r := RelationDefinition{
		Name:            "owner",
		SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
	}
	got := detectFeatures(r, analysis)

	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if got.HasImplied || got.HasUserset || got.HasRecursive || got.HasExclusion || got.HasIntersection {
		t.Errorf("unexpected features: %v", got)
	}
}

func TestDetectFeatures_Implied(t *testing.T) {
	// define viewer: editor or owner
	r := RelationDefinition{
		Name:      "viewer",
		ImpliedBy: []string{"editor", "owner"},
	}
	analysis := RelationAnalysis{}
	got := detectFeatures(r, analysis)

	if !got.HasImplied {
		t.Error("expected HasImplied = true")
	}
}

func TestDetectFeatures_Exclusion(t *testing.T) {
	// define viewer: [user] but not blocked
	r := RelationDefinition{
		Name:             "viewer",
		SubjectTypeRefs:  []SubjectTypeRef{{Type: "user"}},
		ExcludedRelation: "blocked",
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
		ExcludedRelations:  []string{"blocked"},
	}
	got := detectFeatures(r, analysis)

	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !got.HasExclusion {
		t.Error("expected HasExclusion = true")
	}
}

func TestDetectFeatures_Wildcard(t *testing.T) {
	// define public: [user:*]
	r := RelationDefinition{
		Name:            "public",
		SubjectTypeRefs: []SubjectTypeRef{{Type: "user", Wildcard: true}},
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
	}
	got := detectFeatures(r, analysis)

	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !got.HasWildcard {
		t.Error("expected HasWildcard = true")
	}
}

func TestDetectFeatures_Userset(t *testing.T) {
	// define viewer: [user, group#member]
	r := RelationDefinition{
		Name: "viewer",
		SubjectTypeRefs: []SubjectTypeRef{
			{Type: "user"},
			{Type: "group", Relation: "member"},
		},
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
		UsersetPatterns: []UsersetPattern{
			{SubjectType: "group", SubjectRelation: "member"},
		},
	}
	got := detectFeatures(r, analysis)

	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !got.HasUserset {
		t.Error("expected HasUserset = true")
	}
}

func TestDetectFeatures_Recursive(t *testing.T) {
	// define viewer: [user] or viewer from parent
	r := RelationDefinition{
		Name:            "viewer",
		SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
		ParentRelation:  "viewer",
		ParentType:      "parent",
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
		ParentRelations: []ParentRelationInfo{
			{Relation: "viewer", LinkingRelation: "parent", ParentType: "parent"},
		},
	}
	got := detectFeatures(r, analysis)

	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !got.HasRecursive {
		t.Error("expected HasRecursive = true")
	}
	if !got.NeedsCycleDetection() {
		t.Error("expected NeedsCycleDetection() = true")
	}
}

func TestDetectFeatures_Intersection(t *testing.T) {
	// define viewer: writer and editor
	r := RelationDefinition{
		Name: "viewer",
		IntersectionGroups: []IntersectionGroup{
			{Relations: []string{"writer", "editor"}},
		},
	}
	analysis := RelationAnalysis{
		IntersectionGroups: []IntersectionGroupInfo{
			{Parts: []IntersectionPart{
				{Relation: "writer"},
				{Relation: "editor"},
			}},
		},
	}
	got := detectFeatures(r, analysis)

	if !got.HasIntersection {
		t.Error("expected HasIntersection = true")
	}
}

func TestDetectFeatures_ComplexCombination(t *testing.T) {
	// define viewer: [user, group#member] or viewer from parent but not blocked
	// This tests that combinations are properly detected (not forced to Generic)
	r := RelationDefinition{
		Name: "viewer",
		SubjectTypeRefs: []SubjectTypeRef{
			{Type: "user"},
			{Type: "group", Relation: "member"},
		},
		ParentRelation:   "viewer",
		ParentType:       "parent",
		ExcludedRelation: "blocked",
	}
	analysis := RelationAnalysis{
		DirectSubjectTypes: []string{"user"},
		UsersetPatterns: []UsersetPattern{
			{SubjectType: "group", SubjectRelation: "member"},
		},
		ParentRelations: []ParentRelationInfo{
			{Relation: "viewer", LinkingRelation: "parent", ParentType: "parent"},
		},
		ExcludedRelations: []string{"blocked"},
	}
	got := detectFeatures(r, analysis)

	// All features should be detected
	if !got.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !got.HasUserset {
		t.Error("expected HasUserset = true")
	}
	if !got.HasRecursive {
		t.Error("expected HasRecursive = true")
	}
	if !got.HasExclusion {
		t.Error("expected HasExclusion = true")
	}

	// Should still be generatable (we compose SQL)
	if !got.CanGenerate() {
		t.Error("expected CanGenerate() = true for complex combination")
	}

	// Features string should show all
	str := got.String()
	if str != "Direct+Userset+Recursive+Exclusion" {
		t.Errorf("unexpected String(): %s", str)
	}
}

func TestHasWildcardRefs(t *testing.T) {
	tests := []struct {
		name string
		r    RelationDefinition
		want bool
	}{
		{
			name: "no wildcard",
			r: RelationDefinition{
				SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
			},
			want: false,
		},
		{
			name: "with wildcard ref",
			r: RelationDefinition{
				SubjectTypeRefs: []SubjectTypeRef{{Type: "user", Wildcard: true}},
			},
			want: true,
		},
		{
			name: "legacy wildcard suffix",
			r: RelationDefinition{
				SubjectTypes: []string{"user:*"},
			},
			want: true,
		},
		{
			name: "legacy no wildcard",
			r: RelationDefinition{
				SubjectTypes: []string{"user"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasWildcardRefs(tt.r); got != tt.want {
				t.Errorf("hasWildcardRefs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectDirectSubjectTypes(t *testing.T) {
	tests := []struct {
		name string
		r    RelationDefinition
		want []string
	}{
		{
			name: "direct user ref",
			r: RelationDefinition{
				SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
			},
			want: []string{"user"},
		},
		{
			name: "userset ref only",
			r: RelationDefinition{
				SubjectTypeRefs: []SubjectTypeRef{{Type: "group", Relation: "member"}},
			},
			want: nil,
		},
		{
			name: "mixed refs",
			r: RelationDefinition{
				SubjectTypeRefs: []SubjectTypeRef{
					{Type: "user"},
					{Type: "group", Relation: "member"},
				},
			},
			want: []string{"user"},
		},
		{
			name: "legacy subject types",
			r: RelationDefinition{
				SubjectTypes: []string{"user"},
			},
			want: []string{"user"},
		},
		{
			name: "legacy with wildcard stripped",
			r: RelationDefinition{
				SubjectTypes: []string{"user:*"},
			},
			want: []string{"user"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectDirectSubjectTypes(tt.r)
			if len(got) != len(tt.want) {
				t.Errorf("collectDirectSubjectTypes() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("collectDirectSubjectTypes()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCollectUsersetPatterns(t *testing.T) {
	r := RelationDefinition{
		SubjectTypeRefs: []SubjectTypeRef{
			{Type: "user"},
			{Type: "group", Relation: "member"},
			{Type: "team", Relation: "participant"},
		},
	}

	patterns := collectUsersetPatterns(r)

	if len(patterns) != 2 {
		t.Fatalf("collectUsersetPatterns() returned %d patterns, want 2", len(patterns))
	}

	// Check first pattern
	if patterns[0].SubjectType != "group" || patterns[0].SubjectRelation != "member" {
		t.Errorf("patterns[0] = %+v, want {group, member}", patterns[0])
	}

	// Check second pattern
	if patterns[1].SubjectType != "team" || patterns[1].SubjectRelation != "participant" {
		t.Errorf("patterns[1] = %+v, want {team, participant}", patterns[1])
	}
}

func TestCollectParentRelations(t *testing.T) {
	tests := []struct {
		name string
		r    RelationDefinition
		want int
	}{
		{
			name: "single legacy parent",
			r: RelationDefinition{
				ParentRelation: "viewer",
				ParentType:     "parent",
			},
			want: 1,
		},
		{
			name: "multiple parents",
			r: RelationDefinition{
				ParentRelations: []ParentRelationCheck{
					{Relation: "viewer", ParentType: "org"},
					{Relation: "viewer", ParentType: "team"},
				},
			},
			want: 2,
		},
		{
			name: "no parents",
			r:    RelationDefinition{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parents := collectParentRelations(tt.r)
			if len(parents) != tt.want {
				t.Errorf("collectParentRelations() returned %d parents, want %d", len(parents), tt.want)
			}
		})
	}
}

func TestCollectExcludedRelations(t *testing.T) {
	tests := []struct {
		name string
		r    RelationDefinition
		want []string
	}{
		{
			name: "single exclusion (legacy)",
			r: RelationDefinition{
				ExcludedRelation: "blocked",
			},
			want: []string{"blocked"},
		},
		{
			name: "multiple exclusions",
			r: RelationDefinition{
				ExcludedRelations: []string{"blocked", "suspended"},
			},
			want: []string{"blocked", "suspended"},
		},
		{
			name: "no exclusions",
			r:    RelationDefinition{},
			want: nil,
		},
		{
			name: "both legacy and new (no duplicate)",
			r: RelationDefinition{
				ExcludedRelation:  "blocked",
				ExcludedRelations: []string{"blocked", "suspended"},
			},
			want: []string{"blocked", "suspended"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			excluded := collectExcludedRelations(tt.r)
			if len(excluded) != len(tt.want) {
				t.Errorf("collectExcludedRelations() = %v, want %v", excluded, tt.want)
			}
		})
	}
}

func TestAnalyzeRelations(t *testing.T) {
	types := []TypeDefinition{
		{
			Name: "document",
			Relations: []RelationDefinition{
				{
					Name:            "owner",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
				{
					Name:            "editor",
					ImpliedBy:       []string{"owner"},
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
				{
					Name:              "viewer",
					ImpliedBy:         []string{"editor"},
					SubjectTypeRefs:   []SubjectTypeRef{{Type: "user"}},
					ExcludedRelations: []string{"blocked"},
				},
				{
					Name:            "blocked",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
			},
		},
	}

	// Compute closure (using existing function)
	closure := ComputeRelationClosure(types)

	// Analyze relations
	analyses := AnalyzeRelations(types, closure)

	if len(analyses) != 4 {
		t.Fatalf("AnalyzeRelations() returned %d analyses, want 4", len(analyses))
	}

	// Check each relation's features
	for _, a := range analyses {
		switch a.Relation {
		case "owner":
			if !a.Features.HasDirect {
				t.Error("document.owner: expected HasDirect = true")
			}
			if a.Features.HasImplied || a.Features.HasExclusion {
				t.Errorf("document.owner: unexpected features: %v", a.Features)
			}
		case "editor":
			if !a.Features.HasDirect {
				t.Error("document.editor: expected HasDirect = true")
			}
			if !a.Features.HasImplied {
				t.Error("document.editor: expected HasImplied = true")
			}
		case "viewer":
			if !a.Features.HasDirect {
				t.Error("document.viewer: expected HasDirect = true")
			}
			if !a.Features.HasImplied {
				t.Error("document.viewer: expected HasImplied = true")
			}
			if !a.Features.HasExclusion {
				t.Error("document.viewer: expected HasExclusion = true")
			}
			if len(a.ExcludedRelations) != 1 || a.ExcludedRelations[0] != "blocked" {
				t.Errorf("document.viewer excluded = %v, want [blocked]", a.ExcludedRelations)
			}
		case "blocked":
			if !a.Features.HasDirect {
				t.Error("document.blocked: expected HasDirect = true")
			}
		}
	}
}

func TestAnalyzeRelations_WithClosure(t *testing.T) {
	// Test that satisfying relations are populated from closure
	types := []TypeDefinition{
		{
			Name: "document",
			Relations: []RelationDefinition{
				{
					Name:            "owner",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
				{
					Name:            "editor",
					ImpliedBy:       []string{"owner"},
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
				{
					Name:            "viewer",
					ImpliedBy:       []string{"editor"},
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
			},
		},
	}

	closure := ComputeRelationClosure(types)
	analyses := AnalyzeRelations(types, closure)

	// Find viewer analysis
	var viewerAnalysis *RelationAnalysis
	for i := range analyses {
		if analyses[i].Relation == "viewer" {
			viewerAnalysis = &analyses[i]
			break
		}
	}

	if viewerAnalysis == nil {
		t.Fatal("viewer analysis not found")
	}

	// viewer should be satisfied by viewer, editor, owner
	if len(viewerAnalysis.SatisfyingRelations) != 3 {
		t.Errorf("viewer has %d satisfying relations, want 3", len(viewerAnalysis.SatisfyingRelations))
	}
}

func TestAnalyzeRelations_ComplexComposite(t *testing.T) {
	// Test a relation with multiple features that should all be composable
	types := []TypeDefinition{
		{
			Name: "folder",
			Relations: []RelationDefinition{
				{
					Name:            "parent",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "folder"}},
				},
				{
					Name: "viewer",
					SubjectTypeRefs: []SubjectTypeRef{
						{Type: "user"},
						{Type: "group", Relation: "member"},
					},
					ParentRelation:   "viewer",
					ParentType:       "parent",
					ExcludedRelation: "blocked",
				},
				{
					Name:            "blocked",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
			},
		},
		{
			Name: "group",
			Relations: []RelationDefinition{
				{
					Name:            "member",
					SubjectTypeRefs: []SubjectTypeRef{{Type: "user"}},
				},
			},
		},
	}

	closure := ComputeRelationClosure(types)
	analyses := AnalyzeRelations(types, closure)

	// Find folder.viewer analysis
	var viewerAnalysis *RelationAnalysis
	for i := range analyses {
		if analyses[i].ObjectType == "folder" && analyses[i].Relation == "viewer" {
			viewerAnalysis = &analyses[i]
			break
		}
	}

	if viewerAnalysis == nil {
		t.Fatal("folder.viewer analysis not found")
	}

	// Check all features are detected
	f := viewerAnalysis.Features
	if !f.HasDirect {
		t.Error("expected HasDirect = true")
	}
	if !f.HasUserset {
		t.Error("expected HasUserset = true")
	}
	if !f.HasRecursive {
		t.Error("expected HasRecursive = true")
	}
	if !f.HasExclusion {
		t.Error("expected HasExclusion = true")
	}

	// Should still be generatable
	if !f.CanGenerate() {
		t.Error("expected CanGenerate() = true")
	}

	// Check collected data
	if len(viewerAnalysis.DirectSubjectTypes) != 1 || viewerAnalysis.DirectSubjectTypes[0] != "user" {
		t.Errorf("unexpected DirectSubjectTypes: %v", viewerAnalysis.DirectSubjectTypes)
	}
	if len(viewerAnalysis.UsersetPatterns) != 1 {
		t.Errorf("expected 1 userset pattern, got %d", len(viewerAnalysis.UsersetPatterns))
	}
	if len(viewerAnalysis.ParentRelations) != 1 {
		t.Errorf("expected 1 parent relation, got %d", len(viewerAnalysis.ParentRelations))
	}
	if len(viewerAnalysis.ExcludedRelations) != 1 {
		t.Errorf("expected 1 excluded relation, got %d", len(viewerAnalysis.ExcludedRelations))
	}
}
