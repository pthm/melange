package studio

import (
	"slices"
	"testing"
)

func TestPickLabelColumn(t *testing.T) {
	for _, tc := range []struct {
		cols []string
		want string
	}{
		{[]string{"id", "title", "name"}, "name"},              // preset order wins over table order
		{[]string{"id", "Name"}, "Name"},                       // case-insensitive, keeps the real name
		{[]string{"id", "DISPLAY_NAME"}, "DISPLAY_NAME"},       // case-insensitive preset
		{[]string{"id", "status", "check_name"}, "check_name"}, // ends in name
		{[]string{"id", "displayName", "first_name"}, "displayName"},
		{[]string{"id", "description", "area"}, "description"}, // last resort
		{[]string{"id", "check_name", "description"}, "check_name"},
		{[]string{"id", "status"}, ""},
	} {
		if got := pickLabelColumn(tc.cols); got != tc.want {
			t.Errorf("pickLabelColumn(%v) = %q, want %q", tc.cols, got, tc.want)
		}
	}
}

func TestParseTypes(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want []string
	}{
		{`'user'::text`, []string{"user"}},
		{`CASE WHEN ((trails.issue_type)::text = 'ISSUE_TYPE_QA_TRAIL'::text) THEN 'qa_trail'::text ELSE 'aa_trail'::text END`, []string{"qa_trail", "aa_trail"}},
		{`(t.kind)::text`, nil},
	} {
		if got := parseTypes(tc.expr); !slices.Equal(got, tc.want) {
			t.Errorf("parseTypes(%q) = %v, want %v", tc.expr, got, tc.want)
		}
	}
}
