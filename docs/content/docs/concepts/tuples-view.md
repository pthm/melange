---
title: Tuples View
description: "The melange_tuples view schema, query patterns for mapping domain tables to tuples, and indexing for performance."
weight: 4
---

{{< callout type="info" >}}
This page covers the tuples view schema, query patterns, and performance optimization. If you haven't created your first view yet, start with [Creating Your Tuples View](../../getting-started/tuples-view/).
{{< /callout >}}

The `melange_tuples` view is the bridge between your application's domain tables and Melange's permission checking functions. It provides a unified tuple format that the generated SQL functions query at runtime.

## Zero Tuple Sync

Melange queries your existing tables through the view rather than maintaining a separate tuple store. This means:

- Permissions are always in sync with your domain data.
- No replication lag or sync failures.
- Changes to domain tables immediately affect permissions.
- Permission checks within a transaction see uncommitted changes.

## View Schema

The view must provide exactly these five columns, all of type `text`:

| Column | Type | Description |
|--------|------|-------------|
| `subject_type` | `text` | Type of the subject (e.g., `'user'`, `'team'`) |
| `subject_id` | `text` | ID of the subject |
| `relation` | `text` | The relation name (e.g., `'owner'`, `'member'`, `'org'`) |
| `object_type` | `text` | Type of the object (e.g., `'organization'`, `'repository'`) |
| `object_id` | `text` | ID of the object |

### Why TEXT Columns?

All ID columns must be TEXT for two reasons:

1. **Wildcard support**. Melange uses `'*'` as a subject_id for public access (e.g., `user:*`). This cannot be stored in integer or UUID columns.
2. **ID type flexibility**. Your tables may use integers, UUIDs, or strings. TEXT accommodates all of them via `::text` casts.

The `::text` conversion prevents PostgreSQL from using your existing integer indexes. See [Expression Indexes](#expression-indexes) below.

## Wildcard Subjects

To grant public access, use `'*'` as the subject_id:

```sql
SELECT
    'user' AS subject_type,
    '*' AS subject_id,
    'reader' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories
WHERE is_public = true
```

The generated `check_permission` function automatically checks for both the specific subject_id and `'*'`.

## Query Patterns

The generated SQL functions use these access patterns against the view:

**Direct tuple check** (most common):

```sql
SELECT 1 FROM melange_tuples
WHERE object_type = ? AND object_id = ? AND relation = ?
  AND subject_type = ? AND (subject_id = ? OR subject_id = '*')
```

**Parent relation lookup** (for `viewer from parent` patterns):

```sql
SELECT subject_type, subject_id FROM melange_tuples
WHERE object_type = ? AND object_id = ? AND relation = ?
```

**List operations seed**:

```sql
SELECT object_id FROM melange_tuples
WHERE subject_type = ? AND subject_id = ? AND relation = ?
  AND object_type = ?
```

Understanding these patterns is key to choosing the right indexes.

## Recommended Indexes

Since `melange_tuples` is a view, you cannot index it directly. Create indexes on the underlying source tables.

### Object-Based Lookup

For "does user X have role Y on object Z?" queries:

```sql
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);
```

### Subject-Based Lookup

For list operations ("what objects does user X have role Y on?"):

```sql
CREATE INDEX idx_org_members_user
    ON organization_members (user_id, role, organization_id);
```

### Parent Relationship Lookup

For tables defining parent-child relationships:

```sql
CREATE INDEX idx_repos_parent
    ON repositories (id, organization_id);
```

## Expression Indexes

When your view casts integer IDs to text (`id::text`), PostgreSQL cannot use your existing integer indexes for those lookups. Expression indexes solve this:

```sql
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

With this index, queries through the view like `WHERE object_id = '123'` use an index scan instead of a sequential scan.

### When to Add Them

- Your view uses `::text` casts on integer or UUID columns.
- `EXPLAIN ANALYZE` shows sequential scans with filters like `Filter: ((id)::text = '123'::text)`.
- `melange doctor` reports missing expression indexes.

Run `ANALYZE` after creating expression indexes to update query planner statistics.

`melange doctor` detects missing expression indexes and provides exact `CREATE INDEX` statements.

## Verifying Your View

```sql
-- Check the view returns data
SELECT * FROM melange_tuples LIMIT 10;

-- Verify a specific permission
SELECT check_permission('user', '123', 'can_read', 'repository', '456');

-- Analyze query performance
EXPLAIN ANALYZE
SELECT * FROM melange_tuples
WHERE object_type = 'repository' AND object_id = '456';
```

Look for `Seq Scan` in the `EXPLAIN ANALYZE` output. Sequential scans on large tables indicate missing indexes.

## Common Issues

**"relation melange_tuples does not exist"**: the view hasn't been created. See [Creating Your Tuples View](../../getting-started/tuples-view/).

**Permissions not updating**: check that domain table changes are committed, and that the view query correctly maps your columns. Within a transaction, permission checks see uncommitted changes.

**Slow permission checks**: add expression indexes for `::text` columns, add standard indexes on source tables, and consider scaling strategies. Run `melange doctor` for specific recommendations.

## Next Steps

- [Creating Your Tuples View](../../getting-started/tuples-view/): step-by-step setup for your first view
- [Performance](../../reference/performance/): benchmark data and optimization guidance
