/**
 * Trace types for the Melange Expand API.
 *
 * Mirrors the Go types in `melange/expand.go`, which in turn mirror
 * openfgav1.UsersetTree field-for-field so existing OpenFGA tooling
 * (UI builders, audit exporters, SDK consumers) deserialises Melange's
 * Expand response without an adapter.
 *
 * Field names match the JSONB wire format (snake_case for
 * `users_truncated`, `tuple_to_userset`, etc.) so `JSON.parse` produces
 * the right shape with no remapping. The camelCase preference of
 * TypeScript consumers can be applied at the call site if desired.
 */

/**
 * UsersetTree is the root of an Expand response. The shape matches
 * openfgav1.UsersetTree exactly.
 */
export interface UsersetTree {
  readonly root: UsersetTreeNode | null;
}

/**
 * UsersetTreeNode mirrors openfgav1.UsersetTree_Node. Exactly one of
 * `leaf` / `difference` / `union` / `intersection` is populated on any
 * given node — the others are undefined. `name` carries the canonical
 * OpenFGA identifier in `<type>:<id>#<relation>` form so consumers can
 * correlate nodes with the schema's rewrite structure.
 */
export interface UsersetTreeNode {
  readonly name: string;
  readonly leaf?: Leaf;
  readonly difference?: Difference;
  readonly union?: Nodes;
  readonly intersection?: Nodes;
}

/**
 * Leaf mirrors openfgav1.UsersetTree_Leaf. Exactly one of `users` /
 * `computed` / `tuple_to_userset` is populated.
 *
 * - `users` carries the resolved direct grants for this leaf.
 * - `computed` is an unresolved pointer to another (object, relation)
 *   userset — the caller chases it with a follow-up Expand call.
 * - `tuple_to_userset` is an unresolved TTU pointer.
 */
export interface Leaf {
  readonly users?: Users;
  readonly computed?: Computed;
  readonly tuple_to_userset?: TupleToUserset;
}

/**
 * Users carries the resolved direct grants for a leaf. Entries are
 * OpenFGA-formatted strings: concrete users (`user:alice`), inlined
 * userset references (`group:eng#member`), and wildcards (`user:*`).
 * Wildcards are never enumerated — consumers treat a `<type>:*` entry
 * as "every subject of that type".
 *
 * `users_truncated` is a Melange extension. True when the per-leaf
 * cap (`p_max_leaf` / `WithExpandMaxLeaf`) was set and the user list
 * was capped. OpenFGA consumers ignore the field (omitempty in the
 * wire format); Melange clients surface it as a warning.
 */
export interface Users {
  readonly users: string[];
  readonly users_truncated?: boolean;
}

/**
 * Computed is an unresolved pointer to another (object, relation)
 * userset, emitted for computed-userset rewrites
 * (e.g. `define viewer: editor` emits `{userset: "<obj>:#editor"}`).
 * Caller chases it by issuing a follow-up Expand call against the
 * named pair.
 */
export interface Computed {
  readonly userset: string;
}

/**
 * TupleToUserset is the unresolved pointer for a tuple-to-userset (TTU)
 * rewrite (`define can_read: can_read from parent`). `tupleset` names
 * the linking relation; `computed` names the relation to expand against
 * each linked object.
 */
export interface TupleToUserset {
  readonly tupleset: string;
  readonly computed: Computed[];
}

/**
 * Difference mirrors openfgav1.UsersetTree_Difference. Named base /
 * subtract slots match OpenFGA's proto exactly (positional children
 * would be ambiguous when the two are inspected by name).
 */
export interface Difference {
  readonly base: UsersetTreeNode;
  readonly subtract: UsersetTreeNode;
}

/**
 * Nodes is the shared envelope for Union and Intersection — the two
 * cases have identical wire shape. The discriminator is which field is
 * populated on the parent UsersetTreeNode (`union` vs `intersection`).
 */
export interface Nodes {
  readonly nodes: UsersetTreeNode[];
}

/**
 * ExpandOptions controls a single Checker.expand call. Both options
 * are Melange extensions on top of OpenFGA's Expand surface — OpenFGA
 * itself has neither a per-leaf cap nor a subject-type filter.
 */
export interface ExpandOptions {
  /**
   * Narrow every Leaf.Users array to the given subject type. Concrete
   * users of other types and userset references rooted in other types
   * are dropped. The tree structure is unaffected.
   */
  subjectType?: string;

  /**
   * Cap on entries per Leaf.Users array. When the cap fires the
   * affected Users carries `users_truncated: true`. Pass <= 0 / omit
   * to mean "unset" (unbounded, OpenFGA-equivalent).
   */
  maxLeaf?: number;
}

/**
 * flattenUsers walks the tree and returns every Leaf.Users entry as a
 * deduplicated, sorted array of OpenFGA-formatted strings (concrete
 * users, inlined userset references, wildcards). Computed and
 * TupleToUserset pointers are NOT chased — for that use
 * Checker.expandRecursive (which issues follow-up Expand calls per
 * pointer).
 *
 * Order is deterministic so tests and consumers comparing flattened
 * results don't depend on melange_tuples row order.
 */
export function flattenUsers(tree: UsersetTree | null | undefined): string[] {
  if (!tree || !tree.root) {
    return [];
  }
  const set = new Set<string>();
  collectLeafUsers(tree.root, set);
  return Array.from(set).sort();
}

// collectLeafUsers walks the tree and inserts every Leaf.Users entry
// into the set. Exclusion (Difference) is walked into the Base only —
// the Subtract side names users to exclude, not include. Matches the
// behaviour of the Go FlattenUsers.
function collectLeafUsers(node: UsersetTreeNode | null | undefined, set: Set<string>): void {
  if (!node) {
    return;
  }
  if (node.leaf?.users) {
    for (const u of node.leaf.users.users) {
      set.add(u);
    }
  }
  if (node.union) {
    for (const child of node.union.nodes) {
      collectLeafUsers(child, set);
    }
  }
  if (node.intersection) {
    for (const child of node.intersection.nodes) {
      collectLeafUsers(child, set);
    }
  }
  if (node.difference) {
    collectLeafUsers(node.difference.base, set);
  }
}
