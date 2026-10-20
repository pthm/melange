---
title: Quick Start
description: "The shortest path from an empty project to a working permission check in PostgreSQL with Melange."
weight: 2
prev: /docs/getting-started/installation
next: /docs/getting-started/project-setup
---

This is the shortest path to a working permission check. It uses the default settings; see [Project Setup](../project-setup) for customization.

## 1. Initialize Your Project

```bash
melange init -y --db postgres://localhost:5432/mydb
```

The `--db` URL is saved to `melange/config.yaml` and used by every later command. Without it, `init -y` writes the default `postgres://localhost:5432/mydb`. You can also set `MELANGE_DATABASE_URL` or pass `--db` to `melange migrate`.

This creates a `melange/` directory with a config file and starter schema:

```
myproject/
├── melange/
│   ├── config.yaml
│   └── schema.fga
└── ...
```

The default schema defines organizations with role hierarchies and repositories with inherited permissions:

```fga
model
  schema 1.1

type user

type organization
  relations
    define owner: [user]
    define admin: [user] or owner
    define member: [user] or admin

type repository
  relations
    define org: [organization]
    define owner: [user]
    define admin: [user] or owner
    define can_read: member from org or admin
    define can_write: admin
    define can_delete: owner
```

{{< callout type="info" >}}
See [Project Setup](../project-setup) for details on starter templates, migration strategies, and customization options.
{{< /callout >}}

## 2. Create the melange_tuples View

Melange reads authorization data from a SQL view called `melange_tuples`. This view maps your existing tables into tuples.

The view below expects `organization_members`, `repositories`, and `repository_owners` tables. If you are trying Melange on an empty database, create them with some sample rows first:

```sql
CREATE TABLE organization_members (user_id text, organization_id text, role text);
CREATE TABLE repositories (id text, organization_id text);
CREATE TABLE repository_owners (user_id text, repository_id text);

INSERT INTO organization_members VALUES ('alice', 'acme', 'member');
INSERT INTO repositories VALUES ('42', 'acme');
INSERT INTO repository_owners VALUES ('bob', '42');
```

Then create the view:

```sql
CREATE OR REPLACE VIEW melange_tuples AS
-- Organization memberships
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,                -- 'owner', 'admin', or 'member'
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Repository -> Organization relationship
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories

UNION ALL

-- Direct repository owners
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    'owner' AS relation,
    'repository' AS object_type,
    repository_id::text AS object_id
FROM repository_owners;
```

{{< callout type="info" >}}
This view queries your existing tables directly, so there is no separate tuple store to keep in sync. See [Creating Your Tuples View](../tuples-view) for a detailed walkthrough.
{{< /callout >}}

## 3. Apply Migrations

```bash
melange migrate
```

This compiles your schema into PostgreSQL functions and installs them.

To preview the generated SQL without applying:

```bash
melange migrate --dry-run
```

## 4. Check Permissions

The migration installed a `check_permission` function. Try it in SQL:

```sql
-- Returns 1 (allowed) or 0 (denied)
SELECT check_permission('user', 'alice', 'can_read', 'repository', '42');
```

Or from application code:

{{< tabs >}}

{{< tab name="Go" >}}
```go
import (
    "context"
    "database/sql"

    _ "github.com/lib/pq" // any database/sql Postgres driver works
    "github.com/pthm/melange/melange"
)

db, _ := sql.Open("postgres", "postgres://localhost/mydb")

checker := melange.NewChecker(db)
user := melange.Object{Type: "user", ID: "alice"}
repo := melange.Object{Type: "repository", ID: "42"}

allowed, err := checker.Check(context.Background(), user, "can_read", repo)
```

Install the runtime library (stdlib-only, no external dependencies):

```bash
go get github.com/pthm/melange/melange
```
{{< /tab >}}

{{< tab name="TypeScript" >}}
```typescript
import { Pool } from 'pg';

const pool = new Pool({ connectionString: 'postgresql://localhost/mydb' });

const { rows } = await pool.query(
  'SELECT check_permission($1, $2, $3, $4, $5)',
  ['user', 'alice', 'can_read', 'repository', '42']
);
const allowed = rows[0].check_permission === 1;
```
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
-- Direct check
SELECT check_permission('user', 'alice', 'can_read', 'repository', '42');

-- Filter query results
SELECT r.*
FROM repositories r
WHERE check_permission('user', 'alice', 'can_read', 'repository', r.id::text) = 1;
```
{{< /tab >}}

{{< /tabs >}}

## What `melange migrate` Did

1. Parsed your OpenFGA schema
2. Analyzed each relation to detect patterns (direct assignment, role hierarchy, parent inheritance)
3. Generated a SQL function for each type+relation pair (e.g., `check_repository_can_read`)
4. Installed a dispatcher function (`check_permission`) that routes to the correct per-relation function

Permission checks are SQL function calls. There is no external service, no network round-trip, and no tuple synchronization.

## Next Steps

- [Creating Your Tuples View](../tuples-view): mapping your domain tables in detail
- [Your First Migration](../first-migration): validation, health checks, and external migration frameworks
- [Project Setup](../project-setup): templates, migration strategies, and code generation options
- [Checking Permissions](../../guides/checking-permissions/): Checker API, caching, bulk checks, and transactions
- [How It Works](../../concepts/how-it-works/): the compiler model and generated SQL
