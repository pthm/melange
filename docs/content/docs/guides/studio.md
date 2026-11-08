---
title: Studio
description: "melange studio: a local, read-only web console and permissions debugger for OpenFGA models in PostgreSQL. Visualize the deployed model, explain why a check was allowed or denied, and browse tuples."
weight: 12
---

`melange studio` serves a local web console for inspecting a melange-managed database: the deployed model, Explain and Expand results, a check matrix, `EXPLAIN ANALYZE` timings, the tuples behind each entity, migration history, and `melange doctor` health checks.

Studio is experimental. It reads the model from the database, so it needs only a connection: no schema file or repo checkout.

## Starting Studio

```bash
melange studio --db postgres://localhost/mydb
```

Studio prints its URL and runs until you press Ctrl-C:

```
melange studio: http://127.0.0.1:53817 (ctrl-c to stop)
```

The default address is `127.0.0.1:0`, which picks a free port on each run. Pass `--addr` for a fixed one:

```bash
melange studio --addr 127.0.0.1:7070
```

`--addr` must be a loopback address (`127.0.0.1`, `::1`, or `localhost`). Studio refuses to start on any other address.

### Choosing a Database

Studio resolves its connection like the other database commands:

| Flag          | Source when omitted                                   | Purpose                                                    |
| ------------- | ----------------------------------------------------- | ---------------------------------------------------------- |
| `--db`        | `database` settings in config, or the active `--env`  | Connection string                                          |
| `--env`       | `MELANGE_ENV`, then `default_environment` in config, else the base config | [Environment profile](../../reference/configuration/#environments) to connect to |
| `--db-schema` | `database.schema` in config, else `public`            | PostgreSQL schema holding `melange_tuples` and the generated functions |
| `--schema`    | `schema` from a config file or `MELANGE_SCHEMA`; none without either | Local `.fga` file for the Health page's file and drift checks |

```bash
# Point studio at the staging profile from melange.yaml
melange studio --env staging

# melange objects in a non-default schema
melange studio --db postgres://localhost/mydb --db-schema authz
```

An explicit `--db` wins over the environment's connection. When an environment is selected, its name is shown in the top bar, even if `--db` overrides its connection.

## Read-Only Connections

Studio opens every connection with `default_transaction_read_only=on` and checks `SHOW default_transaction_read_only` at startup. If the setting did not take effect, studio exits instead of serving. The top bar shows a `read-only` badge.

There are two exceptions, and neither commits anything. Explain with what-if tuples (see [What-If Tuples](#what-if-tuples)) begins a read-write transaction, creates a temporary view, switches the transaction to read-only, runs Explain, and always rolls back. On a database migrated with `sql.functions.explain` or `expand` off, Explain and Expand create that family's functions in `pg_temp` the same way, for each call (see [Explain and Expand Without Their Functions](#explain-and-expand-without-their-functions)).

## Pages

### Model

The Model page has two tabs: **Query** and **Deployed schema**. The deployed schema tab shows the DSL recorded by the last `melange migrate`. The sidebar lists every type and relation, with a filter; each relation links to its relation page.

{{< studio-screenshot name="schema" alt="The Model page's Deployed schema tab, showing the highlighted DSL with the type and relation sidebar" >}}

The Query form has four modes:

| Mode        | Inputs                      | Result                                                                                   |
| ----------- | --------------------------- | ---------------------------------------------------------------------------------------- |
| **Explain** | subject, relation, object   | Allowed or denied, with every path the check tried. See [Explaining Decisions](../explaining-decisions/) |
| **Expand**  | relation, object            | Who has the relation on the object, as a tree. Computed and tuple-to-userset pointers can be expanded in place. See [Expanding Permissions](../expanding-permissions/) |
| **Matrix**  | subject, object             | Every relation on the object's type, allowed or denied for the subject. Click a relation to explain it |
| **Analyze** | subject, relation, object   | Runs `check_permission` under `EXPLAIN (ANALYZE, BUFFERS)` and shows the plan, timings, and the slowest generated functions |

Subjects and objects use `type:id` (`user:alice`, `document:1`). Each result shows how long the call took.

{{< studio-screenshot name="query-explain" alt="An Explain result for user:5 can_read issue:6, allowed through the repository and organization membership" >}}

Matrix checks run in parallel, at most four list or check calls at a time across the whole server, each limited to 10 seconds.

{{< studio-screenshot name="query-matrix" alt="A Matrix result listing every repository relation as allowed or denied for user:2 on repository:2" >}}

#### What-If Tuples

In Explain mode, open **What-if tuples** and enter one `subject relation object` tuple per line:

```
user:bob viewer document:1
group:eng#member editor document:1
```

Studio runs Explain twice, without and with the tuples, and shows both answers. The tuples are added to `melange_tuples` through a temporary view, the same way [contextual tuples](../contextual-tuples/) work, inside a transaction that is always rolled back. When studio can read the deployed model, each tuple is validated against it first: an unknown relation, or a subject type the relation does not accept directly, is reported by line number.

What-if needs a writable primary and the `TEMP` privilege on the database. It fails on a hot-standby replica, because a replica refuses read-write transactions. Plain Explain and the other query modes work on a replica, except that Explain needs a writable primary on a database migrated with `sql.functions.explain` off, and Expand needs one with `sql.functions.expand` off (see [Replicas](#replicas)).

#### Analyze

Analyze runs inside a read-only transaction with a 10 second `statement_timeout`. A check that runs longer is reported as timed out.

The plan of a PL/pgSQL call is a single opaque node, so the per-function breakdown comes from `pg_stat_xact_user_functions`. That needs `track_functions`, which only a superuser can set unless it is granted:

```sql
GRANT SET ON PARAMETER track_functions TO studio_reader;  -- PostgreSQL 15+
```

Without it, Analyze still shows the plan and total timings, with a note that per-function times are unavailable.

#### Relation Pages

Each relation has a page showing its `define` line, the relations it reads (**Depends on**) and the relations that read it (**Used by**), the code generator's feature analysis, and the SQL source of its deployed functions, read from `pg_proc`: every function the deployed SQL API installs for it (check, list objects, list subjects, accessible and its parent-key form, explain, expand), and why a family is not installed. It links to the relation on the [SQL API](#sql-api) page.

{{< studio-screenshot name="relation" alt="The relation page for repository#can_read, with its definition, feature analysis and dependencies" >}}

### SQL API

The SQL API page (`/sql`) is a viewer for the functions the database installs, and how they relate to the model. It describes the **deployed model** and the exposure its last migration recorded, the same report as [`melange functions --db`](../../reference/cli/#functions), so it needs no `melange.yaml`.

- **Overview** lists what drifted: expected functions that are missing, installed with other arguments or another body, or with stale overloads; orphans the last migration recorded; functions only named like generated ones (unmanaged, never dropped); name collisions; and a note when another melange version generated the functions. The dispatchers follow, with the relations each answers for.
- **A type** (sidebar) lists its exposed relations, then its internal ones (not exposed, modelling only) folded away. Each function shows its exact name (truncated names are marked), kind, public or internal, and status; opening it shows the signature, an example call and how an application uses the result, each with a **Copy** button, any note (an accessible function is inlined into the calling query, so the schema holding `melange_tuples` must be on its `search_path`), and the functions its body may call. Families that are not installed say why.
- **A kind** (sidebar) lists that kind's functions across the model, for example every `accessible` function, which is the quickest way to find a truncated name.
- A type and a kind combine. **all kinds** and **all types** in the sidebar drop one of them.

Bodies are compared with what this melange generates for the deployed model, compiled once per model and cached, and checked through `md5(prosrc)`. The page reads only `pg_proc` and `melange_migrations`.

The [Health](#health) page answers a different question: it runs `melange doctor`, whose Generated Functions checks compare the **local schema file**. While a migration is pending, Health lists what `migrate` would add and the SQL API page what the deployed model is missing; each page links to the other.

### Tuples

The Tuples page lists every type that appears in `melange_tuples`, with the number of distinct ids. The counts take a full pass over the view and are cached for 30 seconds.

Selecting a type lists its entities, 100 at a time; scrolling to the end of the list loads the next page. Search matches a substring of the id or the entity's label.

{{< studio-screenshot name="browse" alt="The Tuples browser listing repositories by name, with their ids and tuple counts" >}}

Entities show by type and id first, with their label after it as context. Long ids and labels are cut off with an ellipsis; hover for the full text.

Selecting an entity opens three tabs:

- **Can access**: what the entity can reach as a subject, one object type at a time, through `ListObjects`. Each entity is listed with its relations underneath; more than 12 collapse behind a **+N more** button.
- **Accessed by**: who can reach the entity as an object, one subject type at a time, through `ListSubjects`, laid out the same way.
- **Direct tuples**: the raw tuples with the entity as subject or as object, as relation, type, id and label columns, with exact counts and a filter. Click a row to open the entity at its other end.

{{< studio-screenshot name="entity" alt="The Can access tab for user carol, listing her repositories with relation chips and an inline Explain" >}}

#### Access List Limits

Each access tab makes one list call per relation for the selected type. To keep large models usable:

- Each relation's list stops at 5,000 ids. When any relation hits the cap, the count gets a `+` suffix (for example `5,000+`) and the list notes that there may be more.
- A computed list is cached for 60 seconds and served 100 rows at a time as you scroll. The filter on these tabs runs over the cached list.
- At most four list calls run at once across the server, each limited to 10 seconds. A relation that times out shows an error in place of its rows.
- The count badges in the type navigation compute one at a time.

These are display limits, not limits of `ListObjects` or `ListSubjects`. Use the [Go API](../listing-objects/) or the SQL functions for complete lists.

#### Labels

Where it can, studio shows a readable name next to each id. It finds the name by planning `melange_tuples` with `EXPLAIN VERBOSE`: a view branch that maps a type to a plain column (for example `'user'` and `team_members.user_id`) is followed through primary and foreign keys to the domain table (`users.id`). The type can be a literal or a `CASE` choosing between literals (`CASE WHEN … THEN 'qa_trail' ELSE 'aa_trail' END`); every type it can produce uses that column.

The label is the table's first column, ignoring case, among `name`, `display_name`, `title`, `label`, `username`, `email` and `slug`; failing those, the first column whose name ends in `name` (such as `check_name`); failing that, `description`. Studio does not follow foreign keys to find a label in another table.

If the view does not reveal a table, studio falls back to a table named after the type, or its plural (`user` or `users`), with an `id` column, matching names regardless of case. The page header says where the labels came from, or that none were found. Discovery runs once per process; restart studio after schema changes.

Labels need `SELECT` on the domain tables. Without it, entities show by id.

### History

The History page lists the last 50 rows of `melange_migrations` in its sidebar, newest first. Each shows when it ran, its line changes (`+12 −3`), a short summary of what changed in the model (`+1 type · +4 rel · ~2 rel`), and badges for the deployed migration and for breaking changes. A migration whose schema is identical to the one before says so, and one that restores an earlier schema says which.

Selecting a migration shows it in detail:

- Counts of changed lines, types and relations, breaking changes, and installed functions.
- The model changes, by type and relation: the grants and exclusions each relation lost and gained. Breaking changes, which narrow access or remove structure, are marked; this is the same classification [`melange status`](../../reference/cli/#status) uses for drift.
- A line diff against the previous migration, file by file for a modular schema.
- The migration record: versions, schema checksum, function count, and the full schema it recorded.

The record is the same data as [`melange history`](../../reference/cli/#history). Diffs and counts need migrations recorded by a version of melange that stores the schema; older rows show what they have.

### Health

The Health page runs the same checks as [`melange doctor`](../../reference/cli/#doctor), grouped by category. Its Generated Functions checks compare the local schema file; the [SQL API](#sql-api) page compares the deployed model. The checks that compare a local schema file with the database use `--schema`, else the `schema` of a config file or `MELANGE_SCHEMA`. With none of these, studio skips the schema file and drift checks and says so; it never needs a schema file to run. With a config file, the exposure check compares its `sql:` section with the exposure the database was migrated with, as `melange doctor` does.

{{< studio-screenshot name="health" alt="The Health page showing melange doctor checks grouped by category with pass and warning counts" >}}

## Restricted SQL API Exposure

A database migrated with a restricted [exposure](../../reference/sql-api/#exposure)
(the `sql:` section of `melange.yaml`) answers for fewer relations or function
families, and its public functions raise `M2003` for the rest. Studio reads
the exposure recorded by the last migration and does not call a function for
a relation it does not answer for:

| Page | Relation not exposed | Family off |
| ---- | -------------------- | ---------- |
| Explain, Analyze | The query says the relation is not exposed and how to expose it | Explain runs on demand (see below) |
| Expand | The query says the relation is not exposed. In a tree, a pointer that `expand_permission` does not answer for has a `not exposed` label instead of a drill-in | Expand and the drill-in run on demand |
| Matrix | The relation is labeled `not exposed` and not checked; the summary counts them | Explain chips explain on demand; without a model recorded by studio's codegen version they are plain labels instead |
| Can access, Accessed by | The relation is not listed; the list names the relations left out | The tab says the list functions are not installed |
| Relation page | The header lists each function family that does not answer for the relation | Same, and explain and expand show `on demand` |

Hover a label for the reason. `expand_permission` answers for every relation
that an exposed relation's pointers lead to, so drilling into a relation that
is installed but not exposed works.

Under the default exposure, or a database migrated before melange recorded
the exposure, studio behaves as before: every relation is answered. Without a
recorded model, studio calls the functions and reports an `M2003` the same
way.

### Explain and Expand Without Their Functions

With `sql.functions.explain` or `expand` off, studio compiles that family's
functions from the recorded model and creates them in `pg_temp` for each
Explain, what-if Explain, Expand and drill-in, in a transaction that begins
read-write, turns read-only before the call, and is always rolled back, on a
connection that is closed afterwards. The
answer is the one installed functions would give, for the relations the
recorded exposure exposes. Studio compiles a model once per process; each call
still creates the functions, which costs tens of milliseconds on a mid-sized
model. On demand needs a model recorded by a melange of studio's codegen
version; without one, Explain and Expand say so and the explain chips are
plain labels. It also needs a writable primary and the `TEMP` privilege,
which studio cannot check ahead of a call: on a replica, or as a role
without `TEMP`, the explain chips stay clickable and each Explain or Expand
fails with a message asking for both. See
[Explain and Expand on Demand](../../reference/sql-api/#explain-and-expand-on-demand).

## Database Privileges

Studio only reads, so it can run as a dedicated low-privilege role. The minimum, for melange objects in the `public` schema:

```sql
CREATE ROLE studio_reader LOGIN PASSWORD '...';
GRANT USAGE ON SCHEMA public TO studio_reader;
GRANT SELECT ON public.melange_tuples, public.melange_migrations TO studio_reader;
```

This covers the Model page, Explain, Expand, Matrix, Analyze without the per-function breakdown, the Tuples browser without labels, History, and Health. It relies on PostgreSQL defaults that most databases keep:

- `EXECUTE` on the generated functions is granted to `PUBLIC` when a function is created. If you revoked it, grant `EXECUTE` on the functions in the melange schema.
- The system catalogs (`pg_proc`, `pg_class`, `pg_constraint`, `information_schema`) are readable by every role. Relation pages, labels, and Health read them.
- `TEMP` on the database is granted to `PUBLIC`. What-if needs it, and so do Explain and Expand on a database migrated without their functions.

Optional grants:

| Feature                          | Grant                                                                 |
| -------------------------------- | --------------------------------------------------------------------- |
| Labels in the Tuples browser     | `SELECT` on the domain tables (or just their key and label columns)   |
| What-if tuples, and Explain and Expand on demand | `TEMP` on the database, if revoked from `PUBLIC`        |
| Per-function times in Analyze    | `GRANT SET ON PARAMETER track_functions` (PostgreSQL 15+), or superuser |

Without `SELECT` on `melange_migrations`, studio cannot read the deployed model, so the schema view, Matrix, relation analysis, History, Health, and the access tabs are unavailable, and what-if tuples are not validated; Explain, Expand, and Analyze still work when their functions are installed (on demand needs the recorded model).

## Replicas

Studio works against a hot-standby replica, with two exceptions, both because a replica refuses read-write transactions: what-if tuples need one to create the temporary view, and on a database migrated with `sql.functions.explain` or `expand` off, Explain and Expand need one to create their functions in `pg_temp`. Point studio at the primary to use them. Everything else studio runs on the primary is read-only.

## Security

- Studio binds a loopback address only, and rejects requests whose `Host` header is not `localhost` or a loopback IP. This blocks DNS-rebinding attacks, where a hostile page resolves its own domain to `127.0.0.1`.
- There is no authentication. Anyone who can reach the loopback port on your machine can use the console with studio's database role.
- Pages are served with a strict Content Security Policy (`default-src 'self'`, no inline scripts or `eval`), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and `Cache-Control: no-store`, so authorization data is not kept in the browser cache.
- Scripts, styles, and fonts are embedded in the binary. Pages load nothing from other hosts.

### Update Check

The only network request studio makes outside your database is the CLI's usual [update check](../../reference/cli/#update-notifications), made by the CLI process, not the browser. When a newer release exists, the top bar shows a `vX.Y.Z available` badge. Disable it with `--no-update-check`; it is also off when the `CI` environment variable is set.

The top bar also links to the melange documentation and GitHub repository. Those are ordinary links and are only fetched if you click them.

## See Also

- [CLI reference](../../reference/cli/#studio): `melange studio` flags
- [Explaining Decisions](../explaining-decisions/): the trace behind the Explain mode
- [Expanding Permissions](../expanding-permissions/): the tree behind the Expand mode
- [Troubleshooting](../troubleshooting/): `melange doctor` and common errors
