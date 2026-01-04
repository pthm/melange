---
title: OpenFGA Compatibility
weight: 10
---

Melange implements a subset of the OpenFGA specification, providing compatibility with OpenFGA schemas while running entirely in PostgreSQL.

## Feature Support

| Feature | OpenFGA | Melange | Notes |
|---------|---------|---------|-------|
| **Direct assignment** `[user]` | Yes | **Full** | Subjects explicitly granted via tuples |
| **Userset references** `[type#relation]` | Yes | **Partial** | Parsed but limited runtime evaluation |
| **Wildcards** `[user:*]` | Yes | **Full** | Public access, `subject_id = '*'` in tuples |
| **Union (OR)** | Yes | **Full** | Any rule matches |
| **Intersection (AND)** | Yes | **Not enforced** | Parsed but treated as union |
| **Exclusion (BUT NOT)** | Yes | **Full** | Recursive with parent inheritance |
| **Computed relations** | Yes | **Full** | `implied_by` with transitive closure |
| **Tuple-to-userset (FROM)** | Yes | **Full** | Parent inheritance |
| **Conditions** | Yes (1.2+) | **None** | CEL expressions, contextual params |
| **Modular models** | Yes (1.2+) | **None** | Multi-file, `module`, `extend type` |
| **Schema 1.0** | Yes | **Untested** | Parser may handle, not validated |
| **Schema 1.1** | Yes | **Full** | Primary supported version |
| **Schema 1.2** | Yes | **None** | Conditions, modules |
| **Grouping `()`** | Yes | **Partial** | Parser handles, flattened on extraction |

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

## Unsupported Features

### Conditions (Schema 1.2)

CEL expressions and contextual parameters are not supported:

```fga
# NOT SUPPORTED
condition ip_allowed(user_ip: ipaddress) {
  user_ip.in_cidr("10.0.0.0/8")
}

define viewer: [user with ip_allowed]
```

### Intersection (AND)

Intersection is parsed but treated as union at runtime:

```fga
# Parsed but NOT enforced correctly
define viewer: editor and can_read from org
```

### Modular Models

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

### When to Consider Migrating

- You need **conditions** for context-aware authorization
- You need **intersection (AND)** logic
- You need **modular models** for large schemas
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

| Aspect | Melange | OpenFGA |
|--------|---------|---------|
| Deployment | Embedded in PostgreSQL | Separate service |
| Tuple storage | View over existing tables | Dedicated tuple store |
| Transaction visibility | Yes (uncommitted changes) | No (eventual consistency) |
| Latency | Single database query | Network round-trip |
| Scaling | PostgreSQL limits | Horizontal scaling |

### What Stays the Same

- Schema files (`.fga`) are compatible
- Relation names and semantics
- Subject/object/relation model
- Basic permission patterns

## Testing Compatibility

Run the OpenFGA test suite against Melange:

```bash
# Run supported feature tests
just test-openfga

# Run specific category
just test-openfga-feature DirectAssignment
just test-openfga-feature ComputedUserset
just test-openfga-feature TupleToUserset
```

See [Contributing - Testing]({{< relref "contributing/testing" >}}) for details on running the compatibility test suite.
