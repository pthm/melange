package analysis

import "testing"

func TestListStrategy_String(t *testing.T) {
	tests := []struct {
		s    ListStrategy
		want string
	}{
		{ListStrategyDirect, "Direct"},
		{ListStrategyUserset, "Userset"},
		{ListStrategyRecursive, "Recursive"},
		{ListStrategyIntersection, "Intersection"},
		{ListStrategyDepthExceeded, "DepthExceeded"},
		{ListStrategySelfRefUserset, "SelfRefUserset"},
		{ListStrategyComposed, "Composed"},
		{ListStrategy(999), "Unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("ListStrategy(%d).String() = %q, want %q", int(tt.s), got, tt.want)
		}
	}
}

func TestDetermineListStrategy(t *testing.T) {
	tests := []struct {
		name string
		a    RelationAnalysis
		want ListStrategy
	}{
		{
			name: "depth exceeded takes highest priority",
			a:    RelationAnalysis{ExceedsDepthLimit: true, HasSelfReferentialUserset: true},
			want: ListStrategyDepthExceeded,
		},
		{
			name: "self-referential userset",
			a:    RelationAnalysis{HasSelfReferentialUserset: true},
			want: ListStrategySelfRefUserset,
		},
		{
			name: "composed via indirect anchor",
			a:    RelationAnalysis{IndirectAnchor: &IndirectAnchorInfo{}},
			want: ListStrategyComposed,
		},
		{
			name: "intersection",
			a:    RelationAnalysis{Features: RelationFeatures{HasIntersection: true}},
			want: ListStrategyIntersection,
		},
		{
			name: "recursive via HasRecursive",
			a:    RelationAnalysis{Features: RelationFeatures{HasRecursive: true}},
			want: ListStrategyRecursive,
		},
		{
			name: "recursive via closure parent relations",
			a:    RelationAnalysis{ClosureParentRelations: []ParentRelationInfo{{}}},
			want: ListStrategyRecursive,
		},
		{
			name: "userset via HasUserset",
			a:    RelationAnalysis{Features: RelationFeatures{HasUserset: true}},
			want: ListStrategyUserset,
		},
		{
			name: "userset via closure userset patterns",
			a:    RelationAnalysis{ClosureUsersetPatterns: []UsersetPattern{{}}},
			want: ListStrategyUserset,
		},
		{
			name: "direct is default",
			a:    RelationAnalysis{},
			want: ListStrategyDirect,
		},
		{
			name: "direct with only implied features",
			a:    RelationAnalysis{Features: RelationFeatures{HasImplied: true}},
			want: ListStrategyDirect,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetermineListStrategy(tt.a); got != tt.want {
				t.Errorf("DetermineListStrategy() = %v, want %v", got, tt.want)
			}
		})
	}
}
