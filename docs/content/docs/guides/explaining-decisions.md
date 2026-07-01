---
title: Explaining Decisions
weight: 4
---

`Explain` returns the resolution tree behind a permission decision: every branch the engine walked, the matching tuples, and per-branch success or failure. Use it to debug a check or surface an audit path in an admin UI.

Explain does more work per call than `Check` and returns a JSONB trace. Don't put it on a request-path hot loop; use `Check` there.

## Basic Usage

{{< tabs >}}

{{< tab name="Go" >}}
```go
import "github.com/pthm/melange/melange"

checker := melange.NewChecker(db)

trace, err := checker.Explain(ctx,
    melange.Object{Type: "user", ID: "alice"},
    melange.Relation("viewer"),
    melange.Object{Type: "document", ID: "1"},
)
if err != nil {
    return err
}

if trace.Result != nil && *trace.Result {
    fmt.Println("allowed via", trace.Root.Type)
} else {
    fmt.Println("denied; tried", len(trace.Root.Children), "branches")
}
```

The signature matches `Check` (subject, relation, object) plus variadic `...ExplainOption`. The returned `*Trace` contains the boolean result, the resolution tree, and optional truncation metadata.
{{< /tab >}}

{{< tab name="CLI" >}}
{{< explaintree >}}
$ melange explain user:alice viewer document:1 --db postgres://localhost/mydb
✓ user:alice has viewer on document:1
└── via userset: via [group#member] → group:engineering
    └── direct: user:alice → member → group:engineering
{{< /explaintree >}}

Subject and object use `<type>:<id>` form. The default tree format is for terminals; use `--format=json` for the raw `Trace` JSONB.
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
-- Inspect a successful permission
SELECT jsonb_pretty(explain_permission('user', 'alice', 'viewer', 'document', '1'));
```

Returns JSONB matching the runtime's `Trace` type. Signature in the [SQL API reference](../../reference/sql-api/#explain_permission).
{{< /tab >}}

{{< /tabs >}}

## Reading a Failure Trace

On denial, the trace records every attempted branch with `Result: false` on each missed leaf, showing which grants are missing.

{{< tabs >}}

{{< tab name="CLI" >}}
{{< explaintree >}}
$ melange explain user:bob viewer document:1
✗ user:bob does NOT have viewer on document:1
└── union of 3 branches
    ├── ✗ no direct grant
    ├── ✗ implied: implied via editor
    │   └── union of 1 branches
    │       └── ✗ no direct grant
    └── ✗ via userset: via [group#member] → group:engineering
        └── union of 1 branches
            └── ✗ no direct grant
{{< /explaintree >}}

The root is a `union` of three attempts: direct grant (no tuple), implied via `editor` (no editor tuple), userset via `[group#member]` (bob isn't in the group). Adding any of the three missing tuples grants `viewer`.
{{< /tab >}}

{{< tab name="Go" >}}
```go
trace, _ := checker.Explain(ctx, bob, "viewer", doc)
if trace.Result != nil && !*trace.Result {
    for _, attempt := range trace.Root.Children {
        switch attempt.Type {
        case melange.NodeDirect:
            // No direct tuple. Add (user:bob, viewer, document:1).
        case melange.NodeImplied:
            // Closure relation also missed.
        case melange.NodeUserset:
            // Userset grant existed but bob's membership didn't.
        case melange.NodeTTU:
            // Linking tuple was there but the parent didn't grant.
        }
    }
}
```

Each branch's `Children` holds the recursive sub-trace, so callers can walk the structure to render an audit path or surface a specific failure cause.
{{< /tab >}}

{{< /tabs >}}

## The `Trace` Structure

A `Trace` carries the boolean `Result` plus a `*Node` root whose `Children` recurse. Each `Node` has a discriminator `Type`, a human-readable `Label`, contributing tuples in `Evidence`, and a per-branch `Result`. JSON field names are snake_case to match the SQL columns. Full type signatures: [Go API reference](../../reference/go-api/#explain). TypeScript type mirrors: `clients/typescript/src/trace.ts`.

### Node Types

| `Type`         | Meaning                                                                                                                |
| -------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `direct`       | Satisfied by a direct tuple in `melange_tuples`. `Evidence` carries the matching row.                                  |
| `implied`      | Satisfied by a closure relation. `Children[0]` carries the underlying relation's trace.                                |
| `userset`      | Satisfied via a `[type#relation]` subject reference. `Children[0]` carries the membership relation's trace.            |
| `ttu`          | Satisfied via `X from Y`. `Children[0]` carries the parent trace; the label inlines the resolved parent identifier.   |
| `union`        | OR aggregation. Appears as the failure root when every attempt missed; each child is a recorded attempt.               |
| `intersection` | AND aggregation. `Children` carries every part; `Result=true` only when all children succeeded.                        |
| `exclusion`    | `but not X`. `Children[0]` is the base success; the node's `Result` reflects whether the exclusion fired.              |
| `wildcard`     | `[type:*]` sentinel. Not enumerated. `Users[0]` carries the matched subject type; `id` is always `"*"`.                |
| `cycle`        | Recursive resolution hit a cycle. `Label` carries the visited key that triggered detection.                            |
| `truncated`    | `p_max_nodes` budget was exhausted. The subtree below this point was omitted from the trace.                            |

## Wildcards

A `[type:*]` grant matches as a `NodeWildcard` sentinel; the user list isn't enumerated. `Users[0].Type` holds the matched subject type; `id` is always `"*"`.

```json
{
  "type": "wildcard",
  "users": [{"type": "user", "id": "*"}],
  "result": true
}
```

The trace records a wildcard match rather than a specific subject id, making clear when a grant came from a `[type:*]` pattern.

## Truncation

Three controls cap the node count, in priority order:

1. Per-call: `WithExplainMaxNodes(n)` in Go, `--max-nodes n` in the CLI, the `p_max_nodes` parameter in SQL.
2. Per-session: `SET melange.max_explain_nodes = N;`. Inherited by subsequent Explain calls unless overridden per-call.
3. Default: `100`.

{{< tabs >}}

{{< tab name="Go" >}}
```go
trace, err := checker.Explain(ctx, user, "viewer", doc,
    melange.WithExplainMaxNodes(50))
if trace.Truncated {
    log.Warn("trace was capped; retry with a larger budget for the full path")
}
```
{{< /tab >}}

{{< tab name="CLI" >}}
```bash
$ melange explain user:alice viewer document:1 --max-nodes 50
```
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
-- Per-call
SELECT explain_permission('user', 'alice', 'viewer', 'document', '1', 50);

-- Per-session
SET melange.max_explain_nodes = 200;
SELECT explain_permission('user', 'alice', 'viewer', 'document', '1');
```
{{< /tab >}}

{{< /tabs >}}

When the cap is hit, `trace.Truncated` is `true`. The root may be `NodeTruncated` or a normal union, depending on where the overshoot landed. Check `Truncated` directly rather than inferring from `Root.Type`.

## Supported Schema Patterns

Explain matches `Check` across the full supported schema feature set:

- Direct grants (`[user]`, `[user:*]`)
- Implied closure relations (`viewer: [user] or editor`), including recursive implication
- TTU (`viewer from parent`), single and multi-level, single- and multi-type linking
- Userset references (`[group#member]`), simple and complex (recursive-membership)
- Intersection (`a and b`) with any part shape: plain relation, `[user]` inline, TTU-in-intersection, per-part exclusion
- Exclusion (`a but not b`), including chained (`(a but not b) but not c`), TTU (`but not X from Y`), and intersection-group (`but not (A and B)`) subtrahends
- Wildcards and per-call / per-session truncation

Every `(object_type, relation)` pair in the OpenFGA compatibility suite generates a specialised `explain_*` function. The dispatcher's no-entry sentinel remains in the codebase as a runtime guard for pairs that don't exist in the schema:

```jsonc
{
  "result": false,
  "root": {
    "type": "union",
    "label": "explain not yet supported for this (object_type, relation) — ..."
  }
}
```

A sentinel response means the requested pair has no generated function. Check what you passed against the migrated schema before treating it as a bug.

## See Also

- [Expanding Permissions](./expanding-permissions/): the companion `Expand` API for "who has access?"
- [Checking Permissions](./checking-permissions/): the request-path `Check` API
- [Caching](./caching/): opt-in caching for Explain traces via `ExplainCache`
- [Troubleshooting](./troubleshooting/): when checks return the wrong answer
- [Go API reference](../reference/go-api/#explain): `Checker.Explain` signature
- [SQL API reference](../reference/sql-api/#explain_permission): `explain_permission` SQL function
- [CLI reference](../reference/cli/#explain): `melange explain` command
