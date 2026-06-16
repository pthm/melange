package test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/lib/doctor"
	"github.com/pthm/melange/test/testutil"
)

// TestDoctor_SourceTableIndexAdvisory verifies that when melange_tuples is a
// view (the standard production setup), doctor emits index recommendations
// derived from the user's schema as advisory output the user can apply to
// their source tables. The recommendations come from sqlgen.RecommendIndexes
// rather than a hardcoded list, so they match the actual access patterns of
// generated SQL.
func TestDoctor_SourceTableIndexAdvisory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")
	check := findCheck(perfChecks, "source_table_indexes_advisory")
	require.NotNil(t, check, "expected source_table_indexes_advisory check on a view-backed melange_tuples")
	assert.Equal(t, doctor.StatusPass, check.Status, "advisory should not escalate to warn/fail on its own")

	// Details should include at least the two universal recommendations.
	assert.Containsf(t, check.Details, "CREATE INDEX IF NOT EXISTS",
		"advisory Details should include DDL; got:\n%s", check.Details)
	assert.Containsf(t, check.Details, "(object_type, object_id, relation, subject_type, subject_id)",
		"advisory Details should include the object-keyed recommendation; got:\n%s", check.Details)
	assert.Containsf(t, check.Details, "(subject_type, subject_id, relation, object_type, object_id)",
		"advisory Details should include the subject-keyed recommendation; got:\n%s", check.Details)

	// Message should be terse enough for non-verbose output.
	assert.Containsf(t, check.Message, "recommendation",
		"Message should mention recommendations; got: %s", check.Message)
}

// TestDoctor_TableIndexes_MissingRecommendations creates a real table named
// melange_tuples (without indexes), runs doctor, and asserts the recommended
// indexes show up as fail/warn check results with executable DDL fix hints
// derived from the schema. Detects missing indexes on the real (non-view)
// case and replaces the previous hardcoded recommendation set.
func TestDoctor_TableIndexes_MissingRecommendations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Swap the view for an unindexed table for this test, then restore.
	t.Cleanup(func() {
		restoreView(t, db)
	})
	_, err := db.ExecContext(ctx, `DROP VIEW IF EXISTS melange_tuples`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type TEXT NOT NULL,
			subject_id   TEXT NOT NULL,
			relation     TEXT NOT NULL,
			object_type  TEXT NOT NULL,
			object_id    TEXT NOT NULL
		)
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS melange_tuples`)
	})

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)

	perfChecks := filterCategory(report, "Performance")

	// Should have at least one table_indexes check; each missing index gets
	// its own CheckResult so users can see the count of indexes they're missing.
	var missing []doctor.CheckResult
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && c.Status != doctor.StatusPass {
			missing = append(missing, c)
		}
	}
	assert.NotEmptyf(t, missing,
		"expected at least one missing-index check on an unindexed melange_tuples table; perf checks were:\n%v",
		perfChecks)

	// Every missing-index check must carry a runnable CREATE INDEX statement
	// the user can copy-paste.
	for _, c := range missing {
		assert.Containsf(t, c.FixHint, "CREATE INDEX",
			"missing-index check %q should have a CREATE INDEX FixHint; got: %s", c.Message, c.FixHint)
		assert.Containsf(t, c.FixHint, "ON melange_tuples",
			"FixHint should target melange_tuples; got: %s", c.FixHint)
	}
}

// TestDoctor_TableIndexes_PartialIndexRecognized verifies the partial
// wildcard recommendation is treated correctly: a full index on
// (object_type, object_id, relation) does NOT cover the wildcard partial
// recommendation, while an actual partial index with the matching WHERE
// clause does.
func TestDoctor_TableIndexes_PartialIndexRecognized(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	db := testutil.DB(t)
	ctx := context.Background()

	// Use a schema with HasWildcard so the partial recommendation is emitted.
	// The test schema.fga used by other doctor tests has public_repo with
	// wildcard subject; relying on it keeps the test deterministic without
	// committing a separate fixture.
	t.Cleanup(func() { restoreView(t, db) })
	_, err := db.ExecContext(ctx, `DROP VIEW IF EXISTS melange_tuples`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE melange_tuples (
			subject_type TEXT NOT NULL,
			subject_id   TEXT NOT NULL,
			relation     TEXT NOT NULL,
			object_type  TEXT NOT NULL,
			object_id    TEXT NOT NULL
		)
	`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DROP TABLE IF EXISTS melange_tuples`)
	})

	// Add the two full indexes but skip the wildcard partial.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_full_object ON melange_tuples (object_type, object_id, relation, subject_type, subject_id);
	`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_full_subject ON melange_tuples (subject_type, subject_id, relation, object_type, object_id);
	`)
	require.NoError(t, err)

	d := doctor.New(db, "testutil/testdata/schema.fga")
	report, err := d.Run(ctx)
	require.NoError(t, err)
	perfChecks := filterCategory(report, "Performance")

	// The test schema produces a wildcard recommendation; we expect to still
	// see a table_indexes warning specifically for the partial index.
	wildcardMissing := false
	for _, c := range perfChecks {
		if c.Name != "table_indexes" || c.Status == doctor.StatusPass {
			continue
		}
		if strings.Contains(c.FixHint, "WHERE subject_id = '*'") {
			wildcardMissing = true
			break
		}
	}
	assert.True(t, wildcardMissing,
		"expected the wildcard partial recommendation to still be flagged missing when only full indexes exist; perf checks:\n%v",
		perfChecks)

	// Now add the partial index and rerun — the wildcard advisory should clear.
	_, err = db.ExecContext(ctx, `
		CREATE INDEX idx_wildcard_partial ON melange_tuples (object_type, object_id, relation) WHERE subject_id = '*'
	`)
	require.NoError(t, err)

	report, err = d.Run(ctx)
	require.NoError(t, err)
	perfChecks = filterCategory(report, "Performance")
	for _, c := range perfChecks {
		if c.Name == "table_indexes" && strings.Contains(c.FixHint, "WHERE subject_id = '*'") {
			t.Errorf("partial wildcard recommendation should be covered now; got:\n  Message: %s\n  FixHint: %s",
				c.Message, c.FixHint)
		}
	}
}
