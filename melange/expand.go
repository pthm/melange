package melange

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// UsersetTree is the root of an Expand response. The shape mirrors
// openfgav1.UsersetTree field-for-field so existing OpenFGA tooling
// (UI builders, audit exporters, SDK consumers) deserialises the JSON
// without an adapter layer.
//
// Resolution is shallow by default: computed rewrites and tuple-to-userset
// (TTU) rewrites surface as unresolved pointers (Leaf.Computed /
// Leaf.TupleToUserset). Callers chase pointers by issuing follow-up
// Expand calls — or use Checker.ExpandRecursive for the convenience walker.
//
// See FlattenUsers for the in-memory accessor that collects every
// Leaf.Users entry across the tree without issuing additional queries.
type UsersetTree struct {
	Root *UsersetTreeNode `json:"root"`
}

// UsersetTreeNode mirrors openfgav1.UsersetTree_Node. Exactly one of
// Leaf / Difference / Union / Intersection is populated on any given node;
// the others are nil. Name carries the canonical OpenFGA identifier in
// "<type>:<id>#<relation>" form so consumers can correlate nodes with
// the schema's rewrite structure.
type UsersetTreeNode struct {
	Name         string      `json:"name"`
	Leaf         *Leaf       `json:"leaf,omitempty"`
	Difference   *Difference `json:"difference,omitempty"`
	Union        *Nodes      `json:"union,omitempty"`
	Intersection *Nodes      `json:"intersection,omitempty"`
}

// Leaf mirrors openfgav1.UsersetTree_Leaf. Exactly one of Users /
// Computed / TupleToUserset is populated; the others are nil.
//   - Users carries resolved direct grants.
//   - Computed is an unresolved pointer to another (object, relation)
//     userset — the caller chases it with a follow-up Expand call.
//   - TupleToUserset is an unresolved TTU pointer.
type Leaf struct {
	Users          *Users          `json:"users,omitempty"`
	Computed       *Computed       `json:"computed,omitempty"`
	TupleToUserset *TupleToUserset `json:"tuple_to_userset,omitempty"`
}

// Users carries the resolved direct grants for a leaf. Entries are
// OpenFGA-formatted strings: concrete users ("user:alice"), inlined
// userset references ("group:eng#member"), and wildcards ("user:*").
// Wildcards are never enumerated to the implied user set; consumers
// treat a "<type>:*" entry as "every subject of that type".
//
// UsersTruncated is a Melange extension. True when the per-leaf cap
// (p_max_leaf / WithExpandMaxLeaf) was set and the user list was
// capped. OpenFGA consumers can ignore the field (it serialises as
// omitempty); Melange clients surface it as a warning.
type Users struct {
	Users          []string `json:"users"`
	UsersTruncated bool     `json:"users_truncated,omitempty"`
}

// Computed is an unresolved pointer to another (object, relation)
// userset emitted by computed-userset rewrites (e.g. `define viewer:
// editor` emits Computed{Userset: "<obj>:#editor"}). Caller chases it
// by issuing a follow-up Expand call against the named pair.
type Computed struct {
	Userset string `json:"userset"`
}

// TupleToUserset is the unresolved pointer for a tuple-to-userset (TTU)
// rewrite (`define can_read: can_read from parent`). Tupleset names the
// linking relation; Computed names the relation to expand against each
// linked object.
type TupleToUserset struct {
	Tupleset string     `json:"tupleset"`
	Computed []Computed `json:"computed"`
}

// Difference mirrors openfgav1.UsersetTree_Difference. Carries named
// base / subtract slots (the OpenFGA convention) rather than positional
// children so consumers can address the two halves unambiguously.
type Difference struct {
	Base     *UsersetTreeNode `json:"base"`
	Subtract *UsersetTreeNode `json:"subtract"`
}

// Nodes is the shared envelope for Union and Intersection. The two cases
// have identical wire shape; the discriminator is which Node field is
// populated on the parent UsersetTreeNode.
type Nodes struct {
	Nodes []*UsersetTreeNode `json:"nodes"`
}

// FlattenUsers walks the tree and returns every Leaf.Users entry as a
// deduplicated, sorted slice. Includes concrete users, inlined userset
// references, and wildcards — the OpenFGA-formatted strings exactly as
// they appear in the tree. Computed and TupleToUserset pointers are NOT
// chased; for that use Checker.ExpandRecursive (which issues follow-up
// Expand calls per pointer).
//
// Order is deterministic (sorted) so tests and consumers comparing
// flattened results don't depend on melange_tuples row order.
func (t *UsersetTree) FlattenUsers() []string {
	if t == nil || t.Root == nil {
		return nil
	}
	set := make(map[string]struct{})
	collectLeafUsers(t.Root, set)
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// collectLeafUsers walks the tree and inserts every Leaf.Users entry
// into set. Exclusion (Difference) is walked into the Base only — the
// Subtract side names users to exclude, not to include. This matches
// the spirit of "users with access" for the Difference case; consumers
// who want the raw subtract set should inspect the tree directly.
func collectLeafUsers(n *UsersetTreeNode, set map[string]struct{}) {
	if n == nil {
		return
	}
	if n.Leaf != nil && n.Leaf.Users != nil {
		for _, u := range n.Leaf.Users.Users {
			set[u] = struct{}{}
		}
	}
	if n.Union != nil {
		for _, child := range n.Union.Nodes {
			collectLeafUsers(child, set)
		}
	}
	if n.Intersection != nil {
		for _, child := range n.Intersection.Nodes {
			collectLeafUsers(child, set)
		}
	}
	if n.Difference != nil {
		collectLeafUsers(n.Difference.Base, set)
	}
}

// Expand returns the OpenFGA UsersetTree for (object, relation).
//
// Resolution is shallow by default: computed-userset rewrites surface
// as Leaf.Computed pointers and TTU rewrites surface as
// Leaf.TupleToUserset pointers. The caller either consumes those
// pointers directly (matches OpenFGA tooling) or uses
// UsersetTree.FlattenUsers for the resolved-users-only flat list.
//
// Wildcards ([type:*]) and userset references ([group#member]) survive
// the projection inline as user-strings in Leaf.Users — never expanded.
//
// Expand honours the same WithUsersetValidation / WithRequestValidation
// options as Check; validation errors short-circuit before any SQL is
// issued.
func (c *Checker) Expand(ctx context.Context, object ObjectLike, relation RelationLike, opts ...ExpandOption) (*UsersetTree, error) {
	resolved := applyExpand(opts)

	obj := object.FGAObject()
	rel := relation.FGARelation()

	if c.validateRequest {
		// Reuse the same validator surface Check / Explain use; passing
		// an empty subject is the convention for "object-side only".
		if err := c.validateCheckRequest(ctx, c.q, Object{}, rel, obj); err != nil {
			return nil, err
		}
	}

	var subjectType any
	if resolved.subjectType != "" {
		subjectType = string(resolved.subjectType)
	}
	var maxLeaf any
	if resolved.maxLeaf > 0 {
		maxLeaf = resolved.maxLeaf
	}

	var raw []byte
	err := c.q.QueryRowContext(ctx,
		fmt.Sprintf("SELECT %s($1, $2, $3, $4, $5)::text", prefixIdent("expand_permission", c.databaseSchema)),
		obj.Type, obj.ID, rel, subjectType, maxLeaf,
	).Scan(&raw)
	if err != nil {
		return nil, c.mapError("expand_permission", err)
	}

	var tree UsersetTree
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("expand_permission: decoding tree: %w", err)
	}
	return &tree, nil
}
