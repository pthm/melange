package doctor

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pthm/melange/lib/sqlgen"
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

func TestPartialSuffix(t *testing.T) {
	full := sqlgen.IndexRecommendation{Columns: []string{"a", "b"}}
	if got := partialSuffix(full); got != "" {
		t.Errorf("full recommendation should yield empty suffix; got %q", got)
	}

	partial := sqlgen.IndexRecommendation{
		Columns:     []string{"a"},
		WhereClause: "subject_id = '*'",
	}
	want := " WHERE subject_id = '*'"
	if got := partialSuffix(partial); got != want {
		t.Errorf("partial suffix = %q, want %q", got, want)
	}
}

func TestParseIndexPredicate(t *testing.T) {
	tests := []struct {
		name     string
		indexdef string
		want     string
	}{
		{
			name:     "full index has no predicate",
			indexdef: "CREATE INDEX idx_foo ON t USING btree (a, b)",
			want:     "",
		},
		{
			name:     "partial index with simple predicate",
			indexdef: "CREATE INDEX idx_foo ON t USING btree (a, b) WHERE subject_id = '*'",
			want:     "subject_id = '*'",
		},
		{
			name:     "partial index with parens around predicate (pg canonical form)",
			indexdef: "CREATE INDEX idx_foo ON t USING btree (a, b) WHERE (subject_id = '*'::text)",
			want:     "(subject_id = '*'::text)",
		},
		{
			name:     "lowercase where keyword",
			indexdef: "CREATE INDEX idx_foo ON t USING btree (a) where a > 0",
			want:     "a > 0",
		},
		{
			name:     "no parens in definition",
			indexdef: "invalid",
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseIndexPredicate(tt.indexdef))
		})
	}
}

func TestPredicatesEquivalent(t *testing.T) {
	// Recommendations emit "subject_id = '*'"; PG renders the same predicate
	// as "(subject_id = '*'::text)" in pg_indexes. Both should match.
	assert.True(t, predicatesEquivalent("(subject_id = '*'::text)", "subject_id = '*'"))
	assert.True(t, predicatesEquivalent("subject_id = '*'", "subject_id = '*'"))
	assert.True(t, predicatesEquivalent("SUBJECT_ID = '*'", "subject_id = '*'"))
	assert.False(t, predicatesEquivalent("subject_id = 'x'", "subject_id = '*'"))
	assert.False(t, predicatesEquivalent("", "subject_id = '*'"))
}

func TestParseExistingIndexes(t *testing.T) {
	defs := []string{
		"CREATE INDEX idx_full ON t USING btree (object_type, object_id, relation, subject_type, subject_id)",
		"CREATE INDEX idx_partial ON t USING btree (object_type, object_id, relation) WHERE (subject_id = '*'::text)",
		"CREATE INDEX idx_expr ON t USING btree ((object_id::text))", // dropped: expression-only
	}
	got := parseExistingIndexes(defs)
	if assert.Len(t, got, 2) {
		assert.Equal(t, []string{"object_type", "object_id", "relation", "subject_type", "subject_id"}, got[0].columns)
		assert.Equal(t, "", got[0].predicate)

		assert.Equal(t, []string{"object_type", "object_id", "relation"}, got[1].columns)
		assert.Equal(t, "(subject_id = '*'::text)", got[1].predicate)
	}
}

func TestIndexCoversRecommendation(t *testing.T) {
	full := sqlgen.IndexRecommendation{
		Columns: []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
	}
	partial := sqlgen.IndexRecommendation{
		Columns:     []string{"object_type", "object_id", "relation"},
		WhereClause: "subject_id = '*'",
	}

	t.Run("exact full match", func(t *testing.T) {
		existing := []existingIndex{{
			columns: []string{"object_type", "object_id", "relation", "subject_type", "subject_id"},
		}}
		assert.True(t, indexCoversRecommendation(existing, full))
	})

	t.Run("broader index covers full recommendation", func(t *testing.T) {
		existing := []existingIndex{{
			columns: []string{"object_type", "object_id", "relation", "subject_type", "subject_id", "extra"},
		}}
		assert.True(t, indexCoversRecommendation(existing, full))
	})

	t.Run("partial recommendation matched by partial index with same predicate", func(t *testing.T) {
		existing := []existingIndex{{
			columns:   []string{"object_type", "object_id", "relation"},
			predicate: "(subject_id = '*'::text)",
		}}
		assert.True(t, indexCoversRecommendation(existing, partial))
	})

	t.Run("partial recommendation NOT matched by full index with same columns", func(t *testing.T) {
		// A full index over (object_type, object_id, relation) doesn't equal a
		// partial index keyed on the same columns: the partial index is
		// physically smaller and PG can prefer it for wildcard lookups.
		existing := []existingIndex{{
			columns: []string{"object_type", "object_id", "relation"},
		}}
		assert.False(t, indexCoversRecommendation(existing, partial))
	})

	t.Run("partial recommendation NOT matched by partial index with different predicate", func(t *testing.T) {
		existing := []existingIndex{{
			columns:   []string{"object_type", "object_id", "relation"},
			predicate: "subject_id = 'admin'",
		}}
		assert.False(t, indexCoversRecommendation(existing, partial))
	})

	t.Run("wrong column order doesn't cover", func(t *testing.T) {
		existing := []existingIndex{{
			columns: []string{"object_id", "object_type", "relation", "subject_type", "subject_id"},
		}}
		assert.False(t, indexCoversRecommendation(existing, full))
	})

	t.Run("no existing indexes", func(t *testing.T) {
		assert.False(t, indexCoversRecommendation(nil, full))
		assert.False(t, indexCoversRecommendation(nil, partial))
	})
}
