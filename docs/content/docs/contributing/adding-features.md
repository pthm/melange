---
title: Adding Features
weight: 5
---

Step-by-step guide for adding support for a new OpenFGA feature in Melange.

## Relation Pattern Reference

Each relation in an OpenFGA schema maps to a pattern based on which fields are set in the `RelationDefinition`:

| Pattern | Fields Set | Example |
|---------|-----------|---------|
| Direct `[user]` | `subject_type` | `define viewer: [user]` |
| Implied `viewer: owner` | `implied_by` | `define viewer: owner` |
| Parent `viewer from org` | `parent_relation`, `subject_type` | `define viewer: viewer from org` |
| Exclusion `but not author` | `excluded_relation` | `define viewer: writer but not author` |
| Userset `[group#member]` | `subject_type`, `subject_relation` | `define viewer: [group#member]` |
| Intersection `a and b` | `rule_group_id`, `rule_group_mode`, `check_relation` | `define viewer: writer and editor` |

## Steps

### 1. Add Parsing Logic (`pkg/parser/`)

The parser converts the OpenFGA AST into `schema.TypeDefinition` structs. If your feature introduces new syntax that the OpenFGA parser already handles, you only need to map the AST nodes to `RelationDefinition` fields.

If the OpenFGA parser doesn't handle the syntax yet, you may need to update the parser dependency first.

### 2. Add Schema Fields (`pkg/schema/`)

If the new feature requires new data in `RelationDefinition`, add fields to the struct in `pkg/schema/schema.go`. Update `ToAuthzModels()` to generate the corresponding `melange_model` rows.

### 3. Add SQL Generation (`internal/sqlgen/`)

This is the core of the work. Add a new code path in the SQL generation that produces the correct SQL for your pattern. Key files:

- `internal/sqlgen/check_queries.go` for check functions.
- `internal/sqlgen/list_queries.go` for list functions.
- `internal/sqlgen/sql.go` and `internal/sqlgen/expr.go` for the SQL DSL.

Use `PlpgsqlFunction` (not `SqlFunction`) for any function that references `melange_tuples`.

### 4. Add Tests

Add OpenFGA compatibility tests in `test/openfgatests/`. The test suite uses embedded YAML files from the OpenFGA project.

For custom test scenarios:

```go
func TestNewFeature(t *testing.T) {
    db := testutil.SetupDB(t)
    ctx := context.Background()

    err := migrator.MigrateFromString(ctx, db, `
model
  schema 1.1
type user
type document
  relations
    define viewer: [user]  // your new pattern here
`)
    require.NoError(t, err)

    // Create tuples view, insert data, check assertions
}
```

### 5. Debug with Dump Tools

Use the dump tools to inspect what's being generated:

```bash
# See the test schema, tuples, and assertions
just dump-openfga <test_name>

# See the generated SQL for a test
just dump-sql <test_name>

# See only the relation analysis
just dump-sql-analysis <test_name>
```

The SQL dump shows the actual generated functions, dispatcher routing, and relation feature analysis. This is essential for verifying your code generation is correct.

### 6. Run the Full Suite

```bash
# Run all supported feature tests
just test-openfga

# Run benchmarks to check for performance regressions
just bench-openfga
```

Ensure no existing tests regress. Performance regressions >20% in ns/op should be investigated.

### 7. Update Documentation

- Add the new pattern to [OpenFGA Compatibility](../../reference/openfga-compatibility/).
- If the feature introduces new runtime API surface, update [Go API](../../reference/go-api/).

## Tips

- Start by reading existing patterns that are similar to what you're adding. Exclusion and Intersection are the most complex examples.
- Use `Lit(value).SQL()` for SQL string escaping (doubles single quotes).
- The `p_visited` array parameter is for cycle detection. Pass it through when calling other check functions.
- Dispatchers are always regenerated. You don't need to manually update them.

## Next Steps

- [Architecture](../architecture/): the full compilation pipeline
- [Testing](../testing/): running and debugging tests
- [Project Structure](../project-structure/): where each piece of code lives
