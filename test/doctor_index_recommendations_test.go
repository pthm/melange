package test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/internal/doctor"
	"github.com/pthm/melange/test/testutil"
)

// TestDoctor_SourceTableIndexes verifies that when melange_tuples is a view,
// doctor names the composite index each single-table branch's object
// lookups need, on the real source table and columns, and only when it is
// missing and would help.
func TestDoctor_SourceTableIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	db := testutil.EmptyDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `
		-- many members per org, relation from a column: needs (org, role, user)
		CREATE TABLE members (org_id bigint NOT NULL, user_id varchar NOT NULL, role varchar NOT NULL);
		CREATE INDEX members_org ON members (org_id);
		-- object id unique: one row per repo, no composite needed
		CREATE TABLE repos (id bigint PRIMARY KEY, org_id varchar NOT NULL);
		-- already has an index leading with object then subject
		CREATE TABLE team_members (team_id varchar NOT NULL, user_id varchar NOT NULL);
		CREATE INDEX tm_team_user ON team_members (team_id, user_id);
		CREATE TABLE teams (id varchar PRIMARY KEY, org_id varchar NOT NULL);
		CREATE VIEW melange_tuples AS
		  SELECT 'user'::text AS subject_type, user_id::text AS subject_id, role::text AS relation, 'org'::text AS object_type, org_id::text AS object_id FROM members
		  UNION ALL
		  SELECT 'org'::text, org_id::text, 'owner'::text, 'repo'::text, id::text FROM repos
		  UNION ALL
		  SELECT 'user'::text, user_id::text, 'member'::text, 'team'::text, team_id::text FROM team_members
		  UNION ALL
		  -- a join: no single index spans it
		  SELECT 'user'::text, tm.user_id::text, 'member'::text, 'org'::text, t.org_id::text FROM team_members tm JOIN teams t ON t.id = tm.team_id
		  UNION ALL
		  -- a fixed object id: nothing to look up by
		  SELECT 'user'::text, user_id::text, 'admin'::text, 'platform'::text, 'main'::text FROM members WHERE role = 'admin'`)
	require.NoError(t, err)

	run := func() []doctor.CheckResult {
		report, err := doctor.New(db, "").Run(ctx)
		require.NoError(t, err)
		var out []doctor.CheckResult
		for _, c := range filterCategory(report, "Performance") {
			if c.Name == "source_table_indexes" {
				out = append(out, c)
			}
		}
		return out
	}

	checks := run()
	require.Len(t, checks, 1, "only members needs a composite: %+v", checks)
	assert.Equal(t, doctor.StatusWarn, checks[0].Status)
	assert.Equal(t, "CREATE INDEX idx_members_by_object ON members ((org_id::text), role, user_id);", checks[0].FixHint,
		"bigint org_id needs the ::text expression, varchar role and user_id don't")
	assert.Contains(t, checks[0].Details, "org → role branch")

	_, err = db.ExecContext(ctx, checks[0].FixHint)
	require.NoError(t, err)
	checks = run()
	require.Len(t, checks, 1)
	assert.Equal(t, doctor.StatusPass, checks[0].Status, "the hint's own index satisfies the check: %s", checks[0].Message)
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
