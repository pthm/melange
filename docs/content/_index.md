---
title: Melange
layout: hextra-home
---

<div class="homepage-hero">

{{< hextra/hero-badge >}}
  <div class="hx:w-2 hx:h-2 hx:rounded-full hx:bg-primary-400"></div>
  <span>Free, open source</span>
  {{< icon name="arrow-circle-right" attributes="height=14" >}}
{{< /hextra/hero-badge >}}

<div class="hx:mt-6 hx:mb-6">
{{< hextra/hero-headline >}}
  Zanzibar-Style Authorization&nbsp;<br class="hx:sm:block hx:hidden" />for PostgreSQL
{{< /hextra/hero-headline >}}
</div>

<div class="hx:mb-12">
{{< hextra/hero-subtitle >}}
  Implement fine-grained, relationship-based access control without external services.&nbsp;<br class="hx:sm:block hx:hidden" />Melange compiles OpenFGA schemas into SQL functions that run inside your existing database.
{{< /hextra/hero-subtitle >}}
</div>

<div class="hx:mb-6 hx:flex hx:gap-4">
{{< hextra/hero-button text="Get Started" link="docs/getting-started/installation" >}}
{{< hextra/hero-button text="GitHub" link="https://github.com/pthm/melange" style="background: #000;" >}}
</div>

</div>

