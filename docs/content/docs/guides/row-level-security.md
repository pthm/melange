---
title: Row-Level Security
description: "Enforce Melange permissions inside PostgreSQL with row-level security policies built on check_permission and list_accessible_objects."
weight: 10
---

Melange's generated functions are ordinary SQL functions that read your tables directly, so you can call them from [row-level security](https://www.postgresql.org/docs/current/ddl-rowsecurity.html) (RLS) policies. The database then enforces your authorization model on every query, whatever client runs it.

## Basic Setup

Pass the current user to the database as a setting, then reference it in your policies:

```sql
ALTER TABLE documents ENABLE ROW LEVEL SECURITY;

-- Reads: a set lookup, evaluated once per query (see Performance below)
CREATE POLICY documents_select ON documents FOR SELECT USING (
    id IN (SELECT object_id FROM list_accessible_objects(
        'user', current_setting('app.user_id', true), 'viewer', 'document', NULL, NULL)));

-- Writes: a per-row check, which is cheap for single-row statements
CREATE POLICY documents_update ON documents FOR UPDATE USING (
    check_permission('user', current_setting('app.user_id', true), 'editor', 'document', id) = 1);

CREATE POLICY documents_delete ON documents FOR DELETE USING (
    check_permission('user', current_setting('app.user_id', true), 'owner', 'document', id) = 1);
```

Then set the user at the start of each transaction:

```sql
BEGIN;
SELECT set_config('app.user_id', '123', true);  -- true = transaction-local
SELECT * FROM documents;  -- only rows user 123 can view
COMMIT;
```

The second argument to `current_setting` (`missing_ok = true`) makes it return `NULL` instead of raising an error when the setting is absent. A `NULL` subject has no permissions, so the query returns no rows.

{{< callout type="warning" >}}
**Use transaction-local settings with connection pools.** `set_config(..., false)` (or plain `SET`) lasts for the whole session. With a pooled connection, the next request that borrows it inherits the previous user's identity. Set it with `is_local = true` (or `SET LOCAL`) inside a transaction, so it is cleared on commit or rollback.
{{< /callout >}}

## Requirements

### Create policies after migrating

A policy that calls `check_permission` cannot be created until the function exists. Run `melange migrate` first, then create your policies. Later migrations replace function bodies in place, so existing policies keep working when the model changes.

{{< callout type="warning" >}}
**Call the dispatchers, not the specialized functions.** Reference `check_permission` and `list_accessible_objects` in policies, not generated functions such as `check_document_viewer`. Migrations drop functions that the model no longer needs with `CASCADE`, which silently drops any policy that depends on them.
{{< /callout >}}

### Use a role that does not bypass RLS

Superusers, roles with `BYPASSRLS`, and the table owner skip RLS entirely. Your application must connect as a separate role (or switch to one with `SET ROLE`). Use `ALTER TABLE ... FORCE ROW LEVEL SECURITY` if the owner must also be subject to policies.

### Grant read access to the tuples source

Policies run with the querying role's privileges, so that role needs `SELECT` on `melange_tuples`:

```sql
GRANT SELECT ON melange_tuples TO app_user;
```

The generated functions are executable by `PUBLIC` by default. If you have revoked that, grant `EXECUTE` on them too.

### Keep the tuples view as a regular view

`melange_tuples` usually reads from the same tables you protect. For example, a `documents.owner_id` column becomes an `owner` tuple. That only works because PostgreSQL views run with their **owner's** privileges by default, and the table owner bypasses RLS, so the view sees every row.

Do not set `security_invoker = true` on the view. With that option, the view applies the caller's RLS policies, which call `check_permission`, which reads the view again. The query fails with `stack depth limit exceeded`.

### Don't authorize inserts through the inserted row

A `WITH CHECK` policy is evaluated before the new row is visible to other queries. If the permission you check comes from the row being inserted (its own `owner_id`, say), `check_permission` cannot see it and the insert is rejected. Check the row's columns directly, or check a permission on its parent:

```sql
-- The creator must own the new document
CREATE POLICY documents_insert ON documents FOR INSERT WITH CHECK (
    owner_id = current_setting('app.user_id', true));

-- Or: the creator must be able to edit the folder it goes in
CREATE POLICY documents_insert ON documents FOR INSERT WITH CHECK (
    check_permission('user', current_setting('app.user_id', true), 'editor', 'folder', folder_id) = 1);
```

## Transactions

Policies read the same snapshot as the statement they protect, so they see uncommitted changes made earlier in the same transaction. Granting access and using it in one transaction works as expected. Other transactions don't see the grant until it commits.

## Performance

A policy that calls `check_permission(..., id)` runs once for **every row the scan reads**, not once per row returned. Point lookups by an indexed column are fast, but anything that scans the table pays for a full permission check on every row.

PostgreSQL also evaluates policy conditions before any user-supplied filter that isn't [leakproof](https://www.postgresql.org/docs/current/ddl-rowsecurity.html), such as `LIKE`. So `WHERE body LIKE 'ab%'` does not reduce the number of checks.

The `list_accessible_objects` form runs once per query and probes a hash of the accessible IDs for each row. As a rough guide, here are the numbers on a local PostgreSQL 18 with 50,000 documents, where the user can view 100:

| SELECT policy | Full scan | `WHERE id = ...` | `WHERE body LIKE ...` |
|---|---|---|---|
| `check_permission(..., id) = 1` | 594 ms | 0.07 ms | 544 ms |
| `id IN (SELECT object_id FROM list_accessible_objects(...))` | 5 ms | 0.26 ms | 3 ms |

The tradeoff is that `list_accessible_objects` always builds the user's whole accessible set, even for a single-row lookup. Its cost grows with how much the user can access, not with the size of the table. For users who can see a large share of a big table, such as organization admins, a per-row check on point lookups can be cheaper.

Guidelines:

- Use `list_accessible_objects` for `SELECT` policies, where scans happen.
- Use `check_permission` for `UPDATE` and `DELETE` policies, which usually target rows by primary key.
- Index the tables behind `melange_tuples`, since every policy evaluation reads them. See [Scaling](../scaling/) and run `melange doctor`.
- Check your real queries with `EXPLAIN ANALYZE` while connected as the application role. Plans run as the table owner skip the policy.

## Limitations

- **No Go-side caching.** Policy checks run in the database, so the [Checker cache](../caching/) does not apply.
- **No contextual tuples.** Policies only see tuples from `melange_tuples`. Model request-scoped facts as tables or settings instead.

## See Also

- [SQL API](../../reference/sql-api/): signatures for `check_permission` and `list_accessible_objects`
- [Listing Objects](../listing-objects/): pagination and performance of list queries
- [Scaling](../scaling/): indexing the tables behind the tuples view
