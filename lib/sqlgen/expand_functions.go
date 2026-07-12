package sqlgen

import (
	"fmt"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// Expand orchestration. Sibling to explain_functions.go but produces
// expand_<type>_<rel> functions and the expand_permission dispatcher.
// Public entry: GenerateSQL calls generateExpandFunction per eligible
// relation (gate is BuildExpandPlan) and generateExpandDispatcher once
// for the whole schema. The dispatcher routes
// (object_type, relation) → expand_{type}_{relation}.

// expandDispatcherInternalArgs is the expand dispatcher's internal
// signature. Mirrors expandFunctionArgs but with the routing keys
// (p_relation, p_object_type) prepended.
func expandDispatcherInternalArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_object_type", Type: "TEXT"},
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_relation", Type: "TEXT"},
		{Name: "p_subject_type", Type: "TEXT", Default: Raw("NULL")},
		{Name: "p_max_leaf", Type: "INTEGER", Default: Raw("NULL")},
	}
}

// expandDispatcherPublicArgs is the public-facing signature. Identical to
// the internal one because Expand doesn't recurse (no p_visited to strip).
// Kept separate from the internal signature so internal-only fields can be
// added later without changing the public surface.
func expandDispatcherPublicArgs() []FuncArg {
	return expandDispatcherInternalArgs()
}

// generateExpandFunction wraps RenderExpandFunction with the per-relation
// plan derivation, returning ("", false) when BuildExpandPlan reports the
// relation ineligible.
func generateExpandFunction(a RelationAnalysis, databaseSchema string) (string, bool) {
	plan, ok := BuildExpandPlan(a, databaseSchema)
	if !ok {
		return "", false
	}
	return RenderExpandFunction(plan), true
}

// generateExpandDispatcher renders the public expand_permission function
// and its internal companion. Same SqlFunction/PlpgsqlFunction split as
// the explain dispatcher: PL/pgSQL internal for the routing CASE,
// pure-SQL public wrapper for the hot-path planner symmetry.
//
// eligible records the (object_type, relation) pairs for which an expand
// function was generated; unsupported pairs route to the no-entry
// sentinel rather than crashing.
func generateExpandDispatcher(analyses []RelationAnalysis, databaseSchema string, eligible map[string]map[string]bool) string {
	cases := buildExpandDispatcherCases(analyses, databaseSchema, eligible)
	if len(cases) == 0 {
		return renderEmptyExpandDispatcher(databaseSchema)
	}
	return renderExpandDispatcherWithCases(databaseSchema, cases)
}

// buildExpandDispatcherCases mirrors buildDispatcherCases / buildExplain*
// but only emits cases for relations whose expand renderer can produce
// a correct tree per the precomputed eligibility map.
func buildExpandDispatcherCases(analyses []RelationAnalysis, databaseSchema string, eligible map[string]map[string]bool) []DispatcherCase {
	var cases []DispatcherCase
	for _, a := range analyses {
		if !eligible[a.ObjectType][a.Relation] {
			continue
		}
		cases = append(cases, DispatcherCase{
			DatabaseSchema:    databaseSchema,
			ObjectType:        a.ObjectType,
			Relation:          a.Relation,
			CheckFunctionName: expandFunctionName(a.ObjectType, a.Relation),
		})
	}
	return cases
}

