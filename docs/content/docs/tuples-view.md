---
title: Tuples View
weight: 2
---

The `melange_tuples` view is the bridge between your application's domain tables and Melange's permission checking system. This document explains how to create, optimize, and scale this view.

## Overview

Melange's `check_permission` function queries the `melange_tuples` view to find authorization tuples. You must create this view to map your existing domain tables (users, organizations, memberships, etc.) into the tuple format that Melange expects.

This "zero tuple sync" approach means:

- Permissions are always in sync with your domain data
- No separate tuple storage or replication
- Changes to domain tables immediately affect permissions
- Permission checks can see uncommitted changes within a transaction

## View Schema

The view must provide these columns:

| Column         | Type   | Description                                                 |
| -------------- | ------ | ----------------------------------------------------------- |
| `subject_type` | `text` | Type of the subject (e.g., `'user'`, `'team'`)              |
| `subject_id`   | `text` | ID of the subject                                           |
| `relation`     | `text` | The relation name (e.g., `'owner'`, `'member'`, `'org'`)    |
| `object_type`  | `text` | Type of the object (e.g., `'organization'`, `'repository'`) |
| `object_id`    | `text` | ID of the object                                            |

### Why TEXT Columns?

All ID columns (`subject_id`, `object_id`) must be TEXT for two important reasons:

**1. Wildcard Support**

Melange supports wildcard permissions using `'*'` as the subject_id. For example, `(user:*, reader, repository:123)` grants all users read access. This requires TEXT columns since `'*'` cannot be stored in integer or UUID columns.

**2. ID Type Flexibility**

Your application tables may use different ID types:

- Integer primary keys (`BIGINT`, `SERIAL`)
- UUIDs (`UUID`)
- String identifiers (`VARCHAR`)
- Composite keys

TEXT columns accommodate all of these. When creating your view, cast IDs to text:

```sql
-- Integer IDs
user_id::text AS subject_id

-- UUIDs
user_id::text AS subject_id  -- UUID casts to text automatically

-- Already text/varchar
user_id AS subject_id        -- No cast needed
```

