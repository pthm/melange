package doctor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseIndexColumns(t *testing.T) {
	tests := []struct {
		name     string
		indexdef string
		want     []string
	}{
		{
			name:     "simple btree",
			indexdef: "CREATE INDEX idx_test ON my_table USING btree (col1, col2, col3)",
			want:     []string{"col1", "col2", "col3"},
		},
		{
			name:     "unique index",
			indexdef: "CREATE UNIQUE INDEX idx_test ON my_table USING btree (object_type, object_id)",
			want:     []string{"object_type", "object_id"},
		},
		{
			name:     "single column",
			indexdef: "CREATE INDEX idx_test ON my_table USING btree (id)",
			want:     []string{"id"},
		},
		{
			name:     "expression index",
			indexdef: "CREATE INDEX idx_test ON my_table USING btree ((id::text))",
			want:     nil,
		},
		{
			name:     "mixed expression and column",
			indexdef: "CREATE INDEX idx_test ON my_table USING btree (col1, (col2::text))",
			want:     nil,
		},
		{
			name:     "hash index",
			indexdef: "CREATE INDEX idx_test ON my_table USING hash (col1)",
			want:     []string{"col1"},
		},
		{
			name:     "no parens",
			indexdef: "not a valid index definition",
			want:     nil,
		},
		{
			name:     "five column composite",
			indexdef: "CREATE INDEX idx_tuples ON melange_tuples USING btree (object_type, object_id, relation, subject_type, subject_id)",
			want:     []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIndexColumns(tt.indexdef)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasColumnPrefix(t *testing.T) {
	tests := []struct {
		name        string
		existing    []string
		recommended []string
		want        bool
	}{
		{
			name:        "exact match",
			existing:    []string{"a", "b", "c"},
			recommended: []string{"a", "b", "c"},
			want:        true,
		},
		{
			name:        "broader index covers recommendation",
			existing:    []string{"a", "b", "c", "d", "e"},
			recommended: []string{"a", "b", "c"},
			want:        true,
		},
		{
			name:        "narrower index does not cover",
			existing:    []string{"a", "b"},
			recommended: []string{"a", "b", "c"},
			want:        false,
		},
		{
			name:        "wrong column order",
			existing:    []string{"b", "a", "c"},
			recommended: []string{"a", "b", "c"},
			want:        false,
		},
		{
			name:        "case insensitive match",
			existing:    []string{"Object_Type", "Object_ID"},
			recommended: []string{"object_type", "object_id"},
			want:        true,
		},
		{
			name:        "empty existing",
			existing:    []string{},
			recommended: []string{"a"},
			want:        false,
		},
		{
			name:        "empty recommended",
			existing:    []string{"a", "b"},
			recommended: []string{},
			want:        true,
		},
		{
			name:        "check_lookup recommendation",
			existing:    []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
			recommended: []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
			want:        true,
		},
		{
			name:        "partial prefix mismatch",
			existing:    []string{"object_type", "relation", "object_id"},
			recommended: []string{"object_type", "object_id", "relation"},
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasColumnPrefix(tt.existing, tt.recommended)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRowCountSeverity(t *testing.T) {
	sev, note := rowCountSeverity(500)
	assert.Equal(t, StatusWarn, sev)
	assert.Equal(t, "recommended for future scaling", note)

	sev, note = rowCountSeverity(10000)
	assert.Equal(t, StatusFail, sev)
	assert.Contains(t, note, "critical")
	assert.Contains(t, note, "10000")
}

func TestParseIndexColumns_CloseBeforeOpen(t *testing.T) {
	assert.Nil(t, parseIndexColumns("CREATE INDEX ) ON t ("))
}

func TestParseIndexColumns_TrailingComma(t *testing.T) {
	got := parseIndexColumns("CREATE INDEX idx ON t USING btree (a, b, )")
	assert.Equal(t, []string{"a", "b"}, got)
}

func TestIsExpressionIndexed(t *testing.T) {
	defs := []string{
		"CREATE INDEX idx_user_text ON org_members ((user_id::text))",
		"CREATE INDEX idx_org_text ON org_members (((org_id)::text))",
		"CREATE INDEX idx_name ON org_members (name)",
	}

	assert.True(t, isExpressionIndexed("user_id", defs))
	assert.True(t, isExpressionIndexed("org_id", defs))
	assert.False(t, isExpressionIndexed("team_id", defs))
	assert.False(t, isExpressionIndexed("name", defs), "plain column index does not match expression pattern")
}

func TestIsExpressionIndexed_Empty(t *testing.T) {
	assert.False(t, isExpressionIndexed("col", nil))
	assert.False(t, isExpressionIndexed("col", []string{}))
}

func TestUniqueStrings(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, uniqueStrings([]string{"a", "b", "c"}))
	assert.Equal(t, []string{"a", "b", "c"}, uniqueStrings([]string{"a", "b", "a", "c", "b"}))
	assert.Nil(t, uniqueStrings(nil))
	assert.Nil(t, uniqueStrings([]string{}))
}