{{< hextra/feature-grid >}}
  {{< hextra/feature-card
    title="Pure PostgreSQL"
    subtitle="Zero external dependencies. No sidecars, no services. Everything runs as SQL functions inside your database."
    icon="database"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(51,103,145,0.2),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="OpenFGA Compatible"
    subtitle="Full Schema 1.1 conformance. Use familiar DSL syntax with complete feature support, validated against the official OpenFGA test suite."
    icon="badge-check"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(0,180,140,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="Optimized for Speed"
    subtitle="~130 μs permission checks in the database with O(1) constant-time scaling. Same speed whether you have 1K or 1M tuples."
    link="docs/reference/performance"
    icon="lightning-bolt"
    style="background: radial-gradient(ellipse at 50% 80%,rgba(194,97,254,0.15),hsla(0,0%,100%,0));"
  >}}
  {{< hextra/feature-card
    title="No Tuple Sync"
    subtitle="Permissions derived from views over your domain tables. Always in sync, transaction-aware, zero data duplication."
    icon="refresh"
  >}}
  {{< hextra/feature-card
    title="Transaction Aware"
    subtitle="Permission checks see uncommitted changes within the same transaction. No eventual consistency, no stale authorization data."
    icon="clock"
  >}}
  {{< hextra/feature-card
    title="Language Agnostic"
    subtitle="Go and TypeScript client libraries included, or call SQL functions directly from any language. Works in triggers, RLS policies, or application code."
    icon="puzzle"
  >}}
{{< /hextra/feature-grid >}}

<div class="homepage-release hx:mt-16">

{{< latest-release >}}

</div>

<div class="homepage-section-gap"></div>

<div class="homepage-columns">
<div>

{{< hextra/hero-section >}}
  How It Works
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

Melange is an **authorization compiler**. Like Protocol Buffers or GraphQL Code Generator, you define a schema and Melange generates optimized code tailored to your exact model. Instead of a generic runtime that interprets your model at query time, Melange generates **purpose-built SQL functions** for each relation in your schema. Role hierarchies are resolved at compile time and inlined into SQL, so runtime checks avoid recursive graph traversal entirely.

</div>
</div>
<div>

```mermaid
flowchart LR
    schema["schema.fga"] --> melange["melange migrate"]
    melange --> funcs["PostgreSQL Functions"]
    funcs --> check["check_permission()"]
    funcs --> list["list_accessible_objects()"]
    funcs --> subjects["list_accessible_subjects()"]
```

</div>
</div>

<div class="homepage-section-gap"></div>

<!-- Define Your Schema: 2-column -->
<div class="homepage-columns">
<div>

{{< hextra/hero-section >}}
  Define Your Schema
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

Write your authorization model using the OpenFGA DSL. The same `.fga` files work with both Melange and OpenFGA, so there's no vendor lock-in.

Melange supports direct assignments, computed usersets, unions, intersections, exclusions, tuple-to-userset, wildcards, userset references, and contextual tuples.

</div>
</div>
<div class="content">

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

</div>
</div>

<div class="homepage-section-gap"></div>

<!-- Query Your Existing Tables: 2-column -->
<div class="homepage-columns">
<div>

{{< hextra/hero-section >}}
  Query Your Existing Tables
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

Unlike traditional FGA systems, Melange doesn't need a separate tuple store. Create a SQL view that maps your existing domain tables into tuples, and Melange queries them directly.

No data duplication. No sync jobs. Permissions are always consistent with your domain data, down to the current transaction.

</div>
</div>
<div class="content">

```sql
CREATE VIEW melange_tuples AS
-- Organization memberships
SELECT
    'user' AS subject_type,
    user_id::text AS subject_id,
    role AS relation,
    'organization' AS object_type,
    organization_id::text AS object_id
FROM organization_members

UNION ALL

-- Repository ownership
SELECT
    'organization' AS subject_type,
    organization_id::text AS subject_id,
    'org' AS relation,
    'repository' AS object_type,
    id::text AS object_id
FROM repositories;
```

</div>
</div>

<div class="homepage-section-gap"></div>

<!-- Compile and Migrate: 2-column -->
<div class="homepage-columns">
<div>

{{< hextra/hero-section >}}
  Compile to SQL Functions
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

`melange migrate` compiles your schema into a PostgreSQL function per relation and installs them, along with `check_permission` and the list functions that dispatch to them.

Role hierarchies are flattened at compile time. `can_read` inherits from `admin`, which inherits from `owner`, so the generated function matches all three in a single lookup instead of walking the graph at query time. Run it in CI or a deploy step, or use `melange generate migration` to emit SQL files for your existing migration tool.

</div>
</div>
<div class="content">

{{< tabs >}}

{{< tab name="CLI" >}}
```bash
# Compile and install the functions
melange migrate

# Or emit up/down SQL for your migration tool
melange generate migration --output db/migrations
```
{{< /tab >}}

{{< tab name="Generated SQL" >}}
```sql
-- Excerpt of the generated function for repository.can_read
CREATE OR REPLACE FUNCTION check_repository_can_read(
    p_subject_type TEXT, p_subject_id TEXT, p_object_id TEXT, ...
) RETURNS INTEGER AS $$
BEGIN
    -- Direct grant, or implied via admin/owner
    IF EXISTS (
        SELECT 1 FROM melange_tuples
        WHERE object_type = 'repository'
          AND object_id = p_object_id
          AND relation IN ('can_read', 'admin', 'owner')
          AND subject_type = p_subject_type
          AND subject_id = p_subject_id
    ) THEN RETURN 1; END IF;

    -- Inherited via org -> member
    IF EXISTS (
        SELECT 1 FROM melange_tuples AS link
        WHERE link.object_type = 'repository'
          AND link.object_id = p_object_id
          AND link.relation = 'org'
          AND check_permission_internal(p_subject_type, p_subject_id,
                'member', link.subject_type, link.subject_id, ...) = 1
    ) THEN RETURN 1; END IF;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;
```
{{< /tab >}}

{{< /tabs >}}

</div>
</div>

<div class="homepage-section-gap"></div>

<!-- Check Permissions: 2-column -->
<div class="homepage-columns">
<div>

{{< hextra/hero-section >}}
  Check Permissions From Any Language
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

Once compiled, permission checks are simple SQL function calls. Use the Go or TypeScript client libraries for convenience, or call the generated functions directly from any language that can talk to PostgreSQL.

</div>
</div>
<div class="content">

{{< tabs >}}

{{< tab name="Go" >}}
```go
checker := melange.NewChecker(db)

allowed, err := checker.Check(ctx,
    authz.User("alice"),
    authz.RelCanRead,
    authz.Repository("123"),
)
```
{{< /tab >}}

{{< tab name="TypeScript" >}}
```typescript
const checker = new Checker(pool);

const decision = await checker.check(
  User('alice'),
  RelCanRead,
  Repository('123'),
);
```
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
SELECT check_permission(
  'user', 'alice',
  'can_read',
  'repository', '123'
);
-- Returns 1 (allowed) or 0 (denied)
```
{{< /tab >}}

{{< /tabs >}}

</div>
</div>

<div class="homepage-section-gap"></div>

<!-- Studio showcase: full width -->
<div class="homepage-studio">

{{< hextra/hero-section >}}
  Inspect Everything in Studio
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

`melange studio` opens a local, read-only console on any melange database. See what each user can access and who can access each object, explain any decision step by step, try a grant before you write it, and read the SQL melange generated for every relation. It needs only a database connection.

```bash
melange studio --env staging
```

</div>

{{< studio-screenshot name="entity" alt="Studio's tuple browser showing the repositories user carol can access, with relation chips and an inline Explain" >}}

<div class="homepage-studio-grid">

{{< studio-screenshot name="query-explain" alt="An Explain trace showing why a user can read an issue" caption="**Explain** any check, path by path" >}}

{{< studio-screenshot name="relation" alt="A relation page with its definition, analysis and dependencies" caption="**Relations** with their analysis and generated SQL" >}}

{{< studio-screenshot name="health" alt="The Health page with melange doctor checks" caption="**Health** checks from `melange doctor`" >}}

</div>

<div class="hx:mt-6">
{{< hextra/hero-button text="Explore Studio" link="docs/guides/studio" >}}
</div>

</div>

<div class="homepage-section-gap"></div>

<div class="homepage-cta">

{{< hextra/hero-section >}}
  Get Started in Minutes
{{< /hextra/hero-section >}}

<div class="hx:mt-6"></div>

<div class="content">

```bash
# Install
brew install pthm/tap/melange

# Initialize your project
melange init

# Apply schema and generate SQL functions
melange migrate

# Generate type-safe client code
melange generate client

# Inspect the database in a local web console
melange studio
```

</div>

<div class="homepage-cta-buttons">
{{< hextra/hero-button text="Read the Docs" link="docs" >}}
{{< hextra/hero-button text="View on GitHub" link="https://github.com/pthm/melange" style="background: #000;" >}}
</div>

</div>
