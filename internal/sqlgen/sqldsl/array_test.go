package sqldsl

import (
	"strings"
	"testing"
)

func TestArrayLiteral_SQL(t *testing.T) {
	tests := []struct {
		name string
		arr  ArrayLiteral
		want string
	}{
		{
			name: "empty array",
			arr:  ArrayLiteral{Values: nil},
			want: "ARRAY[]::TEXT[]",
		},
		{
			name: "single value",
			arr:  ArrayLiteral{Values: []Expr{Lit("test")}},
			want: "ARRAY['test']",
		},
		{
			name: "multiple values",
			arr:  ArrayLiteral{Values: []Expr{Lit("a"), Lit("b"), Lit("c")}},
			want: "ARRAY['a', 'b', 'c']",
		},
		{
			name: "mixed expressions",
			arr:  ArrayLiteral{Values: []Expr{Lit("prefix"), ObjectID}},
			want: "ARRAY['prefix', p_object_id]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.arr.SQL(); got != tt.want {
				t.Errorf("ArrayLiteral.SQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArrayAppend_SQL(t *testing.T) {
	tests := []struct {
		name string
		aa   ArrayAppend
		want string
	}{
		{
			name: "append single value",
			aa: ArrayAppend{
				Array:  Visited,
				Values: []Expr{Lit("key")},
			},
			want: "p_visited || ARRAY['key']",
		},
		{
			name: "append concatenated key",
			aa: ArrayAppend{
				Array: Visited,
				Values: []Expr{
					Concat{Parts: []Expr{Lit("doc:"), ObjectID, Lit(":viewer")}},
				},
			},
			want: "p_visited || ARRAY['doc:' || p_object_id || ':viewer']",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.aa.SQL(); got != tt.want {
				t.Errorf("ArrayAppend.SQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVisitedKey(t *testing.T) {
	key := VisitedKey("document", "viewer", ObjectID)
	got := key.SQL()

	// Should produce: 'document:' || p_object_id || ':viewer'
	if got != "'document:' || p_object_id || ':viewer'" {
		t.Errorf("VisitedKey() = %q, want %q", got, "'document:' || p_object_id || ':viewer'")
	}
}

func TestVisitedWithKey(t *testing.T) {
	expr := VisitedWithKey("document", "viewer", ObjectID)
	got := expr.SQL()

	// Should produce: p_visited || ARRAY['document:' || p_object_id || ':viewer']
	expected := "p_visited || ARRAY['document:' || p_object_id || ':viewer']"
	if got != expected {
		t.Errorf("VisitedWithKey() = %q, want %q", got, expected)
	}
}

func TestArrayContains_SQL(t *testing.T) {
	ac := ArrayContains{
		Value: Raw("v_key"),
		Array: Visited,
	}
	got := ac.SQL()

	if got != "v_key = ANY(p_visited)" {
		t.Errorf("ArrayContains.SQL() = %q, want %q", got, "v_key = ANY(p_visited)")
	}
}

func TestArrayLength_SQL(t *testing.T) {
	tests := []struct {
		name string
		al   ArrayLength
		want string
	}{
		{
			name: "default dimension",
			al:   ArrayLength{Array: Visited, Dimension: 0},
			want: "array_length(p_visited, 1)",
		},
		{
			name: "explicit dimension 1",
			al:   ArrayLength{Array: Visited, Dimension: 1},
			want: "array_length(p_visited, 1)",
		},
		{
			name: "dimension 2",
			al:   ArrayLength{Array: Raw("my_array"), Dimension: 2},
			want: "array_length(my_array, 2)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.al.SQL(); got != tt.want {
				t.Errorf("ArrayLength.SQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVisitedKeyVar(t *testing.T) {
	// VisitedKeyVar returns the same as VisitedKey - just the RHS expression
	expr := VisitedKeyVar("folder", "owner", ObjectID)
	got := expr.SQL()

	if !strings.Contains(got, "'folder:'") || !strings.Contains(got, "':owner'") {
		t.Errorf("VisitedKeyVar() = %q, want to contain folder and owner", got)
	}
}
