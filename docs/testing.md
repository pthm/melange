# Testing Guide

This document covers testing strategies for applications using Melange, as well as running Melange's own test suites.

---

## Overview

Melange provides multiple testing approaches depending on your needs:

| Strategy | Database Required | Speed | Use Case |
|----------|-------------------|-------|----------|
| **Decision overrides** | No | Fast | Unit tests, mocking permission checks |
| **Context-based overrides** | No | Fast | Handler/middleware tests |
| **Integration tests** | Yes (PostgreSQL) | Slower | End-to-end permission validation |
| **OpenFGA test suite** | Yes (PostgreSQL) | Slower | Validating DSL compatibility |

---

## Unit Testing with Decision Overrides

For unit tests where you want to test application logic without a database, use decision overrides to mock permission results.

### Always Allow

```go
func TestCreateDocument_Allowed(t *testing.T) {
    // Checker always returns true, no database required
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

    ok, err := checker.Check(ctx, user, "can_write", doc)
    require.NoError(t, err)
    require.True(t, ok)  // Always true
}
```

### Always Deny

```go
func TestCreateDocument_Denied(t *testing.T) {
    // Checker always returns false, no database required
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

    ok, err := checker.Check(ctx, user, "can_write", doc)
    require.NoError(t, err)
    require.False(t, ok)  // Always false
}
```

### Testing Both Paths

```go
func TestDocumentService(t *testing.T) {
    tests := []struct {
        name     string
        decision melange.Decision
        wantErr  bool
    }{
        {"allowed", melange.DecisionAllow, false},
        {"denied", melange.DecisionDeny, true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            checker := melange.NewChecker(nil, melange.WithDecision(tt.decision))
            svc := NewDocumentService(checker)

            err := svc.Create(ctx, user, doc)
            if tt.wantErr {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

---

## Context-Based Decision Overrides

For middleware or handler tests where the checker is shared, use context-based decisions.

### Setup

```go
// Create checker with context decision support
checker := melange.NewChecker(db, melange.WithContextDecision())

// In tests, override via context
ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)

// All checks in this context return true
ok, _ := checker.Check(ctx, user, "can_read", doc)  // true
```

### Handler Testing Example

```go
func TestHandler(t *testing.T) {
    checker := melange.NewChecker(nil, melange.WithContextDecision())
    handler := NewHandler(checker)

    t.Run("authorized request", func(t *testing.T) {
        ctx := melange.WithDecisionContext(context.Background(), melange.DecisionAllow)
        req := httptest.NewRequest("GET", "/docs/1", nil).WithContext(ctx)
        rec := httptest.NewRecorder()

        handler.GetDocument(rec, req)
        require.Equal(t, http.StatusOK, rec.Code)
    })

    t.Run("unauthorized request", func(t *testing.T) {
        ctx := melange.WithDecisionContext(context.Background(), melange.DecisionDeny)
        req := httptest.NewRequest("GET", "/docs/1", nil).WithContext(ctx)
        rec := httptest.NewRecorder()

        handler.GetDocument(rec, req)
        require.Equal(t, http.StatusForbidden, rec.Code)
    })
}
```

---

## Integration Testing

For full integration tests that validate your actual authorization schema, use a real PostgreSQL database.

### Using testcontainers-go

```go
import (
    "context"
    "testing"

    "github.com/pthm/melange"
    "github.com/pthm/melange/tooling"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func setupTestDB(t *testing.T) *sql.DB {
    ctx := context.Background()

    container, err := postgres.Run(ctx, "postgres:17-alpine",
        postgres.WithDatabase("test"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2),
        ),
    )
    require.NoError(t, err)
    t.Cleanup(func() { container.Terminate(ctx) })

    connStr, err := container.ConnectionString(ctx, "sslmode=disable")
    require.NoError(t, err)

    db, err := sql.Open("postgres", connStr)
    require.NoError(t, err)
    t.Cleanup(func() { db.Close() })

    return db
}

