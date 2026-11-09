package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
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

func TestCheckExpandFanoutAdvisory(t *testing.T) {
	feat := func(hasWildcard, hasRecursive bool) sqlgen.RelationFeatures {
		return sqlgen.RelationFeatures{HasWildcard: hasWildcard, HasRecursive: hasRecursive}
	}
	analysis := func(objectType, relation string, f sqlgen.RelationFeatures) sqlgen.RelationAnalysis {
		return sqlgen.RelationAnalysis{
			ObjectType: objectType,
			Relation:   relation,
			Features:   f,
		}
	}

	t.Run("no wildcards or recursive → no advisory", func(t *testing.T) {
		d := &Doctor{analyses: []sqlgen.RelationAnalysis{
			analysis("document", "viewer", feat(false, false)),
			analysis("document", "editor", feat(false, false)),
		}}
		report := &Report{}
		if err := d.checkExpandFanoutAdvisory(context.Background(), report); err != nil {
			t.Fatalf("checkExpandFanoutAdvisory: %v", err)
		}
		if len(report.Checks) != 0 {
			t.Errorf("expected no advisory, got %d checks: %+v", len(report.Checks), report.Checks)
		}
	})

	t.Run("wildcard relation surfaces advisory with guardrail suggestion", func(t *testing.T) {
		d := &Doctor{analyses: []sqlgen.RelationAnalysis{
			analysis("document", "public", feat(true, false)),
		}}
		report := &Report{}
		if err := d.checkExpandFanoutAdvisory(context.Background(), report); err != nil {
			t.Fatalf("checkExpandFanoutAdvisory: %v", err)
		}
		if len(report.Checks) != 1 {
			t.Fatalf("expected 1 advisory, got %d", len(report.Checks))
		}
		check := report.Checks[0]
		assert.Equal(t, "expand_fanout_advisory", check.Name)
		assert.Equal(t, StatusPass, check.Status)
		assert.Contains(t, check.Details, "document#public")
		assert.Contains(t, check.Details, "WithExpandMaxLeaf")
		assert.NotContains(t, check.Details, "max_expand_leaf")
		assert.Contains(t, check.Details, "Wildcard grants")
	})

	t.Run("recursive TTU surfaces advisory", func(t *testing.T) {
		d := &Doctor{analyses: []sqlgen.RelationAnalysis{
			analysis("document", "viewer", feat(false, true)),
		}}
		report := &Report{}
		if err := d.checkExpandFanoutAdvisory(context.Background(), report); err != nil {
			t.Fatalf("checkExpandFanoutAdvisory: %v", err)
		}
		if len(report.Checks) != 1 {
			t.Fatalf("expected 1 advisory, got %d", len(report.Checks))
		}
		check := report.Checks[0]
		assert.Contains(t, check.Details, "Recursive TTU")
		assert.Contains(t, check.Details, "document#viewer")
	})

	t.Run("both signals in one advisory", func(t *testing.T) {
		d := &Doctor{analyses: []sqlgen.RelationAnalysis{
			analysis("document", "public", feat(true, false)),
			analysis("folder", "viewer", feat(false, true)),
		}}
		report := &Report{}
		if err := d.checkExpandFanoutAdvisory(context.Background(), report); err != nil {
			t.Fatalf("checkExpandFanoutAdvisory: %v", err)
		}
		if len(report.Checks) != 1 {
			t.Fatalf("expected 1 advisory, got %d", len(report.Checks))
		}
		check := report.Checks[0]
		assert.Contains(t, check.Message, "2 relation(s)")
		assert.Contains(t, check.Details, "document#public")
		assert.Contains(t, check.Details, "folder#viewer")
		// Both sections present.
		assert.True(t, strings.Contains(check.Details, "Wildcard grants") && strings.Contains(check.Details, "Recursive TTU"),
			"expected both wildcard and recursive sections in details:\n%s", check.Details)
	})
}