{{< callout type="warning" >}}
**Performance Note**: The `::text` conversion prevents PostgreSQL from using integer indexes directly. See [Expression Indexes](#expression-indexes-for-text-id-conversion) below to restore efficient index usage.
{{< /callout >}}

## Basic Example

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Organization memberships: users have roles on organizations
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,              -- 'owner', 'admin', 'member'
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Repository ownership: organizations own repositories
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,             -- linking relation for parent inheritance
    'repository' AS object_type,
    id::text AS object_id
FROM repositories

UNION ALL

-- Repository collaborators: users have direct roles on repositories
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,              -- 'admin', 'writer', 'reader'
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_collaborators;
```

## Wildcard Subjects

To support public access (e.g., public repositories), use `'*'` as the subject_id:

```sql
-- Public repositories: any user can read
SELECT
    'user' AS subject_type,
    '*' AS subject_id,              -- wildcard: matches any user
    'reader' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories
WHERE is_public = true
```

The `check_permission` function automatically checks for both the specific subject_id and `'*'`.

## Performance Optimization

Since `melange_tuples` is a view over your application tables, you cannot create indexes directly on it. Instead, create indexes on the **underlying source tables** to optimize the query patterns used by `check_permission`.

### Query Patterns

The `check_permission` function uses these primary access patterns:

1. **Direct tuple check** (most common):

   ```sql
   SELECT 1 FROM melange_tuples
   WHERE object_type = ? AND object_id = ? AND relation = ?
     AND subject_type = ? AND (subject_id = ? OR subject_id = '*')
   ```

2. **Parent relation lookup**:

   ```sql
   SELECT subject_type, subject_id FROM melange_tuples
   WHERE object_type = ? AND object_id = ? AND relation = ?
   ```

3. **List operations seed**:
   ```sql
   SELECT object_id FROM melange_tuples
   WHERE subject_type = ? AND subject_id = ? AND relation = ?
     AND object_type = ?
   ```

### Recommended Index Patterns

For each source table in your `UNION ALL`, create indexes that cover the columns used in the view:

#### Pattern 1: Object-based lookup

For tables where you look up by object (the most common pattern):

```sql
-- Example: organization_members table
-- Covers: "does user X have role Y on organization Z?"
CREATE INDEX idx_org_members_lookup
    ON organization_members (organization_id, role, user_id);
```

#### Pattern 2: Subject-based lookup

For list operations that find all objects a subject can access:

```sql
-- Example: organization_members table
-- Covers: "what organizations does user X have role Y on?"
CREATE INDEX idx_org_members_user
    ON organization_members (user_id, role, organization_id);
```

#### Pattern 3: Parent relationship lookup

For tables that define parent-child relationships:

```sql
-- Example: repositories table (org -> repo relationship)
-- Covers: "what organization owns repository X?"
CREATE INDEX idx_repos_parent
    ON repositories (id, organization_id);
```

### Complete Index Example

For the view example above, create these indexes:

```sql
-- organization_members indexes
CREATE INDEX idx_org_members_org_role_user
    ON organization_members (organization_id, role, user_id);
CREATE INDEX idx_org_members_user_role_org
    ON organization_members (user_id, role, organization_id);

-- repositories indexes (for parent relationship)
CREATE INDEX idx_repos_id_org
    ON repositories (id, organization_id);
CREATE INDEX idx_repos_org_id
    ON repositories (organization_id, id);

-- repository_collaborators indexes
CREATE INDEX idx_repo_collabs_repo_role_user
    ON repository_collaborators (repository_id, role, user_id);
CREATE INDEX idx_repo_collabs_user_role_repo
    ON repository_collaborators (user_id, role, repository_id);
```

### Expression Indexes for Text ID Conversion

The `melange_tuples` view converts integer IDs to text (e.g., `id::text AS object_id`). This conversion prevents PostgreSQL from using your existing integer-based primary key and foreign key indexes when querying through the view.

**Expression indexes** (also called functional indexes) solve this by indexing the result of the `::text` conversion:

```sql
-- Index the text-converted ID for object lookups
CREATE INDEX idx_org_members_text
    ON organization_members ((organization_id::text), (user_id::text));
```

With this index, queries like `WHERE object_id = '123'` can use an index scan instead of a sequential scan.

#### Why Expression Indexes Matter

Without expression indexes, PostgreSQL must perform sequential scans on each table in the UNION ALL view. At scale, this causes severe performance degradation:

| Scale       | Without Expression Indexes | With Expression Indexes |
| ----------- | -------------------------- | ----------------------- |
| 10K tuples  | ~1ms                       | ~0.3ms                  |
| 100K tuples | ~7ms                       | ~0.5ms                  |
| 1M tuples   | ~80ms                      | ~3ms                    |

The improvement is most dramatic for **exclusion patterns** (`but not author`) and **tuple-to-userset patterns** (`viewer from parent`) which require multiple view lookups per permission check.

#### Complete Expression Index Example

For the view example above, add these expression indexes alongside your regular indexes:

```sql
-- Expression indexes for text ID conversion
-- These enable efficient lookups through the UNION ALL view

-- organization_members: object and subject lookups
CREATE INDEX idx_org_members_obj_text
    ON organization_members ((organization_id::text), (user_id::text));
CREATE INDEX idx_org_members_subj_text
    ON organization_members ((user_id::text), (organization_id::text));

-- repositories: parent relationship lookup
CREATE INDEX idx_repos_id_text
    ON repositories ((id::text));
CREATE INDEX idx_repos_org_text
    ON repositories ((id::text), (organization_id::text));

-- repository_collaborators: object and subject lookups
CREATE INDEX idx_repo_collabs_obj_text
    ON repository_collaborators ((repository_id::text), (user_id::text));
CREATE INDEX idx_repo_collabs_subj_text
    ON repository_collaborators ((user_id::text), (repository_id::text));
```

#### When to Use Expression Indexes

Add expression indexes when:

- You have more than 10K tuples
- Permission checks involve exclusion patterns (`but not`)
- Permission checks involve tuple-to-userset patterns (`viewer from parent`)
- You see sequential scans in `EXPLAIN ANALYZE` output for view queries

Expression indexes add minimal write overhead and can dramatically improve read performance.

{{< callout type="info" >}}
**Tip**: Run `ANALYZE` after creating expression indexes to update PostgreSQL's query planner statistics.
{{< /callout >}}

## Scaling Strategies

For high-traffic applications or large datasets, consider these strategies to improve performance.

### Strategy 1: Materialized View

Convert the view to a materialized view for faster queries:

```sql
CREATE MATERIALIZED VIEW melange_tuples AS
-- ... same query as your regular view ...
WITH DATA;

-- Create indexes directly on the materialized view
CREATE INDEX idx_mt_object
    ON melange_tuples (object_type, object_id, relation, subject_type, subject_id);
CREATE INDEX idx_mt_subject
    ON melange_tuples (subject_type, subject_id, relation, object_type, object_id);
```

#### Refresh Strategies

**Periodic refresh** - Simple but permissions may be stale:

```sql
-- Refresh every minute via cron or pg_cron
REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples;
```

**On-demand refresh** - Refresh after batch operations:

```sql
-- In your application after bulk updates
db.ExecContext(ctx, "REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples")
```

**Trigger-based refresh** - Refresh on data changes (use sparingly):

```sql
CREATE OR REPLACE FUNCTION refresh_tuples_trigger()
RETURNS TRIGGER AS $$
BEGIN
    REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER refresh_tuples_on_members
    AFTER INSERT OR UPDATE OR DELETE ON organization_members
    FOR EACH STATEMENT
    EXECUTE FUNCTION refresh_tuples_trigger();
```

#### Trade-offs

| Aspect                 | Regular View                 | Materialized View        |
| ---------------------- | ---------------------------- | ------------------------ |
| Query speed            | Slower (joins at query time) | Faster (pre-computed)    |
| Data freshness         | Always current               | Stale until refresh      |
| Transaction visibility | Sees uncommitted changes     | Only sees committed data |
| Index support          | Must index source tables     | Direct indexes on view   |
| Maintenance            | None                         | Refresh required         |

### Strategy 2: Dedicated Tuples Table

For very high-traffic scenarios, maintain a separate `melange_tuples` table instead of a view:

```sql
CREATE TABLE melange_tuples (
    subject_type text NOT NULL,
    subject_id text NOT NULL,
    relation text NOT NULL,
    object_type text NOT NULL,
    object_id text NOT NULL,
    PRIMARY KEY (object_type, object_id, relation, subject_type, subject_id)
);

CREATE INDEX idx_tuples_subject
    ON melange_tuples (subject_type, subject_id, relation, object_type);
```

Sync the table using triggers on your domain tables:

```sql
CREATE OR REPLACE FUNCTION sync_org_member_tuple()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO melange_tuples
        VALUES ('user', NEW.user_id::text, NEW.role, 'organization', NEW.organization_id::text);
    ELSIF TG_OP = 'UPDATE' THEN
        UPDATE melange_tuples
        SET relation = NEW.role
        WHERE subject_type = 'user'
          AND subject_id = OLD.user_id::text
          AND object_type = 'organization'
          AND object_id = OLD.organization_id::text;
    ELSIF TG_OP = 'DELETE' THEN
        DELETE FROM melange_tuples
        WHERE subject_type = 'user'
          AND subject_id = OLD.user_id::text
          AND object_type = 'organization'
          AND object_id = OLD.organization_id::text;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER sync_org_members
    AFTER INSERT OR UPDATE OR DELETE ON organization_members
    FOR EACH ROW EXECUTE FUNCTION sync_org_member_tuple();
```

### Strategy 3: Application-Level Caching

Combine any of the above with Melange's built-in cache or build your own:

```go
cache := melange.NewCache(
    melange.WithTTL(time.Minute),
    melange.WithMaxSize(10000),
)
checker := melange.NewChecker(db, melange.WithCache(cache))
```

This provides:

- Sub-microsecond repeated checks (~79ns vs ~1ms)
- Reduced database load
- Configurable TTL for freshness requirements

### Scaling Recommendations

| Scale           | Recommended Approach                                                        |
| --------------- | --------------------------------------------------------------------------- |
| < 10K tuples    | Regular view + source table indexes                                         |
| 10K-100K tuples | Regular view + expression indexes + application cache                       |
| 100K-1M tuples  | Regular view + expression indexes + application cache, or materialized view |
| > 1M tuples     | Dedicated table with trigger sync + application cache                       |

{{< callout type="warning" >}}
**Expression indexes are critical at scale.** Without them, permission checks involving exclusions or parent relationships can be 10-15x slower due to sequential scans through the UNION ALL view.
{{< /callout >}}

## Verifying Your View

After creating the view, verify it works:

```sql
-- Check the view exists and has data
SELECT * FROM melange_tuples LIMIT 10;

-- Verify a specific permission
SELECT check_permission('user', '123', 'can_read', 'repository', '456');

-- Analyze query performance
EXPLAIN ANALYZE
SELECT * FROM melange_tuples
WHERE object_type = 'repository' AND object_id = '456';
```

## Common Issues

### "relation melange_tuples does not exist"

The view hasn't been created. Create it using the patterns above.

### Permissions not updating

If using a regular view, check that:

1. Your domain table changes are committed
2. The view query correctly maps your table columns

If using a materialized view, refresh it:

```sql
REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples;
```

### Slow permission checks

1. Run `EXPLAIN ANALYZE` on the underlying queries
2. Check for sequential scans on source tables—add expression indexes for `::text` columns
3. Add standard indexes to source tables (see patterns above)
4. Consider a materialized view or dedicated table for read-heavy workloads
5. Enable application-level caching

**Diagnosing with EXPLAIN ANALYZE:**

```sql
EXPLAIN ANALYZE
SELECT 1 FROM melange_tuples
WHERE object_type = 'repository' AND object_id = '123'
  AND relation = 'reader' AND subject_type = 'user';
```

Look for `Seq Scan` in the output. If you see sequential scans with filters like `Filter: ((id)::text = '123'::text)`, you need expression indexes on the `(id::text)` column.
