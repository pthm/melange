---
title: Creating Your Tuples View
description: "Create the melange_tuples view that maps your existing domain tables into authorization tuples, with no tuple sync."
weight: 4
prev: /docs/getting-started/project-setup
next: /docs/getting-started/first-migration
---

Melange reads authorization data from a SQL view called `melange_tuples`. This view maps your existing domain tables into the tuple format that Melange expects. There is no separate tuple store to keep in sync.

## Required Columns

The `melange_tuples` view must provide exactly these five columns, all of type `text`:

| Column | Description | Example |
|--------|-------------|---------|
| `subject_type` | Type of the entity performing the action | `'user'` |
| `subject_id` | ID of that entity | `'alice'` |
| `relation` | The relationship name | `'member'` |
| `object_type` | Type of the resource being accessed | `'repository'` |
| `object_id` | ID of that resource | `'42'` |

## Building Your View

Each `UNION ALL` section in the view maps one of your tables to tuples. Each section answers: what relationships does this table represent?

### Step 1: Identify Your Relationships

Look at your schema and list the relationships that need to be represented. Using the default Organization RBAC template:

| Source Table | Relationship | Subject | Object |
|-------------|-------------|---------|--------|
| `organization_members` | `owner`, `admin`, `member` | user → organization | |
| `repositories` | `org` (parent link) | organization → repository | |
| `repository_owners` | `owner` | user → repository | |

### Step 2: Write a UNION ALL for Each

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Organization memberships: user is {role} of organization
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Parent link: repository belongs to organization
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories

UNION ALL

-- Direct owners: user owns repository
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_owners;
```

### Step 3: Verify

After creating the view, check that it returns data:

```sql
SELECT * FROM melange_tuples LIMIT 10;
```

You should see rows like:

| subject_type | subject_id | relation | object_type | object_id |
|:------------|:----------|:--------|:-----------|:---------|
| user | 1 | owner | organization | 5 |
| user | 2 | member | organization | 5 |
| organization | 5 | org | repository | 42 |

## Key Points

- **Column types must be `text`**. Use `::text` casts for integer IDs. This is required for wildcard support (`'*'`) and mixed ID formats.
- **Use `UNION ALL`**, not `UNION`. `UNION` deduplicates rows, which adds overhead unnecessarily since tuples are naturally distinct.
- **Relation names must match your schema**. If your schema says `define member: [user]`, the view must produce `'member'` as the relation value, not `'is_member'` or `'membership'`.
- **The view is live**. It queries your tables directly, so permission changes take effect when the underlying data changes. Within a transaction, permission checks see uncommitted changes.

## Next Steps

- [Your First Migration](../first-migration): applying your schema and running health checks
- [Tuples View (Concepts)](../../concepts/tuples-view/): performance optimization, expression indexes, and scaling strategies
