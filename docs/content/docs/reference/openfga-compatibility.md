---
title: OpenFGA Compatibility
weight: 4
---

Melange provides **full OpenFGA Schema 1.1 compatibility**, running entirely in PostgreSQL. Melange is tested against the official OpenFGA test suite and passes all Schema 1.1 tests.

## Test Suite Compliance

Melange is validated against the **official OpenFGA compatibility test suite**. This ensures that authorization behavior matches the OpenFGA specification for all supported features. The test suite covers:

- Direct assignments and userset references
- Union, intersection, and exclusion operators
- Tuple-to-userset (parent inheritance)
- Wildcards and contextual tuples
- Complex nested permission patterns

All Schema 1.1 tests pass, ensuring reliable compatibility with OpenFGA schemas.

## Feature Support

| Feature                                  | Melange | Level        | Notes                                       |
| ---------------------------------------- | ------- | ------------ | ------------------------------------------- |
| **Direct assignment** `[user]`           | ✅      | **Full**     | Subjects explicitly granted via tuples      |
| **Userset references** `[type#relation]` | ✅      | **Full**     | Full runtime evaluation                     |
| **Wildcards** `[user:*]`                 | ✅      | **Full**     | Public access, `subject_id = '*'` in tuples |
| **Union (OR)**                           | ✅      | **Full**     | Any rule matches                            |
| **Intersection (AND)**                   | ✅      | **Full**     | All rules must match                        |
| **Exclusion (BUT NOT)**                  | ✅      | **Full**     | Recursive with parent inheritance           |
| **Computed relations**                   | ✅      | **Full**     | `implied_by` with transitive closure        |
| **Tuple-to-userset (FROM)**              | ✅      | **Full**     | Parent inheritance                          |
| **Contextual tuples**                    | ✅      | **Full**     | Temporary tuples passed with check request  |
| **Grouping `()`**                        | ✅      | **Full**     | Complex nested expressions                  |
| **Schema 1.0**                           | ⚠️      | **Untested** | Parser potentially works, but untested      |
| **Schema 1.1**                           | ✅      | **Full**     | Fully supported and tested                  |
| **Conditions**                           | ❌      | **None**     | CEL expressions not supported               |
| **Modular models**                       | ❌      | **None**     | Multi-file, `module`, `extend type`         |
| **Schema 1.2**                           | ❌      | **None**     | Conditions, modules                         |

## Supported Features

### Direct Assignment

Subjects can be directly assigned to relations:

```fga
type document
  relations
    define owner: [user]
    define editor: [user, team]
```

### Union (OR)

Multiple conditions combined with OR:

```fga
define admin: [user] or owner
define reader: [user] or writer or can_read from org
```

### Tuple-to-Userset (FROM)

Inherit permissions from parent objects:

```fga
type repository
  relations
    define org: [organization]
    define can_read: can_read from org
```

### Role Hierarchy (Implied-By)

Transitive closure is computed at schema load time:

```fga
define owner: [user]
define admin: [user] or owner     # owner implies admin
define member: [user] or admin    # owner and admin imply member
```

### Exclusion (BUT NOT)

Deny permission based on another relation:

```fga
define can_review: can_read from repo but not author
```

### Wildcards

Public access using wildcard subjects:

```fga
define public: [user:*]
define can_read: public or member
```

### Intersection (AND)

Require multiple conditions to all be satisfied:

```fga
define viewer: editor and can_read from org
define can_delete: owner and active
```

### Contextual Tuples

Pass temporary tuples with a check request without storing them in the database. Useful for evaluating hypothetical permissions or time-limited access:

```go
// Check with contextual tuples
allowed, err := checker.CheckWithContext(ctx, user, "can_read", doc, []melange.Tuple{
    {Subject: user, Relation: "temp_access", Object: doc},
})
```

## Unsupported Features

### Conditions (Schema 1.2)

CEL expressions for conditional authorization are not supported:

```fga
# NOT SUPPORTED
condition ip_allowed(user_ip: ipaddress) {
  user_ip.in_cidr("10.0.0.0/8")
}

define viewer: [user with ip_allowed]
```

### Modular Models (Schema 1.2)

Multi-file schemas and module extends are not supported:

```fga
# NOT SUPPORTED
module base

extend type document
  relations
    define new_relation: [user]
```

## Migration Path to OpenFGA

Melange is designed to be a stepping stone. If you outgrow its capabilities, you can migrate to the full OpenFGA service.

**Planned**: Support for the official OpenFGA client SDK is on the roadmap, which will make migration even simpler by allowing you to use the same client code with both Melange and OpenFGA.

### When to Consider Migrating

- You need **conditions** (Schema 1.2) for context-aware authorization with CEL expressions
- You need **modular models** for large multi-file schemas
- You need a **dedicated authorization service** for horizontal scaling
- Your **tuple volume** exceeds what PostgreSQL can handle efficiently

### Migration Steps

1. **Schema compatibility**: Your `.fga` schema files work with both Melange and OpenFGA.

2. **Export tuples**: Query your `melange_tuples` view to export tuples:

   ```sql
   SELECT subject_type, subject_id, relation, object_type, object_id
   FROM melange_tuples;
   ```

3. **Import to OpenFGA**: Use the OpenFGA API to write tuples:

   ```bash
   fga tuple write --store-id $STORE_ID \
     user:alice owner repository:123
   ```

4. **Update application code**: Replace Melange Checker with OpenFGA SDK:

   ```go
   // Before (Melange)
   allowed, err := checker.Check(ctx, user, "can_read", repo)

   // After (OpenFGA)
   resp, err := client.Check(ctx).Body(openfga.CheckRequest{
       TupleKey: openfga.TupleKey{
           User:     "user:alice",
           Relation: "can_read",
           Object:   "repository:123",
       },
   }).Execute()
   allowed := resp.GetAllowed()
   ```

5. **Sync tuples ongoing**: Replace the `melange_tuples` view with tuple writes to OpenFGA on data changes.

### What Changes

| Aspect                 | Melange                   | OpenFGA                   |
| ---------------------- | ------------------------- | ------------------------- |
| Deployment             | Embedded in PostgreSQL    | Separate service          |
| Tuple storage          | View over existing tables | Dedicated tuple store     |
| Transaction visibility | Yes (uncommitted changes) | No (eventual consistency) |
| Latency                | Single database query     | Network round-trip        |
| Scaling                | PostgreSQL limits         | Horizontal scaling        |

### What Stays the Same

- Schema files (`.fga`) are compatible
- Relation names and semantics
- Subject/object/relation model
- Basic permission patterns

## Testing Compatibility

Melange includes the official OpenFGA test suite to validate Schema 1.1 compliance. Run the full suite:

```bash
# Run the complete OpenFGA compatibility test suite
just test-openfga

# Run specific feature categories
just test-openfga-feature DirectAssignment
just test-openfga-feature ComputedUserset
just test-openfga-feature TupleToUserset
just test-openfga-feature Intersection
just test-openfga-feature Exclusion
just test-openfga-feature Wildcards
```

The test suite validates behavior against the official OpenFGA specification, ensuring that permission checks return identical results to the OpenFGA server for all supported patterns.

See [Contributing - Testing](../contributing/testing.md) for details on running the compatibility test suite.
