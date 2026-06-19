---
title: Testing Authorization
weight: 7
---

Test your tuples view and schema together against a real PostgreSQL database. Mocking the permission layer risks divergence between test and production behavior.

## Integration Test Pattern (Go)

```go
func TestPermissions(t *testing.T) {
    // Set up test database with melange functions
    db := setupTestDB(t) // Your test helper that creates a fresh DB
    ctx := context.Background()

    // Apply melange migration
    m := migrator.NewMigrator(db, "schemas/schema.fga")
    _, err := m.Migrate(ctx)
    require.NoError(t, err)

    // Insert test data into domain tables
    tx, err := db.BeginTx(ctx, nil)
    require.NoError(t, err)
    defer tx.Rollback()

    _, err = tx.ExecContext(ctx, `
        INSERT INTO organization_members (user_id, organization_id, role)
        VALUES ('alice', 'org1', 'admin')
    `)
    require.NoError(t, err)

    _, err = tx.ExecContext(ctx, `
        INSERT INTO repositories (id, organization_id)
        VALUES ('repo1', 'org1')
    `)
    require.NoError(t, err)

    // Check permissions within the transaction
    checker := melange.NewChecker(tx)

    allowed, err := checker.Check(ctx,
        melange.Object{Type: "user", ID: "alice"},
        melange.Relation("can_read"),
        melange.Object{Type: "repository", ID: "repo1"},
    )
    require.NoError(t, err)
    assert.True(t, allowed, "admin should be able to read repos in their org")
}
```

Transaction rollback provides test isolation. Each test starts with a clean state without needing to truncate tables.

## Testing with SQL Directly

For database-only tests or debugging, call the SQL functions directly:

```sql
-- Set up test data
BEGIN;
INSERT INTO organization_members (user_id, organization_id, role) VALUES ('bob', 'org1', 'member');
INSERT INTO repositories (id, organization_id) VALUES ('repo1', 'org1');

-- Test permission
SELECT check_permission('user', 'bob', 'can_read', 'repository', 'repo1');
-- Expected: 1 (member inherits can_read via "member from org")

-- Test denial
SELECT check_permission('user', 'bob', 'can_delete', 'repository', 'repo1');
-- Expected: 0 (only owners can delete)

ROLLBACK;
```

## Common Test Patterns

### Role Hierarchy Inheritance

Verify that higher roles inherit lower role permissions:

```go
// Owner should have all permissions
for _, rel := range []string{"can_read", "can_write", "can_delete"} {
    allowed, err := checker.Check(ctx, owner, melange.Relation(rel), repo)
    require.NoError(t, err)
    assert.True(t, allowed, "owner should have %s", rel)
}

// Member should only have read
allowed, _ := checker.Check(ctx, member, "can_read", repo)
assert.True(t, allowed)

allowed, _ = checker.Check(ctx, member, "can_write", repo)
assert.False(t, allowed)
```

### Exclusion Denials

```go
// User is a writer but also blocked
_, _ = tx.ExecContext(ctx, `INSERT INTO writers (user_id, doc_id) VALUES ('eve', 'doc1')`)
_, _ = tx.ExecContext(ctx, `INSERT INTO blocked_users (user_id, doc_id) VALUES ('eve', 'doc1')`)

// "viewer: writer but not blocked" should deny
allowed, _ := checker.Check(ctx, eve, "viewer", doc)
assert.False(t, allowed, "blocked user should be denied even if they are a writer")
```

### Tuple Removal Removes Access

```go
// Grant access
_, _ = tx.ExecContext(ctx, `INSERT INTO team_members (user_id, team_id) VALUES ('alice', 'team1')`)
allowed, _ := checker.Check(ctx, alice, "member", team)
assert.True(t, allowed)

// Revoke access
_, _ = tx.ExecContext(ctx, `DELETE FROM team_members WHERE user_id = 'alice' AND team_id = 'team1'`)
allowed, _ = checker.Check(ctx, alice, "member", team)
assert.False(t, allowed)
```

### Wildcard Access

```go
// Make repository public
_, _ = tx.ExecContext(ctx, `UPDATE repositories SET is_public = true WHERE id = 'repo1'`)

// Any user should have read access
allowed, _ := checker.Check(ctx, randomUser, "can_read", repo)
assert.True(t, allowed, "public repos should be readable by any user")
```

## Testing Decision Overrides

Use decision overrides to test application behavior for both allowed and denied paths without database setup:

```go
func TestAdminBypass(t *testing.T) {
    // No database needed
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))
    allowed, err := checker.Check(ctx, anyUser, "anything", anyObject)
    assert.NoError(t, err)
    assert.True(t, allowed)
}

func TestDeniedPath(t *testing.T) {
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))
    allowed, err := checker.Check(ctx, anyUser, "anything", anyObject)
    assert.NoError(t, err)
    assert.False(t, allowed)
}
```

## Using melange doctor in CI

Run `melange doctor` as a CI step to catch configuration drift:

```yaml
# GitHub Actions example
- name: Health check
  run: melange doctor --db "$DATABASE_URL"
```

Doctor verifies that the schema, migrations, tuples view, and generated functions are all in sync. A non-zero exit code fails the CI step.

## Next Steps

- [Checking Permissions](../checking-permissions/): Checker API reference
- [Caching](../caching/): cache behavior in tests
- [CLI Reference](../../reference/cli/): `doctor` and `validate` commands