func TestHasLeadingColumn(t *testing.T) {
	defs := []string{
		"CREATE INDEX r_v ON public.r USING btree (v)",
		"CREATE INDEX r_ab ON public.r USING btree (a, b)",
		`CREATE INDEX r_q ON public.r USING btree ("Q")`,
		"CREATE INDEX r_p ON public.r USING btree (p) WHERE (p IS NOT NULL)",
		"CREATE INDEX r_e ON public.r USING btree (((e)::text))",
	}
	for col, want := range map[string]bool{
		"v": true, "V": true, "a": true, "q": true,
		"b": false, // not the leading key
		"p": false, // partial
		"e": false, // expression only
		"z": false,
	} {
		if got := hasLeadingColumn(col, defs); got != want {
			t.Errorf("hasLeadingColumn(%q) = %v, want %v", col, got, want)
		}
	}
}

// exposureDoctor is a Doctor for restrictedPerfSchema, migrated with exposure.
func exposureDoctor(t *testing.T, exposure sqlgen.Exposure) *Doctor {
	t.Helper()
	types, err := parser.ParseSchemaString(restrictedPerfSchema)
	if err != nil {
		t.Fatalf("parsing schema: %v", err)
	}
	return &Doctor{parsedTypes: types, lastMigration: &migrator.MigrationRecord{Exposure: exposure}}
}

const restrictedPerfSchema = `model
  schema 1.1

type user

type folder
  relations
    define viewer: [user]

type document
  relations
    define parent: [folder]
    define public: [user:*]
    define editor: [user]
    define viewer: [user] or editor or viewer from parent
`

func TestInstalledIndexRecommendations(t *testing.T) {
	off := false
	recommended := func(d *Doctor) map[string][]string {
		out := make(map[string][]string)
		for _, rec := range d.installedIndexRecommendations(d.getAnalyses()) {
			out[strings.Join(rec.Columns, ",")+partialSuffix(rec)] = rec.BenefitsFunctions
		}
		return out
	}
	subjectKeyed := "subject_type,subject_id,relation,object_type,object_id"

	all := recommended(exposureDoctor(t, sqlgen.Exposure{}))
	assert.Contains(t, all[subjectKeyed], "list_document_viewer_obj", "the default exposure installs list_*_obj")
	assert.Len(t, all, 3, "object-keyed, subject-keyed and wildcard partial")

	// No list_*_obj is installed without these families, so the
	// subject-keyed index benefits nothing; public is outside the closure,
	// so the wildcard partial index benefits nothing either.
	restricted := recommended(exposureDoctor(t, sqlgen.Exposure{
		Relations: sqlgen.RelationPatterns{Include: []string{"document#viewer"}},
		Functions: sqlgen.FunctionFamilies{ListObjects: &off, Accessible: &off, ParentKey: &off},
	}))
	assert.NotContains(t, restricted, subjectKeyed)
	assert.Len(t, restricted, 1, "only the object-keyed index has installed beneficiaries")
	for _, benefits := range restricted {
		assert.Contains(t, benefits, "check_document_viewer")
		assert.Contains(t, benefits, "check_folder_viewer", "in document#viewer's closure")
		assert.NotContains(t, benefits, "check_document_public", "outside the closure")
	}
}

func TestCheckExpandFanoutAdvisory_InstalledExposure(t *testing.T) {
	off := false
	advisory := func(d *Doctor) *CheckResult {
		report := &Report{}
		if err := d.checkExpandFanoutAdvisory(context.Background(), report); err != nil {
			t.Fatalf("checkExpandFanoutAdvisory: %v", err)
		}
		if len(report.Checks) == 0 {
			return nil
		}
		return &report.Checks[0]
	}

	if c := advisory(exposureDoctor(t, sqlgen.Exposure{})); assert.NotNil(t, c) {
		assert.Contains(t, c.Details, "document#public")
	}
	c := advisory(exposureDoctor(t, sqlgen.Exposure{
		Relations: sqlgen.RelationPatterns{Include: []string{"document#editor"}},
	}))
	if c != nil {
		assert.NotContains(t, c.Details, "document#public", "the expand dispatcher raises for relations outside the closure")
	}
	assert.Nil(t, advisory(exposureDoctor(t, sqlgen.Exposure{
		Functions: sqlgen.FunctionFamilies{Expand: &off},
	})), "no expand functions are installed")
}
