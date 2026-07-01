---
title: Expanding Permissions
weight: 5
---

`Expand` returns the OpenFGA-shaped `UsersetTree` for a `(object, relation)` pair — the structured "who has access?" answer. Use it to surface an audit UI, generate an access-review export, or debug why a specific subject ended up in the result of `ListSubjects`.

Expand does more work per call than `Check` or `ListSubjects` and returns a JSONB tree. Don't put it on a request-path hot loop; use `Check` there.

## Basic Usage

{{< tabs >}}

{{< tab name="Go" >}}
```go
import "github.com/pthm/melange/melange"

checker := melange.NewChecker(db)

tree, err := checker.Expand(ctx,
    melange.Object{Type: "document", ID: "1"},
    melange.Relation("viewer"),
)
if err != nil {
    return err
}

for _, u := range tree.FlattenUsers() {
    fmt.Println(u)
}
```

`Expand` takes an object and a relation. The returned `*UsersetTree` mirrors `openfgav1.UsersetTree` field-for-field so existing OpenFGA tooling deserialises the JSON without an adapter. `FlattenUsers` collects every `Leaf.Users` entry across the returned tree without issuing additional queries.
{{< /tab >}}

{{< tab name="CLI" >}}
```bash
$ melange expand document:1 viewer --db postgres://localhost/mydb
document:1#viewer • union of 2
├── document:1#viewer • users
│   ├── user:alice
│   ├── user:bob
│   └── group:eng#member
└── document:1#viewer • computed pointer
    └── computed → document:1#editor  (melange expand document:1#editor to chase)
```

`--format=json` returns the raw `UsersetTree` JSONB. `--flatten` calls `ExpandRecursive` and prints the flat, deduplicated user list.
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
SELECT jsonb_pretty(expand_permission('document', '1', 'viewer'));
```

Returns JSONB matching the runtime's `UsersetTree` type. Signature in the [SQL API reference](../../reference/sql-api/#expand_permission).
{{< /tab >}}

{{< /tabs >}}

## Shallow by Default

`Expand` resolves one level. Computed rewrites (`define viewer: editor`) surface as `Leaf.Computed` pointers; TTU rewrites (`viewer from parent`) surface as `Leaf.TupleToUserset` pointers. The caller chases pointers with follow-up Expand calls:

```go
if node.Leaf != nil && node.Leaf.Computed != nil {
    obj, rel := parsePointer(node.Leaf.Computed.Userset) // "document:1#editor"
    subtree, _ := checker.Expand(ctx, obj, rel)
    // ... recurse into subtree
}
```

This matches OpenFGA's Expand behaviour: consumers walk the tree at their own pace instead of the server materialising every layer up front.

For flows that want a flat user list, use `ExpandRecursive`:

{{< tabs >}}

{{< tab name="Go" >}}
```go
users, err := checker.ExpandRecursive(ctx,
    melange.Object{Type: "document", ID: "1"},
    melange.Relation("viewer"),
)
// users = ["user:alice", "user:bob", "user:carol", "group:eng#member", "user:*"]
```

`ExpandRecursive` walks `Leaf.Computed` and `Leaf.TupleToUserset` pointers with additional Expand calls until the graph is exhausted. Cycle-safe: every `(object, relation)` pair is expanded at most once per call. Wildcards and userset references survive as their string forms; the walker does not chase userset refs because OpenFGA models them as inline subjects, not pointers.

Cost is N round-trips for N distinct pointers. Suitable for admin flows, not the request path.
{{< /tab >}}

{{< tab name="CLI" >}}
```bash
$ melange expand document:1 viewer --recursive
user:alice
user:bob
user:carol
group:eng#member
user:*
```
{{< /tab >}}

{{< /tabs >}}

## Tree Structure

A `UsersetTree` carries a single `Root *UsersetTreeNode`. Every node has a `Name` (`"<type>:<id>#<relation>"`) and exactly one of `Leaf` / `Union` / `Intersection` / `Difference` populated:

| Slot | Meaning |
|------|---------|
| `Leaf.Users` | Resolved direct grants: `["user:alice", "group:eng#member", "user:*"]` |
| `Leaf.Computed` | Unresolved pointer to another userset (chased via follow-up Expand) |
| `Leaf.TupleToUserset` | Unresolved TTU pointer: tupleset naming the linking relation + one Computed per linked object |
| `Union` | OR-aggregated children — the relation has multiple rewrites |
| `Intersection` | AND-aggregated children — `a and b` |
| `Difference` | Named `base` / `subtract` slots — `a but not b` |

Full Go type signatures: [Go API reference](../reference/go-api/#expand).

### Direct Grants + Wildcards + Userset References

All three shapes live in `Leaf.Users` as OpenFGA-formatted strings:

```json
{
  "leaf": {
    "users": {
      "users": ["user:alice", "user:bob", "group:eng#member", "user:*"]
    }
  }
}
```

Wildcards (`user:*`) mean "every subject of that type" — the tree never enumerates them. Userset references (`group:eng#member`) mean "every member of `group:eng`" — chase with a follow-up Expand call on `("group:eng", "member")` if you need the concrete users. `FlattenUsers` returns userset refs and wildcards as-is; the consumer decides whether to resolve them further.

