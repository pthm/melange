---
title: Testing
weight: 1
---

Melange validates compatibility with OpenFGA using their official test suite. This guide covers running tests, inspecting test cases, and debugging failures.

## OpenFGA Test Suite

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

## Inspecting Test Cases

Use the `dumptest` utility to understand what a test does:

```bash
# Build the dumptest utility
just build-dumptest

# List all available test names (148 tests)
just dump-openfga-list

# Dump a specific test to see its model, tuples, and assertions
just dump-openfga userset_defines_itself_1

# Dump tests matching a pattern
just dump-openfga-pattern "^wildcard"
just dump-openfga-pattern "computed_userset|ttu_"

# Dump all tests (warning: very long output)
just dump-openfga-all
```

Example output:

````
Test: userset_defines_itself_1
------------------------------

=== Stage 1 ===

Model:
```fga
model
  schema 1.1
type user
type document
  relations
    define viewer: [user]
```

Tuples: (none)

Check Assertions:
  [1] ALLOW: document:1#viewer | viewer | document:1
  [2] DENY: document:2#viewer | viewer | document:1

ListObjects Assertions:
  [1] user=document:1#viewer relation=viewer type=document
      => [document:1]

ListUsers Assertions:
  [1] object=document:1 relation=viewer filters=[document#viewer]
      => [document:1#viewer]
````

This is useful for:
- Understanding what a failing test expects
- Learning OpenFGA patterns by example
- Debugging why a specific assertion fails

## Unit Testing with Decision Overrides

For unit tests in your own code, use decision overrides to mock permission results:

### Always Allow

```go
func TestCreateDocument_Allowed(t *testing.T) {
    // Checker always returns true, no database required
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionAllow))

    ok, err := checker.Check(ctx, user, "can_write", doc)
    require.NoError(t, err)
    require.True(t, ok)
}
```

### Always Deny

```go
func TestCreateDocument_Denied(t *testing.T) {
    // Checker always returns false, no database required
    checker := melange.NewChecker(nil, melange.WithDecision(melange.DecisionDeny))

    ok, err := checker.Check(ctx, user, "can_write", doc)
    require.NoError(t, err)
    require.False(t, ok)
}
```

### Context-Based Overrides

For middleware or handler tests:

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

## Integration Testing

For full integration tests with a real PostgreSQL database:

```go
import (
    "github.com/pthm/melange/melange"
    "github.com/pthm/melange/pkg/migrator"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func setupTestDB(t *testing.T) *sql.DB {
    ctx := context.Background()

    container, err := postgres.Run(ctx, "postgres:17-alpine",
        postgres.WithDatabase("test"),
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
    err := migrator.MigrateFromString(ctx, db, `
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

    ok, err := checker.Check(ctx, alice, "viewer", doc)
    require.NoError(t, err)
    require.True(t, ok, "alice should be viewer as owner")

    ok, err = checker.Check(ctx, bob, "viewer", doc)
    require.NoError(t, err)
    require.False(t, ok, "bob should not be viewer")
}
```

## Debugging Failed Tests

### Enable SQL Logging

```go
type loggingDB struct {
    *sql.DB
    t *testing.T
}

func (l *loggingDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
    l.t.Logf("SQL: %s\nArgs: %v", query, args)
    return l.DB.QueryRowContext(ctx, query, args...)
}
```

### Inspect Database State

```sql
-- View all tuples
SELECT * FROM melange_tuples ORDER BY object_type, object_id;

-- Manual permission check
SELECT check_permission('user', 'alice', 'can_read', 'document', 'doc1');
```

## Test Commands Reference

```bash
# OpenFGA test suite
just test-openfga              # Supported features with gotestfmt
just test-openfga-feature X    # Single feature category
just test-openfga-name X       # Single test by name
just test-openfga-pattern X    # Tests matching regex
just test-openfga-list         # List all test names
just test-openfga-verbose      # Without gotestfmt
just test-openfga-full-check   # Full suite (many failures expected)

# OpenFGA test inspection
just dump-openfga-list         # List all test names
just dump-openfga X            # Dump a specific test by name
just dump-openfga-pattern X    # Dump tests matching regex
just dump-openfga-all          # Dump all tests

# Tools
just install-gotestfmt         # Install gotestfmt formatter
just build-dumptest            # Build the dumptest utility
```
