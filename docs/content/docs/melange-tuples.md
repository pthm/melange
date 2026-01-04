---
title: The melange_tuples View
weight: 10
---

The `melange_tuples` view is the bridge between your application's domain tables and Melange's permission checking system. This document explains how to create and optimize this view.

## Overview

Melange's `check_permission` function queries the `melange_tuples` view to find authorization tuples. You must create this view to map your existing domain tables (users, organizations, memberships, etc.) into the tuple format that Melange expects.

This "zero tuple sync" approach means:
- Permissions are always in sync with your domain data
- No separate tuple storage or replication
- Changes to domain tables immediately affect permissions
- Permission checks can see uncommitted changes within a transaction

## View Schema

The view must provide these columns:

| Column | Type | Description |
|--------|------|-------------|
| `subject_type` | `text` | Type of the subject (e.g., `'user'`, `'team'`) |
| `subject_id` | `text` | ID of the subject |
| `relation` | `text` | The relation name (e.g., `'owner'`, `'member'`, `'org'`) |
| `object_type` | `text` | Type of the object (e.g., `'organization'`, `'repository'`) |
| `object_id` | `text` | ID of the object |

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

### Complete Example

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

## Materialized Views

For very high-traffic applications, consider using a materialized view:

```sql
CREATE MATERIALIZED VIEW melange_tuples AS
-- ... same query as above ...
WITH DATA;

-- Create indexes directly on the materialized view
CREATE INDEX idx_mt_object ON melange_tuples (object_type, object_id, relation);
CREATE INDEX idx_mt_subject ON melange_tuples (subject_type, subject_id, relation);

-- Refresh periodically or on-demand
REFRESH MATERIALIZED VIEW CONCURRENTLY melange_tuples;
```

**Trade-offs**:
- Faster permission checks (indexes on the view itself)
- Permissions may be stale until refresh
- Loses transaction isolation (can't see uncommitted changes)
- Requires periodic refresh strategy

## Verifying Your View

After creating the view, verify it works:

```sql
-- Check the view exists
SELECT * FROM melange_tuples LIMIT 10;

-- Verify a specific permission
SELECT check_permission('user', '123', 'can_read', 'repository', '456');

-- Check migration status (includes view existence)
-- In Go: migrator.GetStatus(ctx)
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
2. Add indexes to source tables (see patterns above)
3. Consider a materialized view for read-heavy workloads
