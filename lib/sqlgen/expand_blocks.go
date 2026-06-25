package sqlgen

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// JSONB construction helpers for Expand. Sibling to trace_blocks.go but
// emits the OpenFGA-shaped UsersetTree types declared in melange/expand.go.
// Every JSON shape Expand emits routes through here so the wire format
// matches openfgav1.UsersetTree field-for-field and the per-rewrite
// renderer doesn't need to know about JSONB construction.
//
// As with trace_blocks.go these return plain SQL strings rather than
// sqldsl.Expr because the jsonb_build_* call sites take variadic
// positional args and the alternation between literals and SQL
// expressions is irregular enough that an Expr tree adds noise without
// buying composition.

// BuildExpandNodeName emits the canonical "<type>:<id>#<relation>" name
// every UsersetTreeNode carries. typeExpr and idExpr are SQL expressions
// (column refs, literals); relation is a Go string that gets quoted into
// a SQL literal here.
func BuildExpandNodeName(typeExpr, idExpr, relation string) string {
	return fmt.Sprintf("(%s || ':' || %s || %s)",
		typeExpr, idExpr,
		sqldsl.QuoteLiteral("#"+relation))
}

// BuildExpandComputedLeafJSON emits a `{leaf: {computed: {userset: ...}}}`
// fragment for a single computed-userset rewrite (e.g. `define viewer:
// editor` emits a leaf whose Computed.Userset is "<obj>:#editor").
// usersetExpr is a SQL expression that evaluates to the canonical
// "<obj_type>:<obj_id>#<rel>" string; the helper handles all the
// jsonb_build_object wrapping including the outer `leaf` key so the
// caller can concatenate the result directly into a UsersetTreeNode.
func BuildExpandComputedLeafJSON(usersetExpr string) string {
	return fmt.Sprintf(
		"jsonb_build_object('leaf', jsonb_build_object('computed', jsonb_build_object('userset', %s)))",
		usersetExpr)
}

// BuildExpandUsersLeafJSON emits a `{leaf: {users: {users: [...]}}}`
// fragment. usersExpr is a SQL expression evaluating to a JSONB array of
// OpenFGA-formatted user strings (e.g. the result of jsonb_agg() over a
// SELECT). When usersTruncatedExpr is non-empty it should evaluate to a
// boolean; the users_truncated key is omitted from the JSONB when the
// expression is false (matching the omitempty Go tag).
//
// usersExpr must always be a JSONB array — never NULL. Callers that build
// the array via aggregation should COALESCE the SELECT to '[]'::jsonb so
// an empty leaf is structurally `{users: []}` rather than `{users: null}`.
func BuildExpandUsersLeafJSON(usersExpr, usersTruncatedExpr string) string {
	// Build the inner Users object first: {users: <arr>}. The optional
	// users_truncated key is merged via `||` AT THE OBJECT level, NOT
	// inside the jsonb_build_object call — Postgres's `||` on a JSONB
	// array would APPEND the truncated object as an array element,
	// mangling the user list. The outer parentheses keep the
	// concatenation scoped to this object before it's nested.
	usersObj := "jsonb_build_object('users', " + usersExpr + ")"
	if usersTruncatedExpr != "" {
		usersObj = "(" + usersObj + fmt.Sprintf(
			" || CASE WHEN %s THEN jsonb_build_object('users_truncated', true) ELSE '{}'::jsonb END",
			usersTruncatedExpr) + ")"
	}
	return "jsonb_build_object('leaf', jsonb_build_object('users', " + usersObj + "))"
}

// BuildExpandTTULeafJSON emits a `{leaf: {tuple_to_userset: {...}}}`
// fragment for a TTU rewrite. tuplesetExpr names the linking relation
// (canonical "<obj_type>:<obj_id>#<linking>"); computedExprs are SQL
// expressions each evaluating to a Computed JSONB object (matches the
// proto's repeated computed field — one entry per allowed parent
// relation).
//
// Reserved for slice 2.2 — declared now so the helper surface is
// complete and the per-rewrite renderer can call it once the analysis
// for TTU lands.
func BuildExpandTTULeafJSON(tuplesetExpr string, computedExprs []string) string {
	computedArr := "jsonb_build_array(" + strings.Join(computedExprs, ", ") + ")"
	return fmt.Sprintf(
		"jsonb_build_object('leaf', jsonb_build_object('tuple_to_userset', jsonb_build_object('tupleset', %s, 'computed', %s)))",
		tuplesetExpr, computedArr)
}

// BuildExpandUnionJSON wraps per-rewrite child Node JSONB objects in
// `{union: {nodes: [...]}}`. childExprs are SQL expressions each
// evaluating to a complete UsersetTreeNode (i.e. each carries its own
// name + one populated leaf/difference/union/intersection slot).
func BuildExpandUnionJSON(childExprs []string) string {
	return fmt.Sprintf(
		"jsonb_build_object('union', jsonb_build_object('nodes', jsonb_build_array(%s)))",
		strings.Join(childExprs, ", "))
}

// BuildExpandIntersectionJSON wraps child Node JSONBs in
// `{intersection: {nodes: [...]}}`. Same shape as the Union wrapper —
// the discriminator on the outer UsersetTreeNode is which key is
// populated. Reserved for slice 2.2.
func BuildExpandIntersectionJSON(childExprs []string) string {
	return fmt.Sprintf(
		"jsonb_build_object('intersection', jsonb_build_object('nodes', jsonb_build_array(%s)))",
		strings.Join(childExprs, ", "))
}

// BuildExpandDifferenceJSON wraps base/subtract Node JSONBs in
// `{difference: {base: ..., subtract: ...}}`. Both args are SQL
// expressions each evaluating to a complete UsersetTreeNode. Named
// slots (not positional children) match OpenFGA's proto exactly so
// consumers can address each half without ambiguity. Reserved for
// slice 2.2.
func BuildExpandDifferenceJSON(baseExpr, subtractExpr string) string {
	return fmt.Sprintf(
		"jsonb_build_object('difference', jsonb_build_object('base', %s, 'subtract', %s))",
		baseExpr, subtractExpr)
}

// BuildExpandNodeJSON wraps a name + one populated value slot into a
// complete UsersetTreeNode. valueExpr is a SQL expression that
// evaluates to one of the leaf/difference/union/intersection JSONB
// objects emitted by the Build*JSON helpers above — it must already
// include the discriminator key (the `users`/`computed`/`tuple_to_userset`
// wrapping for leaves, or the `union`/`intersection`/`difference`
// wrapping for non-leaves).
//
// The resulting object always has `name` first so jsonb_build_object's
// deterministic key ordering aligns with the Go struct's field order.
func BuildExpandNodeJSON(nameExpr, valueExpr string) string {
	return fmt.Sprintf(
		"jsonb_build_object('name', %s) || %s",
		nameExpr, valueExpr)
}

// BuildExpandTreeRoot emits the top-level `{root: <node>}` envelope
// every Expand response uses. rootExpr is a SQL expression evaluating
// to a complete UsersetTreeNode JSONB. Companion to
// expandNoEntrySentinelSQL in expand_functions.go.
func BuildExpandTreeRoot(rootExpr string) string {
	return "jsonb_build_object('root', " + rootExpr + ")"
}