### Intersection and Difference

Intersection nodes list every AND-part as a child; a subject is in the result only if it's in every child's leaf set. Difference nodes carry named `Base` and `Subtract` slots; a subject is in the result if it's in `Base` but not in `Subtract`. Both shapes preserve structure so consumers can render an audit tree, not just a flat list.

## Melange Extensions

Two options extend OpenFGA's Expand without breaking the wire shape.

### Subject-Type Filter

Narrow `Leaf.Users` to a single subject type:

{{< tabs >}}

{{< tab name="Go" >}}
```go
tree, err := checker.Expand(ctx, doc, "viewer",
    melange.WithSubjectTypeFilter("user"))
```
{{< /tab >}}

{{< tab name="CLI" >}}
```bash
$ melange expand document:1 viewer --subject-type=user
```
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
SELECT expand_permission('document', '1', 'viewer', 'user');
```
{{< /tab >}}

{{< /tabs >}}

Grants for other subject types are still visible in the tree structure (Computed pointers, TTU pointers, etc.) but concrete `Leaf.Users` lists are filtered.

### Per-Leaf Cap

Cap each `Leaf.Users` list. When the cap fires, the leaf gets a `UsersTruncated: true` field so consumers know the list is incomplete.

Three-tier precedence, per-call > session GUC > default:

{{< tabs >}}

{{< tab name="Go" >}}
```go
tree, err := checker.Expand(ctx, doc, "viewer",
    melange.WithExpandMaxLeaf(100))
```
{{< /tab >}}

{{< tab name="CLI" >}}
```bash
$ melange expand document:1 viewer --max-leaf 100
```
{{< /tab >}}

{{< tab name="SQL" >}}
```sql
-- Per-call
SELECT expand_permission('document', '1', 'viewer', NULL, 100);

-- Per-session
SET melange.max_expand_leaf = 500;
SELECT expand_permission('document', '1', 'viewer');
```
{{< /tab >}}

{{< /tabs >}}

Default is unbounded (matching OpenFGA). Set the GUC as a safety belt on connections that serve admin flows.

## Wildcards

A `[user:*]` grant survives as a `"user:*"` entry in `Leaf.Users`:

```json
{"leaf": {"users": {"users": ["user:*", "user:alice"]}}}
```

Consumers treat any `"<type>:*"` suffix as "every subject of that type". Wildcards are never enumerated to the implied user set.

## Supported Schema Patterns

Expand matches `Check` across the full supported schema feature set:

- Direct grants (`[user]`, `[user:*]`, `[group#member]`)
- Computed rewrites (`define viewer: editor`) as `Leaf.Computed` pointers
- TTU rewrites (`viewer from parent`) as `Leaf.TupleToUserset` pointers
- Union, intersection (any part shape), and difference (chained, TTU-excluded, intersection-excluded)
- Wildcards and userset references inlined in `Leaf.Users`

Every `(object_type, relation)` pair in the OpenFGA compatibility suite generates a specialised `expand_*` function. The dispatcher returns an empty `Leaf.Users` sentinel for pairs that don't exist in the schema; a sentinel response means the requested pair was not migrated.

## Caching

Opt-in via `ExpandCache` — see [Caching](./caching/#expandcache).

## See Also

- [Explaining Decisions](./explaining-decisions/): the companion `Explain` API for "why (or why not)?"
- [Listing Subjects](./listing-subjects/): request-path listing without the tree structure
- [Caching](./caching/): opt-in caching for Expand trees via `ExpandCache`
- [Go API reference](../reference/go-api/#expand): `Checker.Expand` / `ExpandRecursive` signatures
- [SQL API reference](../reference/sql-api/#expand_permission): `expand_permission` SQL function
- [CLI reference](../reference/cli/#expand): `melange expand` command