func renderExpandDispatcherWithCases(databaseSchema string, cases []DispatcherCase) string {
	internalFn := PlpgsqlFunction{
		Schema:  databaseSchema,
		Name:    "expand_permission_internal",
		Args:    expandDispatcherInternalArgs(),
		Returns: "JSONB",
		// IF-chain, not RETURN CASE: measurably faster on the hot path (see
		// dispatchIfChain in check_functions.go).
		Body: dispatchIfChain(cases, expandDispatchCall, Raw(expandNoEntrySentinelSQL())),
		Header: []string{
			"Generated internal dispatcher for expand_permission",
			"Routes (object_type, relation) to specialised expand_* functions",
			"Returns an empty Leaf.Users sentinel for unknown / not-yet-supported",
			"pairs so OpenFGA tooling deserialises without special-casing.",
			"Callers that need to distinguish 'no one has access' from 'expand",
			"not supported for this relation' should compare against Check.",
		},
		// Routes only to schema-qualified expand_{type}_{rel} calls, no
		// unqualified melange_tuples.
		NoSearchPath: true,
	}

	publicFn := SqlFunction{
		Schema:  databaseSchema,
		Name:    "expand_permission",
		Args:    expandDispatcherPublicArgs(),
		Returns: "JSONB",
		Body: Raw("SELECT " + sqldsl.PrefixIdent("expand_permission_internal", databaseSchema) +
			"(p_object_type, p_object_id, p_relation, p_subject_type, p_max_leaf)"),
		Header: []string{
			"Generated public dispatcher for expand_permission",
			"Companion to list_accessible_subjects — returns an OpenFGA-shaped",
			"UsersetTree JSONB describing who has the relation on the object.",
			"Shallow by default: computed/TTU rewrites surface as unresolved",
			"pointers (use Checker.ExpandRecursive client-side to chase).",
		},
		NoSearchPath: true,
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

// renderEmptyExpandDispatcher emits a no-op dispatcher when the schema
// has no eligible relations. Returns a structurally valid UsersetTree
// with an empty Users leaf so callers parsing the JSON don't choke.
func renderEmptyExpandDispatcher(databaseSchema string) string {
	body := "SELECT " + expandNoEntrySentinelSQL()

	internalFn := SqlFunction{
		Schema:  databaseSchema,
		Name:    "expand_permission_internal",
		Args:    expandDispatcherInternalArgs(),
		Returns: "JSONB",
		Body:    Raw(body),
		Header: []string{
			"Generated empty dispatcher for expand_permission",
			"(no eligible relations — every request returns an empty tree)",
		},
		NoSearchPath: true,
	}

	publicFn := SqlFunction{
		Schema:       databaseSchema,
		Name:         "expand_permission",
		Args:         expandDispatcherPublicArgs(),
		Returns:      "JSONB",
		Body:         Raw(body),
		NoSearchPath: true,
	}

	return internalFn.SQL() + "\n\n" + publicFn.SQL() + "\n"
}

// expandDispatchCall is the specialized expand_<type>_<rel> call for one arm.
func expandDispatchCall(c DispatcherCase) Expr {
	return Func{
		Schema: c.DatabaseSchema,
		Name:   c.CheckFunctionName,
		Args:   []Expr{Raw("p_object_id"), Raw("p_subject_type"), Raw("p_max_leaf")},
	}
}

// expandNoEntrySentinelSQL emits the JSONB UsersetTree returned when
// the dispatcher has no CASE branch for the requested (object_type,
// relation). Shape matches OpenFGA's UsersetTree exactly — a root
// node carrying the requested name and an empty Leaf.Users — so
// OpenFGA tooling deserialises without an adapter.
//
// Callers that need to distinguish "no users have this permission"
// from "expand isn't yet supported for this relation" should cross-
// reference against Check (which has full feature coverage).
func expandNoEntrySentinelSQL() string {
	// Name is built inline rather than via BuildExpandNodeName because
	// the relation portion is a runtime column ref (p_relation), not a
	// literal — the standard helper quotes its relation arg as a SQL
	// literal which would produce '#' || ''<p_relation>'' instead of
	// '#' || p_relation.
	nameExpr := "(p_object_type || ':' || p_object_id || '#' || p_relation)"
	emptyLeaf := BuildExpandUsersLeafJSON("'[]'::jsonb", "")
	rootNode := fmt.Sprintf("jsonb_build_object('name', %s) || %s", nameExpr, emptyLeaf)
	return BuildExpandTreeRoot(rootNode)
}
