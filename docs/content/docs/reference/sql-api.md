---
title: SQL API
description: "Melange's generated SQL functions, such as check_permission and list_accessible_objects, callable from any PostgreSQL client."
weight: 3
---

Melange generates SQL functions that can be called directly from any PostgreSQL client. This allows you to use Melange's authorization system from any language or framework without requiring the Go library.

## Overview

When you run `melange migrate`, Melange generates:

| Function | Purpose |
|----------|---------|
| `check_permission` | Check if a subject has a relation on an object |
| `check_permission_bulk` | Check multiple permissions in a single call |
| `list_accessible_objects` | List all objects a subject can access (with pagination) |
| `list_accessible_subjects` | List all subjects with access to an object (with pagination) |
| `accessible_objects` | The objects a subject can access, as a set to filter your own queries by |
| `accessible_{type}_{relation}` | The same set for one relation, inlined into your query: faster for large sets |

These are the primary entry points. Internally, Melange generates specialized per-relation functions (e.g., `check_document_viewer`) that the dispatchers route to.

To see the exact functions a database has, with their signatures and an example call for each, run [`melange functions --db`](../cli/#functions) (or `melange functions show <type#relation>` for one relation) or open studio's [SQL API page](../../guides/studio/#sql-api). Long names are truncated with a hash, so look them up there rather than building them.

## Exposure

Which of these functions exist, and which relations they answer for, depends
on the SQL API exposure: the [`sql:` section](../configuration/#sql-api-settings)
of `melange.yaml` and the exposure flags of `melange migrate`. The default
exposes every relation and generates every function family, which is the API
this page describes. A restricted exposure names the relations an application
queries (`relations.include` / `relations.exclude`) and the families it uses
(`functions.*`).

| Family | Public functions | Per-relation functions |
|--------|------------------|------------------------|
| check (always on) | `check_permission`, `check_permission_nw`, `check_permission_bulk` | `check_{type}_{relation}` |
| `list_objects` | `list_accessible_objects` | `list_{type}_{relation}_obj` |
| `list_subjects` | `list_accessible_subjects` | `list_{type}_{relation}_sub` |
| `accessible` | `accessible_objects` | `accessible_{type}_{relation}` |
| `parent_key` | | `accessible_{type}_{relation}_by_{link}` |
| `expand` | `expand_permission` | `expand_{type}_{relation}` |
| `explain` | `explain_permission` | `explain_{type}_{relation}` |

**What is installed.** A relation usually resolves through others:
`document#can_view` may read `document#viewer`, which reads `folder#viewer`
through a parent. Migrate installs the functions of the exposed relations and
of every relation they reach this way, directly or through other relations, so
an exposed relation always resolves completely. A tuple-to-userset inside an
intersection or exclusion, such as `[user] and viewer from parent`, reads
`viewer` on the parent's types only, so it installs nothing for a `viewer`
relation on the object's own type. Check functions are installed for the
exposed relations and every relation they reach. The `list_*_obj` and
`accessible_*` functions are installed for all of them when any of
`list_objects`, `accessible` or `parent_key` is on, since the three read each
other; `list_*_sub`, `expand_*` and `explain_*` when their family is on. The
`_by_{link}` parent-key functions are installed for exposed relations only.
Everything outside that set is not generated, and migrate drops it if an
earlier exposure installed it. The one addition is the
`check_permission_unexposed` helper described below.

**Public and internal dispatchers.** The public dispatchers on this page
answer only for exposed relations of an enabled family. A generated function
checks another relation by calling its `check_{type}_{relation}` function
directly when the type and relation are known when the SQL is generated. It
goes through the internal dispatchers (`check_permission_internal`,
`check_permission_nw_internal`, `list_accessible_subjects_internal`,
`explain_permission_internal`, `expand_permission_internal`) otherwise, for
example for a tuple-to-userset whose tupleset allows several types. Both reach
every installed function, so a relation an exposed one reaches resolves even
though it is not exposed. `expand_permission` answers for every relation it
installs, because clients follow the computed and tuple-to-userset pointers in
an expand tree through it.

**Unexposed and unknown relations.** A public dispatcher raises
[`M2003`](#error-code-m2003) for a relation the model defines on that type but
the exposure does not expose, including every relation of a disabled family
(`list_accessible_subjects` with `list_subjects: false`). A type and relation
the model does not define keep the [unknown-pair](#unknown-typerelation)
result: `0`, an empty set, or the sentinel. With the default exposure nothing
raises, so the API behaves as it did before exposure existed.

A restricted exposure also installs `check_permission_unexposed(p_object_type TEXT, p_relation TEXT)`, the PL/pgSQL helper that the `LANGUAGE sql` public dispatchers call to raise `M2003`. It is not generated under the default exposure, and migrate drops it when the exposure returns to the default.

### Explain and Expand on Demand

Explain and expand are debugging tools, so an application can migrate with
`explain: false` and `expand: false` and keep their per-relation functions out
of the database. The dispatchers stay: `explain_permission` and
`expand_permission` then raise `M2003` for every relation the model defines.

`melange explain`, `melange expand` (including `--flatten` and `--recursive`)
and [studio](../../guides/studio/) still work against such a database. They
compile the family's functions from the model recorded by the last migration,
exactly as migrate installs them with the family on, and create them in
`pg_temp` for one transaction:

```sql
BEGIN READ WRITE;
CREATE FUNCTION pg_temp.check_permission(...) ...;         -- forwards to the installed one
CREATE FUNCTION pg_temp.explain_document_viewer(...) ...;  -- and the rest of the family
SET TRANSACTION READ ONLY;
SELECT pg_temp.explain_permission(...);                     -- every call of the command
ROLLBACK;
```

Only that family's functions are created, plus a `pg_temp.check_permission`
that forwards to the installed one, because the Go runtime's `Checker` probes
for `check_permission` in the schema it calls. They call the installed check
functions and `check_permission_internal`, which every exposure installs, and
answer for the relations the recorded exposure exposes: a relation that is not
exposed stays not exposed. Nothing is committed. The transaction runs on a
connection of its own, which is closed afterwards instead of returned to the
pool: a backend keeps the compiled PL/pgSQL of every function it has called
until it exits, even once the function is dropped. On demand needs:

- **A writable primary.** `CREATE FUNCTION` is refused in a read-only
  transaction, even in `pg_temp`, and a hot-standby replica refuses read-write
  transactions.
- **The `TEMP` privilege** on the database, which PostgreSQL grants to `PUBLIC`
  by default.
- **A recorded model.** `melange migrate` records it (and so does an applied
  `melange migrate --dry-run` script). Databases deployed from
  `melange generate migration` files have no record, so the tools cannot tell
  the family is off and report the `M2003`.
- **The same codegen version.** The compiled bodies call functions the
  recorded melange version generated, so a melange of another version refuses
  and says which: run `melange migrate` with it, or use the recorded version.

Each call pays for a new connection and for creating the functions. On the kitchen-sink test model (60
explain or expand functions), one Explain took about 28 ms on demand against
0.25 ms installed, and one Expand about 12 ms against 0.14 ms, on a local
PostgreSQL 18 (`BenchmarkKitchenSinkOnDemand`); compiling the functions takes 3 ms for explain and 0.7 ms
for expand and is done once per process. The cost grows with the number of the
family's functions, not with the data. The Go and TypeScript runtimes do not
compile on demand: `Checker.Explain` and `Checker.Expand` report the `M2003`.

{{< callout type="info" >}}
The exposure shapes the API surface; it is not an access-control boundary. The
per-relation functions and the internal dispatchers of an installed relation
stay callable, exposed or not. Use database privileges to restrict who may
call them.
{{< /callout >}}

## check_permission

Checks whether a subject has a specific relation on an object.

### Signature

```sql
check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_type` | TEXT | Type of the subject (e.g., `'user'`, `'team'`) |
| `p_subject_id` | TEXT | ID of the subject |
| `p_relation` | TEXT | Relation to check (e.g., `'viewer'`, `'can_read'`) |
| `p_object_type` | TEXT | Type of the object (e.g., `'document'`, `'repository'`) |
| `p_object_id` | TEXT | ID of the object |

### Return Value

- `1` - Access granted
- `0` - Access denied

### Examples

```sql
-- Check if user 123 can view document 456
SELECT check_permission('user', '123', 'viewer', 'document', '456');

-- Use in a WHERE clause to filter accessible records
SELECT d.*
FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;

-- Use with CASE for conditional logic
SELECT
    d.id,
    d.title,
    CASE WHEN check_permission('user', '123', 'editor', 'document', d.id::text) = 1
         THEN true ELSE false END AS can_edit
FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;
```

## check_permission_bulk

Checks multiple permissions in a single SQL call. Each position across the input arrays forms one check request, and results are returned as a table.

### Signature

```sql
check_permission_bulk(
    p_subject_types TEXT[],
    p_subject_ids TEXT[],
    p_relations TEXT[],
    p_object_types TEXT[],
    p_object_ids TEXT[]
) RETURNS TABLE(idx INTEGER, allowed INTEGER)
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_types` | TEXT[] | Array of subject types (e.g., `ARRAY['user', 'user']`) |
| `p_subject_ids` | TEXT[] | Array of subject IDs |
| `p_relations` | TEXT[] | Array of relations to check |
| `p_object_types` | TEXT[] | Array of object types |
| `p_object_ids` | TEXT[] | Array of object IDs |

{{< callout type="warning" >}}
All five arrays **must** be the same length. Each position across the arrays forms one check request. For example, position 1 checks whether `p_subject_types[1]:p_subject_ids[1]` has `p_relations[1]` on `p_object_types[1]:p_object_ids[1]`.
{{< /callout >}}

### Return Value

Returns a table with two columns:
- `idx` - 1-based position corresponding to the input arrays
- `allowed` - `1` for access granted, `0` for access denied

One row is returned per input check. The rows are not in index order: they are grouped by object type and relation. Match each row to its request by `idx`, and add `ORDER BY idx` when the order matters.

### Examples

```sql
-- Check two permissions in a single call
SELECT idx, allowed
FROM check_permission_bulk(
    ARRAY['user', 'user'],
    ARRAY['123', '123'],
    ARRAY['viewer', 'editor'],
    ARRAY['document', 'document'],
    ARRAY['456', '789']
);
-- Returns:
-- idx | allowed
-- ----+---------
--   1 |       1
--   2 |       0

-- Mixed object types and relations
SELECT idx, allowed
FROM check_permission_bulk(
    ARRAY['user', 'user', 'team'],
    ARRAY['123', '123', '456'],
    ARRAY['viewer', 'can_read', 'member'],
    ARRAY['document', 'repository', 'organization'],
    ARRAY['doc-1', 'repo-1', 'org-1']
);

-- Use results in a query: idx is the position in the input arrays
WITH docs AS (
    SELECT array_agg(id::text ORDER BY id) AS ids FROM documents
)
SELECT d.id, d.title, b.allowed
FROM docs
CROSS JOIN LATERAL check_permission_bulk(
    array_fill('user'::text, ARRAY[COALESCE(cardinality(docs.ids), 0)]),
    array_fill('123'::text, ARRAY[COALESCE(cardinality(docs.ids), 0)]),
    array_fill('viewer'::text, ARRAY[COALESCE(cardinality(docs.ids), 0)]),
    array_fill('document'::text, ARRAY[COALESCE(cardinality(docs.ids), 0)]),
    COALESCE(docs.ids, ARRAY[]::text[])
) b
JOIN documents d ON d.id::text = docs.ids[b.idx]
ORDER BY d.id;
```

## list_accessible_objects

Returns all object IDs that a subject has a specific relation on, with cursor-based pagination support.

### Signature

```sql
list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(object_id TEXT, next_cursor TEXT)
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_type` | TEXT | Type of the subject |
| `p_subject_id` | TEXT | ID of the subject |
| `p_relation` | TEXT | Relation to check |
| `p_object_type` | TEXT | Type of objects to list |
| `p_limit` | INT | Maximum results per page (NULL = no limit) |
| `p_after` | TEXT | Cursor from previous page (NULL = first page) |

### Return Value

Returns a table with two columns:
- `object_id` - The ID of an accessible object
- `next_cursor` - Cursor for the next page (NULL when no more pages)

The `next_cursor` value is repeated on every row for client convenience. Use the last row's cursor value to fetch the next page.

{{< callout type="info" >}}
**Ordering**: Results are ordered deterministically by `object_id` to ensure stable pagination.
{{< /callout >}}

### Examples

```sql
-- Get first 100 documents user 123 can view
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);

-- Get next page using cursor
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, 'doc-100');

-- Get all accessible objects (no pagination)
SELECT object_id
FROM list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL);

-- Join with domain table to get full records
SELECT d.*
FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL) a
    ON d.id::text = a.object_id;

-- Count accessible objects
SELECT COUNT(*)
FROM list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL);
```

## list_accessible_subjects

Returns all subjects that have a specific relation on an object, with cursor-based pagination support.

### Signature

```sql
list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT,
    p_limit INT DEFAULT NULL,
    p_after TEXT DEFAULT NULL
) RETURNS TABLE(subject_id TEXT, next_cursor TEXT)
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_object_type` | TEXT | Type of the object |
| `p_object_id` | TEXT | ID of the object |
| `p_relation` | TEXT | Relation to check |
| `p_subject_type` | TEXT | Type of subjects to list |
| `p_limit` | INT | Maximum results per page (NULL = no limit) |
| `p_after` | TEXT | Cursor from previous page (NULL = first page) |

### Return Value

Returns a table with two columns:
- `subject_id` - The ID of a subject with access
- `next_cursor` - Cursor for the next page (NULL when no more pages)

{{< callout type="info" >}}
**Ordering**: Results are ordered with wildcard subjects (`'*'`) first, then alphabetically by `subject_id`. This ensures stable pagination while keeping wildcard entries grouped at the top.
{{< /callout >}}

### Examples

```sql
-- Get first 100 users who can view document 456
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'user', 100, NULL);

-- Get next page using cursor
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'user', 100, 'user-100');

-- Get all subjects with access (no pagination)
SELECT subject_id
FROM list_accessible_subjects('document', '456', 'viewer', 'user', NULL, NULL);

-- Join with users table to get full user records
SELECT u.*
FROM users u
JOIN list_accessible_subjects('document', '456', 'viewer', 'user', NULL, NULL) a
    ON u.id::text = a.subject_id;

-- Get all team members with access (userset filter)
-- Note: Use 'team#member' as subject_type to filter by userset
SELECT subject_id, next_cursor
FROM list_accessible_subjects('document', '456', 'viewer', 'team#member', NULL, NULL);
```

## accessible_objects

Returns the IDs of the objects of `p_object_type` on which the subject holds `p_relation`, for filtering your own queries. It takes the relation and type by name, like `list_accessible_objects`, and works for every relation that has one.

### Signature

```sql
accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT)
```

```sql
SELECT d.*
FROM documents d
WHERE d.id::text IN (SELECT * FROM accessible_objects('user', '123', 'viewer', 'document'))
  AND d.archived = false
ORDER BY d.created_at DESC
LIMIT 50;
```

It reads the whole set through `list_accessible_objects` and then joins it, so it plans in constant time whatever the size of your model. That is as fast as `accessible_{type}_{relation}` when the subject reaches up to a few thousand objects, and slower beyond that: for a viewer of a million subjects, a page took 1.3 s through `accessible_objects` and 0.34 s through the per-relation function.

## accessible_{type}_{relation}

The same set as `accessible_objects` for one relation, for example `accessible_document_viewer`. Each listable relation gets one.

### Signature

```sql
accessible_{type}_{relation}(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_userset BOOLEAN DEFAULT NULL
) RETURNS TABLE(object_id TEXT)
```

Call it with the subject only. `p_userset` says whether the subject is a userset (`team:1#member`); the generated list functions pass it so their cached plans leave out what cannot match, and without it the function reads it from the subject. A call whose value contradicts the subject returns no rows.

It is a `LANGUAGE sql` function, so PostgreSQL inlines it into the calling query. Your filters, ordering and limit are planned together with the access set instead of around a function call, which is what makes it faster than `accessible_objects` for large sets. Use it in `IN`, not as a list to page through:

```sql
SELECT d.*
FROM documents d
WHERE d.id::text IN (SELECT * FROM accessible_document_viewer('user', '123'))
  AND d.archived = false
ORDER BY d.created_at DESC
LIMIT 50;
```

The inlined query reads `melange_tuples` through your query's `search_path`, so the schema holding it must be on the path. Rows can repeat; `IN` does not need them distinct. Long type and relation names are shortened with a hash to fit PostgreSQL's 63-character limit; `accessible_objects` needs no names, and [generated clients](#names-in-generated-clients) and [`melange functions`](../cli/#functions) carry the exact ones.

Relations listed by a breadth-first walk are the exception. These are a relation that recurses on itself through a parent (`viewer: [user] or viewer from parent`) and a userset on its own relation (`member: [user, group#member]` on `group`). For them `accessible_{type}_{relation}` reads the whole list from `list_{type}_{relation}_obj`, a PL/pgSQL function, so your filters and limit apply after the walk. A relation past the depth limit raises `M2002` through either function.

### accessible_{type}_{relation}_by_{link}

A relation whose every grant comes through the same link to a single parent type, such as `can_read: reader from org` or `can_read: reader from org or admin from org`, also gets a parent-key form. It returns the IDs of the parents through which the subject holds the relation, so you filter on the column that holds the link:

```sql
SELECT s.*
FROM subjects s
WHERE s.organization_id IN (SELECT * FROM accessible_subject_can_read_by_org('user', '123'))
ORDER BY s.created_at DESC
LIMIT 50;
```

This reads a handful of parent IDs instead of the access set of every object under them. The function is PL/pgSQL rather than inlined, so it is planned once per session, and the planner expects a few rows from it (`ROWS 10`), so a filter on it reads the parents' rows through an index on the link column. Its IDs are distinct.

To page by another column, take the top of each parent and merge them, so an index on `(organization_id, created_at)` reads at most a page per parent:

```sql
SELECT s.*
FROM accessible_subject_can_read_by_org('user', '123') AS p(organization_id),
     LATERAL (
         SELECT * FROM subjects t
         WHERE t.organization_id = p.organization_id
         ORDER BY t.created_at DESC
         LIMIT 50
     ) s
ORDER BY s.created_at DESC
LIMIT 50;
```

For a viewer of an organization holding a million subjects, a count took about 4 s filtering by `accessible_subject_can_read` and 70 ms filtering by `accessible_subject_can_read_by_org`. A page of 50 by creation time took 93 ms with `IN` and a sort, and 0.9 ms with the query above.

It is exact when each object has a single link, held in that column (a many-to-one foreign key). It does not cover a subject that is a userset on the object type itself (`subject:s#can_read`). A relation that also grants directly, through a userset or a wildcard, or with an exclusion or intersection, has no parent-key form.

### Names in generated clients

Do not build these names by hand: a name longer than 63 bytes is truncated and hashed, so `pull_request#can_read_security_advisories_and_vulnerability_reports` is `accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c`. [`melange generate client`](../generated-code/#accessible-function-names) emits the name of every accessible function migrate installs for an exposed relation: Go constants such as `SQLAccessibleDocumentViewer` and `SQLAccessibleSubjectCanReadByOrg` with the `SQLAccessibleFunction(type, relation)` lookup, and the TypeScript maps `AccessibleFunctions[type][relation]` and `AccessibleByParentFunctions[type][relation][link]`. It follows the `sql:` section, so a function the exposure installs only because an exposed relation resolves through it is not named. The names are unqualified: qualify them with the [database schema](../configuration/#custom-database-schema) when it is not on the `search_path`.

A name is stable while the type, relation and link keep their names. The client stops naming `accessible_{type}_{relation}` when the relation is no longer listable or exposed, or `accessible` is turned off, and a parent-key form when the relation is no longer exposed, `parent_key` is turned off, or its grants change shape. The function itself can outlive its name: as [What is installed](#exposure) describes, `accessible_{type}_{relation}` stays installed while an exposed relation resolves through the relation and any of `list_objects`, `accessible` or `parent_key` is on, so migrate drops it only when the relation is renamed, stops being listable, is no longer reached from an exposed relation, or all three families are off. Do not rely on a function the client no longer names: it is kept only for what resolves through it, and a later migration can drop it.

Calling a named function from a view or an RLS policy is fine: `melange migrate` refuses to drop a function that one depends on and lists it (see [Dependent objects](../../guides/migrations/#dependent-objects)). Regenerate the client when the model or the `sql:` section changes, so a name that went away fails to compile in your code.

## explain_permission

Returns a JSONB resolution trace for a check: every attempted branch, contributing tuples, per-branch success/failure. The companion to `check_permission` for debugging and admin tooling — not the request path, since it builds a JSONB document per call.

### Signature

```sql
explain_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_max_nodes INTEGER DEFAULT NULL
) RETURNS JSONB
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_subject_type` | TEXT | Subject type (e.g., `'user'`) |
| `p_subject_id` | TEXT | Subject identifier |
| `p_relation` | TEXT | Relation to check |
| `p_object_type` | TEXT | Object type (e.g., `'document'`) |
| `p_object_id` | TEXT | Object identifier |
| `p_max_nodes` | INTEGER | Optional cap on trace nodes. `NULL` defers to session GUC `melange.max_explain_nodes`, then to the built-in default (100). |

### Return Value

A JSONB document matching the runtime's `Trace` type. Always returns a valid trace; for an unknown `(object_type, relation)` pair the root is a sentinel rather than NULL.

```jsonc
{
  "object": "document:1",
  "relation": "viewer",
  "subject": "user:alice",
  "result": true,
  "root": {
    "type": "userset",
    "label": "via [group#member] → group:engineering",
    "result": true,
    "children": [
      {
        "type": "direct",
        "label": "direct grant",
        "result": true,
        "evidence": [
          {"subject_type": "user", "subject_id": "alice",
           "relation": "member", "object_type": "group", "object_id": "engineering"}
        ]
      }
    ]
  },
  "node_count": 2
}
```

`result` is `true` on allow, `false` on deny. `truncated` is omitted when `false`; it's `true` when `p_max_nodes` capped the trace.

### Examples

```sql
-- Pretty-print the trace
SELECT jsonb_pretty(explain_permission('user', 'alice', 'viewer', 'document', '1'));

-- Per-call cap
SELECT explain_permission('user', 'alice', 'can_read', 'repository', '42', 50);

-- Per-session cap; subsequent calls inherit it
SET melange.max_explain_nodes = 200;
SELECT explain_permission('user', 'bob', 'can_admin', 'repository', '7');
```

### Use from runtime / CLI

`Checker.Explain` and `melange explain` wrap this function. See the [Explaining Decisions guide](../../guides/explaining-decisions/). With `sql.functions.explain` off, `melange explain` and studio create it in `pg_temp` for each call; see [Explain and Expand on Demand](#explain-and-expand-on-demand).

## explain_permission_internal

Internal dispatcher behind `explain_permission`. Same signature plus a `p_visited TEXT[]` cycle-detection array. Specialized functions (`explain_<type>_<rel>`) call it recursively; most callers should use `explain_permission` instead.

```sql
explain_permission_internal(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT[] DEFAULT ARRAY[]::TEXT[],
    p_max_nodes INTEGER DEFAULT NULL
) RETURNS JSONB
```

## expand_permission

Returns a JSONB `UsersetTree` for a `(object, relation)` pair, matching `openfgav1.UsersetTree` field-for-field. Resolution is shallow: computed rewrites surface as `Leaf.Computed` pointers, TTU rewrites as `Leaf.TupleToUserset` pointers. The companion to `list_accessible_subjects` for admin / audit tooling that needs the structural tree rather than a flat list.

### Signature

```sql
expand_permission(
    p_object_type  TEXT,
    p_object_id    TEXT,
    p_relation     TEXT,
    p_subject_type TEXT    DEFAULT NULL,
    p_max_leaf     INTEGER DEFAULT NULL
) RETURNS JSONB
```

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `p_object_type` | TEXT | Object type (e.g., `'document'`) |
| `p_object_id` | TEXT | Object identifier |
| `p_relation` | TEXT | Relation to expand |
| `p_subject_type` | TEXT | Melange extension. When set, `Leaf.Users` entries are filtered to this subject type. `NULL` matches all types. |
| `p_max_leaf` | INTEGER | Melange extension. Caps each `Leaf.Users` list. `NULL` means no cap (matching OpenFGA). When the cap fires the leaf's `users_truncated` field is `true`. |

### Return Value

A JSONB document matching the runtime's `UsersetTree` type. Always returns a valid tree; for an unknown `(object_type, relation)` pair the root's `Leaf.Users` is empty rather than NULL.

```jsonc
{
  "root": {
    "name": "document:1#viewer",
    "union": {
      "nodes": [
        {
          "name": "document:1#viewer",
          "leaf": {
            "users": {
              "users": ["user:alice", "user:bob", "group:eng#member"]
            }
          }
        },
        {
          "name": "document:1#viewer",
          "leaf": {
            "computed": {"userset": "document:1#editor"}
          }
        }
      ]
    }
  }
}
```

Wildcards (`user:*`) and userset references (`group:eng#member`) survive as inline strings in `Leaf.Users`. Callers chase `Leaf.Computed` and `Leaf.TupleToUserset` pointers with follow-up `expand_permission` calls, or use `Checker.ExpandRecursive` from the runtime for the walker.

### Examples

```sql
-- Full tree
SELECT jsonb_pretty(expand_permission('document', '1', 'viewer'));

-- Filter Leaf.Users to a single subject type
SELECT expand_permission('document', '1', 'viewer', 'user');

-- Per-call cap; users_truncated fires on overflowing leaves
SELECT expand_permission('document', '1', 'viewer', NULL, 100);

```

### Use from runtime / CLI

`Checker.Expand`, `Checker.ExpandRecursive`, and `melange expand` wrap this function. See the [Expanding Permissions guide](../../guides/expanding-permissions/). With `sql.functions.expand` off, `melange expand` and studio create it in `pg_temp` for each call; see [Explain and Expand on Demand](#explain-and-expand-on-demand).

## expand_permission_internal

Internal dispatcher behind `expand_permission`. Same signature. Specialized functions (`expand_<type>_<rel>`) call it recursively to resolve pointer chains inside `ExpandRecursive`; most callers should use `expand_permission` instead.

```sql
expand_permission_internal(
    p_object_type  TEXT,
    p_object_id    TEXT,
    p_relation     TEXT,
    p_subject_type TEXT    DEFAULT NULL,
    p_max_leaf     INTEGER DEFAULT NULL
) RETURNS JSONB
```

## Pagination

Both list functions support cursor-based (keyset) pagination for efficient traversal of large result sets.

### How Pagination Works

1. **First page**: Call with `p_limit` set and `p_after = NULL`
2. **Check for more pages**: If any row has `next_cursor` non-NULL, there are more pages
3. **Next page**: Call with `p_after` set to the `next_cursor` value from the previous page
4. **Last page**: When `next_cursor` is NULL, you've reached the end

### Example: Paginating Through All Results

```sql
-- First page
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);
-- Returns 100 rows, next_cursor = 'doc-100'

-- Second page
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, 'doc-100');
-- Returns 100 rows, next_cursor = 'doc-200'

-- Continue until next_cursor is NULL
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, 'doc-900');
-- Returns 50 rows, next_cursor = NULL (no more pages)
```

### Transaction Consistency

Paginated queries across multiple calls can observe changes between pages. For consistency-critical flows, run paging inside a transaction:

```sql
BEGIN ISOLATION LEVEL REPEATABLE READ;

SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);
-- Use cursor for next pages within the same transaction

COMMIT;
```

## Error Handling

### Error Code: M2002

The functions raise an exception with error code `M2002` when resolving a request takes more steps than OpenFGA allows. As in OpenFGA, a step is a parent relationship (tuple-to-userset) or a userset hop; computed relations and intersection or exclusion operands are not steps. The limits match OpenFGA's:

- `check_permission` resolves a chain of 25 steps and raises at 26
- the list functions resolve 24 and raise at 25, rather than return a partial list. A grant reached through a userset or a parent lookup into another type at the end of a parent chain counts as one more step, so 23 parent hops plus that step list fine and 24 raise.

OpenFGA's own Check limit depends on which resolver it picks for the model. Its fast path for a relation that recurses on itself (`[user] or viewer from parent`) resolves 25 steps, as melange does. When the relation also grants through usersets or other relations (`[user, group#member] or viewer from parent`), OpenFGA's general resolver raises one step earlier, and melange's check does not follow it there. Deeply nested groups at the end of a parent chain are also counted as a single step by the list functions, where OpenFGA counts each level.

```sql
RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
```

This can occur with deeply nested parent relationships or deeply nested userset chains. A cycle in the data does not raise on its own: a check stops at an object it has already visited, and the list functions expand each object once. A check that takes more than 25 steps before it gets back to a visited object still raises `M2002`, because the depth limit is checked before the cycle.

Handle this error in your application:

```sql
DO $$
BEGIN
    PERFORM check_permission('user', '123', 'viewer', 'document', '456');
EXCEPTION
    WHEN SQLSTATE 'M2002' THEN
        RAISE NOTICE 'Permission resolution too complex';
END;
$$;
```

### Error Code: M2003

A public dispatcher raises `M2003` when the requested relation is defined by
the model on that type but is not exposed by the
[SQL API exposure](#exposure) the database was migrated with, or its function
family is disabled:

```
ERROR:  relation "viewer" on type "document" is not exposed
```

`check_permission_bulk` raises for the whole call when any row names an
unexposed relation, as `check_permission` would for that row. The error never
occurs under the default exposure. Fix it by adding the relation (or family)
to the `sql:` section and migrating, or by querying an exposed relation.

### Unknown Type/Relation

When an object type or relation the model does not define is queried, the functions return:
- `check_permission`: `0` (denied)
- `list_accessible_objects`: Empty result set
- `list_accessible_subjects`: Empty result set

No error is raised for unknown types/relations. A relation the model defines
but the exposure leaves out raises [`M2003`](#error-code-m2003) instead.

## Usage from Different Languages

### Python (psycopg2)

```python
import psycopg2

conn = psycopg2.connect("postgresql://localhost/mydb")
cur = conn.cursor()

# Check permission
cur.execute(
    "SELECT check_permission(%s, %s, %s, %s, %s)",
    ('user', '123', 'viewer', 'document', '456')
)
allowed = cur.fetchone()[0] == 1

# List accessible objects (first page)
cur.execute(
    "SELECT object_id, next_cursor FROM list_accessible_objects(%s, %s, %s, %s, %s, %s)",
    ('user', '123', 'viewer', 'document', 100, None)
)
rows = cur.fetchall()
accessible_ids = [row[0] for row in rows]
next_cursor = rows[-1][1] if rows else None

# List all accessible objects (no pagination)
cur.execute(
    "SELECT object_id FROM list_accessible_objects(%s, %s, %s, %s, %s, %s)",
    ('user', '123', 'viewer', 'document', None, None)
)
all_accessible_ids = [row[0] for row in cur.fetchall()]
```

### Node.js (pg)

```javascript
const { Pool } = require('pg');
const pool = new Pool({ connectionString: 'postgresql://localhost/mydb' });

// Check permission
const { rows } = await pool.query(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', '123', 'viewer', 'document', '456']
);
const allowed = rows[0].check_permission === 1;

// List accessible objects (first page)
const result = await pool.query(
  'SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', '123', 'viewer', 'document', 100, null]
);
const accessibleIds = result.rows.map(row => row.object_id);
const nextCursor = result.rows.length > 0 ? result.rows[result.rows.length - 1].next_cursor : null;

// List all accessible objects (no pagination)
const allResult = await pool.query(
  'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', '123', 'viewer', 'document', null, null]
);
const allAccessibleIds = allResult.rows.map(row => row.object_id);
```

### Ruby (pg gem)

```ruby
require 'pg'

conn = PG.connect(dbname: 'mydb')

# Check permission
result = conn.exec_params(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', '123', 'viewer', 'document', '456']
)
allowed = result[0]['check_permission'] == '1'

# List accessible objects (first page)
result = conn.exec_params(
  'SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', '123', 'viewer', 'document', 100, nil]
)
accessible_ids = result.map { |row| row['object_id'] }
next_cursor = result.ntuples > 0 ? result[result.ntuples - 1]['next_cursor'] : nil

# List all accessible objects (no pagination)
result = conn.exec_params(
  'SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)',
  ['user', '123', 'viewer', 'document', nil, nil]
)
all_accessible_ids = result.map { |row| row['object_id'] }
```

### Java (JDBC)

```java
import java.sql.*;
import java.util.ArrayList;
import java.util.List;

Connection conn = DriverManager.getConnection("jdbc:postgresql://localhost/mydb");

// Check permission
PreparedStatement ps = conn.prepareStatement(
    "SELECT check_permission(?, ?, ?, ?, ?)"
);
ps.setString(1, "user");
ps.setString(2, "123");
ps.setString(3, "viewer");
ps.setString(4, "document");
ps.setString(5, "456");

ResultSet rs = ps.executeQuery();
rs.next();
boolean allowed = rs.getInt(1) == 1;

// List accessible objects (first page)
ps = conn.prepareStatement(
    "SELECT object_id, next_cursor FROM list_accessible_objects(?, ?, ?, ?, ?, ?)"
);
ps.setString(1, "user");
ps.setString(2, "123");
ps.setString(3, "viewer");
ps.setString(4, "document");
ps.setInt(5, 100);
ps.setNull(6, Types.VARCHAR);

rs = ps.executeQuery();
List<String> accessibleIds = new ArrayList<>();
String nextCursor = null;
while (rs.next()) {
    accessibleIds.add(rs.getString("object_id"));
    nextCursor = rs.getString("next_cursor");
}

// List all accessible objects (no pagination)
ps = conn.prepareStatement(
    "SELECT object_id FROM list_accessible_objects(?, ?, ?, ?, ?, ?)"
);
ps.setString(1, "user");
ps.setString(2, "123");
ps.setString(3, "viewer");
ps.setString(4, "document");
ps.setNull(5, Types.INTEGER);
ps.setNull(6, Types.VARCHAR);

rs = ps.executeQuery();
List<String> allAccessibleIds = new ArrayList<>();
while (rs.next()) {
    allAccessibleIds.add(rs.getString("object_id"));
}
```

### Rust (tokio-postgres)

```rust
use tokio_postgres::NoTls;

let (client, connection) = tokio_postgres::connect(
    "postgresql://localhost/mydb", NoTls
).await?;

// Check permission
let row = client.query_one(
    "SELECT check_permission($1, $2, $3, $4, $5)",
    &[&"user", &"123", &"viewer", &"document", &"456"]
).await?;
let allowed: i32 = row.get(0);
let has_access = allowed == 1;

// List accessible objects (first page)
let rows = client.query(
    "SELECT object_id, next_cursor FROM list_accessible_objects($1, $2, $3, $4, $5, $6)",
    &[&"user", &"123", &"viewer", &"document", &Some(100i32), &None::<String>]
).await?;
let accessible_ids: Vec<String> = rows.iter()
    .map(|row| row.get("object_id"))
    .collect();
let next_cursor: Option<String> = rows.last()
    .and_then(|row| row.get("next_cursor"));

// List all accessible objects (no pagination)
let rows = client.query(
    "SELECT object_id FROM list_accessible_objects($1, $2, $3, $4, $5, $6)",
    &[&"user", &"123", &"viewer", &"document", &None::<i32>, &None::<String>]
).await?;
let all_accessible_ids: Vec<String> = rows.iter()
    .map(|row| row.get("object_id"))
    .collect();
```

## Specialized Functions

In addition to the dispatcher functions, Melange generates specialized functions for each type/relation pair the [exposure](#exposure) installs. These are called internally by the dispatchers but can also be called directly if you know the exact type and relation:

| Pattern | Function Name |
|---------|---------------|
| Check | `check_{type}_{relation}(subject_type, subject_id, object_id, visited)` |
| List objects | `list_{type}_{relation}_obj(subject_type, subject_id, p_limit, p_after)` |
| List subjects | `list_{type}_{relation}_sub(object_id, subject_type, p_limit, p_after)` |

For example, for a `document` type with a `viewer` relation:

```sql
-- Direct specialized function call (slightly faster, bypasses dispatcher)
SELECT check_document_viewer('user', '123', '456', ARRAY[]::TEXT[]);

-- List objects using specialized function (with pagination)
SELECT object_id, next_cursor
FROM list_document_viewer_obj('user', '123', 100, NULL);

-- List subjects using specialized function (with pagination)
SELECT subject_id, next_cursor
FROM list_document_viewer_sub('456', 'user', 100, NULL);
```

{{< callout type="info" >}}
**Note**: Specialized functions include a `p_visited` parameter used for cycle detection in recursive patterns. When calling directly, pass `ARRAY[]::TEXT[]` for this parameter.
{{< /callout >}}

Every `check_{type}_{relation}` function and `_nw` variant is declared `COST 1000`, the same cost as `check_permission_internal`, so the planner places a direct call in a `WHERE` clause or RLS policy where it would place a dispatcher call: after cheaper conditions. Each one also raises [`M2002`](#error-code-m2002) once `p_visited` holds 26 entries, as the dispatcher does.

## Performance Considerations

### Use List Functions for Batch Operations

Instead of calling `check_permission` for each object:

```sql
-- Inefficient: N function calls
SELECT d.* FROM documents d
WHERE check_permission('user', '123', 'viewer', 'document', d.id::text) = 1;
```

Use list functions with a JOIN:

```sql
-- Efficient: 1 function call + JOIN
SELECT d.* FROM documents d
JOIN list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL) a
    ON d.id::text = a.object_id;
```

### Use Pagination for Large Result Sets

For potentially large result sets, use pagination to avoid loading unbounded data:

```sql
-- Good: Controlled page size
SELECT object_id, next_cursor
FROM list_accessible_objects('user', '123', 'viewer', 'document', 100, NULL);

-- Use with caution: Returns ALL results
SELECT object_id
FROM list_accessible_objects('user', '123', 'viewer', 'document', NULL, NULL);
```

### Transaction Consistency

All functions are marked `STABLE`, meaning they see a consistent snapshot of the database within a transaction. Permission checks within the same transaction will see uncommitted changes to the `melange_tuples` view.

```sql
BEGIN;
-- Insert new tuple (via domain table that feeds the view)
INSERT INTO team_members (user_id, team_id, role) VALUES ('123', '456', 'member');

-- Permission check sees the uncommitted row
SELECT check_permission('user', '123', 'member', 'team', '456');
-- Returns 1

ROLLBACK;
-- Now returns 0
```

### Index Recommendations

See [Tuples View](../../concepts/tuples-view/#performance-optimization) for indexing strategies that optimize the SQL functions.