func TestPermissions(t *testing.T) {
    ctx := context.Background()
    db := setupTestDB(t)

    // Apply Melange schema
    err := tooling.MigrateFromString(ctx, db, `
model
  schema 1.1

type user

type document
  relations
    define owner: [user]
    define viewer: [user] or owner
`)
    require.NoError(t, err)

    // Create melange_tuples view
    _, err = db.ExecContext(ctx, `
        CREATE TABLE documents (id TEXT PRIMARY KEY, owner_id TEXT);
        CREATE VIEW melange_tuples AS
        SELECT 'user' AS subject_type, owner_id AS subject_id,
               'owner' AS relation, 'document' AS object_type, id AS object_id
        FROM documents;
    `)
    require.NoError(t, err)

    // Insert test data
    _, err = db.ExecContext(ctx, `INSERT INTO documents VALUES ('doc1', 'alice')`)
    require.NoError(t, err)

    // Test permissions
    checker := melange.NewChecker(db)

    alice := melange.Object{Type: "user", ID: "alice"}
    bob := melange.Object{Type: "user", ID: "bob"}
    doc := melange.Object{Type: "document", ID: "doc1"}

    // Owner has viewer permission
    ok, err := checker.Check(ctx, alice, "viewer", doc)
    require.NoError(t, err)
    require.True(t, ok, "alice should be viewer as owner")

    // Non-owner denied
    ok, err = checker.Check(ctx, bob, "viewer", doc)
    require.NoError(t, err)
    require.False(t, ok, "bob should not be viewer")
}
```

### Transaction Testing

Test that permission checks see uncommitted changes:

```go
func TestTransactionVisibility(t *testing.T) {
    ctx := context.Background()
    db := setupTestDB(t)

    // ... setup schema and view ...

    tx, err := db.BeginTx(ctx, nil)
    require.NoError(t, err)
    defer tx.Rollback()

    // Insert within transaction
    _, err = tx.ExecContext(ctx, `INSERT INTO documents VALUES ('doc2', 'carol')`)
    require.NoError(t, err)

    // Checker on transaction sees uncommitted data
    checker := melange.NewChecker(tx)
    carol := melange.Object{Type: "user", ID: "carol"}
    doc := melange.Object{Type: "document", ID: "doc2"}

    ok, err := checker.Check(ctx, carol, "owner", doc)
    require.NoError(t, err)
    require.True(t, ok, "should see uncommitted tuple")

    // Checker on db does NOT see uncommitted data
    dbChecker := melange.NewChecker(db)
    ok, err = dbChecker.Check(ctx, carol, "owner", doc)
    require.NoError(t, err)
    require.False(t, ok, "should not see uncommitted tuple")
}
```

---

## OpenFGA Test Suite

Melange includes an adapter to run the official OpenFGA test suite against our implementation. This validates DSL compatibility for supported features.

### Running Tests

```bash
# Run all supported feature tests (recommended)
just test-openfga

# Run a specific feature category
just test-openfga-feature Wildcards
just test-openfga-feature Exclusion
just test-openfga-feature TupleToUserset

# Run a single test by name
just test-openfga-name wildcard_direct
just test-openfga-name computed_userset

# Run tests matching a pattern
just test-openfga-pattern "^exclusion"
just test-openfga-pattern "tuple_to_userset"

# List all available test names
just test-openfga-list

# Run without gotestfmt formatting
just test-openfga-verbose

# Run full suite (includes unsupported features - many will fail)
just test-openfga-full-check
```

### Feature Test Categories

| Category | Command | Tests |
|----------|---------|-------|
| Direct Assignment | `just test-openfga-feature DirectAssignment` | `this`, `this_with_contextual_tuples`, `this_and_union` |
| Computed Userset | `just test-openfga-feature ComputedUserset` | Role hierarchy via `or` |
| Tuple-to-Userset | `just test-openfga-feature TupleToUserset` | Parent inheritance via `from` |
| Wildcards | `just test-openfga-feature Wildcards` | Public access via `[user:*]` |
| Exclusion | `just test-openfga-feature Exclusion` | `but not` patterns |
| Union | `just test-openfga-feature Union` | Multiple `or` branches |
| Complex Patterns | `just test-openfga-feature ComplexPatterns` | Nested combinations |
| Cycle Handling | `just test-openfga-feature CycleHandling` | Cycle detection |

### Understanding Test Output

With `gotestfmt`, passing tests show green checkmarks:

```
📦 github.com/pthm/melange/test/openfgatests
  ✅ TestOpenFGA_Wildcards (110ms)
  ✅ TestOpenFGA_Wildcards/wildcard_direct (80ms)
  ✅ TestOpenFGA_Wildcards/wildcard_direct/stage_0 (20ms)
  ✅ TestOpenFGA_Wildcards/wildcard_direct/stage_0/check_0 (0s)
```

Failed tests show red X marks with details:

```
  ❌ TestOpenFGA_Unsupported/some_test
      Error: expected: true, actual: false
      Messages: check user:alice:viewer on document:1
```

### Excluded Tests

Some OpenFGA tests are excluded because they use unsupported features:

| Feature | Reason | Example Tests |
|---------|--------|---------------|
| Userset references `[type#relation]` | Partial support only | `ttu_mix_with_userset` |
| Intersection/AND | Not implemented | `cycle_and_true_return_false` |
| Conditions (ABAC) | Not supported | `wildcard_with_condition` |
| Complex cycles | Edge cases | `true_butnot_cycle_return_false` |

See `docs/openfga-support.md` for the full feature compatibility matrix.

---

## Test Commands Reference

### Justfile Commands

```bash
# All tests
just test                    # Unit + integration tests
just test-unit               # Unit tests only (no Docker)
just test-integration        # Integration tests (requires Docker)
just test-race               # Tests with race detection

# OpenFGA suite
just test-openfga            # Supported features with gotestfmt
just test-openfga-feature X  # Single feature category
just test-openfga-name X     # Single test by name
just test-openfga-pattern X  # Tests matching regex
just test-openfga-list       # List all test names
just test-openfga-verbose    # Without gotestfmt
just test-openfga-full-check # Full suite (many failures expected)

# Tools
just install-gotestfmt       # Install gotestfmt formatter
```

### Direct Go Commands

```bash
# Run specific test
cd test && go test -v -run TestBasicCheck ./openfgatests/...

# Run with environment variable
cd test && OPENFGA_TEST_NAME=wildcard_direct go test -v -run TestOpenFGAByName ./openfgatests/...

# Run with pattern
cd test && OPENFGA_TEST_PATTERN="^tuple" go test -v -run TestOpenFGAByPattern ./openfgatests/...

# Run with JSON output for gotestfmt
cd test && go test -json -run "TestOpenFGA_" ./openfgatests/... | gotestfmt
```

---

## Writing Custom Tests

### Testing Your Schema

Create tests specific to your authorization schema:

```go
func TestMyAppPermissions(t *testing.T) {
    db := setupTestDB(t)
    ctx := context.Background()

    // Apply your schema
    err := tooling.Migrate(ctx, db, "path/to/schemas")
    require.NoError(t, err)

    // Create your tuples view
    _, err = db.ExecContext(ctx, myTuplesViewSQL)
    require.NoError(t, err)

    // Test scenarios
    checker := melange.NewChecker(db)

    t.Run("org admin can delete repo", func(t *testing.T) {
        // Setup: admin -> org, org -> repo
        setupOrgWithAdmin(t, db, "org1", "alice")
        setupRepo(t, db, "repo1", "org1")

        alice := melange.Object{Type: "user", ID: "alice"}
        repo := melange.Object{Type: "repository", ID: "repo1"}

        ok, err := checker.Check(ctx, alice, "can_delete", repo)
        require.NoError(t, err)
        require.True(t, ok)
    })
}
```

### Property-Based Testing

For comprehensive coverage, consider property-based tests:

```go
func TestListCheckParity(t *testing.T) {
    db := setupTestDB(t)
    checker := melange.NewChecker(db)

    // Property: ListObjects results should all pass Check
    objects, err := checker.ListObjects(ctx, user, "can_read", "document")
    require.NoError(t, err)

    for _, objID := range objects {
        obj := melange.Object{Type: "document", ID: objID}
        ok, err := checker.Check(ctx, user, "can_read", obj)
        require.NoError(t, err)
        require.True(t, ok, "ListObjects returned %s but Check denied", objID)
    }
}
```

---

## Debugging Failed Tests

### Enable Verbose SQL Logging

```go
// Wrap your db with logging
type loggingDB struct {
    *sql.DB
    t *testing.T
}

func (l *loggingDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
    l.t.Logf("SQL: %s\nArgs: %v", query, args)
    return l.DB.QueryRowContext(ctx, query, args...)
}
```

### Inspect melange_model

```sql
-- View loaded schema rules
SELECT * FROM melange_model ORDER BY object_type, relation;

-- View closure table
SELECT * FROM melange_relation_closure ORDER BY object_type, relation;
```

### Inspect melange_tuples

```sql
-- View all tuples
SELECT * FROM melange_tuples ORDER BY object_type, object_id;

-- Check specific tuple exists
SELECT * FROM melange_tuples
WHERE subject_type = 'user' AND subject_id = 'alice'
  AND relation = 'owner' AND object_type = 'document';
```

### Manual Permission Check

```sql
-- Direct SQL permission check
SELECT check_permission('user', 'alice', 'can_read', 'document', 'doc1');
-- Returns 1 (allowed) or 0 (denied)
```
