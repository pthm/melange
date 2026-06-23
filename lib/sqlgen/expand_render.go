package sqlgen

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// Expand codegen. Mirrors the shape of explain_render.go but emits the
// OpenFGA-shaped UsersetTree JSONB (see melange/expand.go and
// expand_blocks.go). Per-relation function bodies are pure SQL inside a
// PL/pgSQL RETURN — Expand is shallow (no recursion, no cycle detection,
// no per-call truncation), so the only thing the body does is build the
// JSONB tree for the relation's direct rewrites.
//
// Slice 2.1 scope: direct grants (Leaf.Users via jsonb_agg over
// melange_tuples) and computed-userset rewrites (Leaf.Computed pointers
// emitted from RelationAnalysis.DirectImpliedBy). Slices 2.2 / 2.3 / 2.4
// drop the corresponding eligibility gates and add TTU, intersection,
// exclusion, wildcards, usersets, and the p_max_leaf cap.

// expandFunctionName returns "expand_{type}_{relation}".
func expandFunctionName(objectType, relation string) string {
	return SafeIdentifier("expand_", objectType, relation, "")
}

// expandFunctionArgs returns the per-relation expand function signature.
// Three args only: the object id being expanded, the Melange-extension
// subject-type filter (NULL = all types), and the Melange-extension leaf
// cap (NULL = unbounded, OpenFGA-equivalent). No p_visited / p_max_nodes
// because Expand doesn't recurse.
func expandFunctionArgs() []FuncArg {
	return []FuncArg{
		{Name: "p_object_id", Type: "TEXT"},
		{Name: "p_subject_type", Type: "TEXT", Default: Raw("NULL")},
		{Name: "p_max_leaf", Type: "INTEGER", Default: Raw("NULL")},
	}
}

func expandFunctionHeader(plan ExpandPlan) []string {
	return []string{
		"Generated expand function for " + plan.ObjectType + "." + plan.Relation,
		"Returns OpenFGA-shaped UsersetTree JSONB. Shallow by default — computed",
		"rewrites surface as Leaf.Computed pointers; callers chase them with",
		"follow-up Expand calls or use Checker.ExpandRecursive.",
	}
}

// ExpandPlan is the per-relation input to RenderExpandFunction. It's
// computed from a RelationAnalysis by BuildExpandPlan; the rendering
// stage is a pure function of the plan so it stays trivially testable.
//
// Rewrites carry the per-rewrite shape (direct or computed) in the order
// they should appear under the Union node. A single-rewrite plan emits
// the leaf directly (no Union wrapper); multi-rewrite plans emit the
// Union envelope around per-rewrite children.
type ExpandPlan struct {
	DatabaseSchema string
	ObjectType     string
	Relation       string
	Rewrites       []ExpandRewrite
}

// ExpandRewrite is one of the per-rewrite shapes Expand can emit for
// slice 2.1: either a Direct grant (Leaf.Users via jsonb_agg) or a
// Computed pointer (Leaf.Computed naming the implied relation). The
// fields are mutually exclusive; the discriminator is which is non-zero.
type ExpandRewrite struct {
	// Direct is the subject-type whitelist for a direct rewrite. nil/empty
	// means this rewrite is not a direct grant.
	Direct []string
	// Computed is the implied relation name on the same object type for
	// a computed-userset rewrite. Empty means this rewrite is not a
	// computed pointer.
	Computed string
}

// ComputeExpandEligibility returns, for each (object_type, relation),
// whether the slice 2.1 expand renderer can produce a tree for it.
// Mirrors ComputeExplainEligibility's surface so CollectFunctionNames
// can call either without branching, but the implementation is trivial:
// no transitive sweep is needed because Expand is shallow (computed/TTU
// rewrites surface as unresolved pointers — an ineligible callee
// doesn't disable the caller).
func ComputeExpandEligibility(analyses []RelationAnalysis) map[string]map[string]bool {
	eligible := make(map[string]map[string]bool, len(analyses))
	for _, a := range analyses {
		if _, ok := BuildExpandPlan(a, ""); !ok {
			continue
		}
		if eligible[a.ObjectType] == nil {
			eligible[a.ObjectType] = make(map[string]bool)
		}
		eligible[a.ObjectType][a.Relation] = true
	}
	return eligible
}

// BuildExpandPlan derives the per-rewrite plan from a RelationAnalysis.
// Returns (plan, true) when the relation is eligible for slice 2.1; the
// dispatcher routes ineligible relations to the no-entry sentinel.
//
// Slice 2.1 eligibility: at least one rewrite (direct or computed), AND
// no usersets / wildcards / TTU / intersection / exclusion / complex
// userset patterns. Later slices drop these gates.
func BuildExpandPlan(a RelationAnalysis, databaseSchema string) (ExpandPlan, bool) {
	if !expandLocalSupported(a) {
		return ExpandPlan{}, false
	}
	plan := ExpandPlan{
		DatabaseSchema: databaseSchema,
		ObjectType:     a.ObjectType,
		Relation:       a.Relation,
	}
	if a.Features.HasDirect {
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{
			Direct: append([]string(nil), a.AllowedSubjectTypes...),
		})
	}
	for _, implied := range a.DirectImpliedBy {
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{Computed: implied})
	}
	if len(plan.Rewrites) == 0 {
		// Relation has no concrete access paths — let the dispatcher
		// sentinel handle it rather than emitting a structurally empty
		// tree the caller would have to special-case.
		return ExpandPlan{}, false
	}
	return plan, true
}

// expandLocalSupported is the slice 2.1 eligibility predicate. Each
// follow-up slice drops one of these gates as it adds renderer support.
func expandLocalSupported(a RelationAnalysis) bool {
	f := a.Features
	if f.HasUserset || f.HasWildcard {
		return false // slice 2.3
	}
	if f.HasRecursive {
		return false // slice 2.2 (TTU)
	}
	if f.HasIntersection {
		return false // slice 2.2
	}
	if f.HasExclusion {
		return false // slice 2.2
	}
	if a.HasComplexUsersetPatterns {
		return false // follow-up
	}
	return f.HasDirect || f.HasImplied
}

// RenderExpandFunction is the entry point for expand_* function
// generation. Returns the complete CREATE OR REPLACE FUNCTION text plus
// a trailing newline so file-level concatenation stays clean.
func RenderExpandFunction(plan ExpandPlan) string {
	children := make([]string, 0, len(plan.Rewrites))
	for _, r := range plan.Rewrites {
		children = append(children, buildExpandRewriteNode(plan, r))
	}

	var rootValue string
	if len(children) == 1 {
		// Strip the per-rewrite child wrapper and use the leaf's value
		// slot directly on the root — matches OpenFGA's emission for
		// relations with a single rewrite (no redundant Union envelope).
		rootValue = buildExpandRewriteValue(plan, plan.Rewrites[0])
	} else {
		rootValue = BuildExpandUnionJSON(children)
	}

	rootNode := BuildExpandNodeJSON(
		BuildExpandNodeName(Lit(plan.ObjectType).SQL(), "p_object_id", plan.Relation),
		rootValue,
	)
	body := BuildExpandTreeRoot(rootNode)

	fn := PlpgsqlFunction{
		Schema:  plan.DatabaseSchema,
		Name:    expandFunctionName(plan.ObjectType, plan.Relation),
		Args:    expandFunctionArgs(),
		Returns: "JSONB",
		Body:    []Stmt{ReturnValue{Value: Raw(body)}},
		Header:  expandFunctionHeader(plan),
	}
	return fn.SQL() + "\n"
}

// buildExpandRewriteNode wraps one rewrite's value slot in the outer
// `{name, ...}` UsersetTreeNode envelope. The name matches the parent
// relation (not the rewrite target) because OpenFGA's per-rewrite child
// nodes are all "branches that satisfy the parent relation", so they
// share its identity.
func buildExpandRewriteNode(plan ExpandPlan, r ExpandRewrite) string {
	return BuildExpandNodeJSON(
		BuildExpandNodeName(Lit(plan.ObjectType).SQL(), "p_object_id", plan.Relation),
		buildExpandRewriteValue(plan, r),
	)
}

// buildExpandRewriteValue emits the leaf / difference / union /
// intersection slot for a single rewrite — without the surrounding
// {name, ...} envelope, so it can be either the value of a per-rewrite
// child OR (when the relation has a single rewrite) the value slot of
// the root node directly.
func buildExpandRewriteValue(plan ExpandPlan, r ExpandRewrite) string {
	switch {
	case r.Computed != "":
		usersetExpr := BuildExpandNodeName(Lit(plan.ObjectType).SQL(), "p_object_id", r.Computed)
		return BuildExpandComputedLeafJSON(usersetExpr)
	case len(r.Direct) > 0:
		return buildExpandDirectLeaf(plan, r.Direct)
	default:
		// Defensive — BuildExpandPlan never appends a zero-value rewrite.
		// Emit an empty Leaf.Users so the dispatcher response stays
		// structurally valid rather than blowing up on NULL.
		return BuildExpandUsersLeafJSON("'[]'::jsonb", "")
	}
}

// buildExpandDirectLeaf renders the Leaf.Users projection for a direct
// rewrite: jsonb_agg over melange_tuples matching the relation's
// allowed subject types, optionally filtered by p_subject_type. The
// emitted user-strings are OpenFGA-formatted (`<subject_type>:<subject_id>`)
// so they survive any later flattening helper unchanged.
//
// The aggregation is wrapped in COALESCE(..., '[]'::jsonb) so an
// empty result becomes `{users: []}` rather than `{users: null}` —
// OpenFGA tooling expects an array, not null.
func buildExpandDirectLeaf(plan ExpandPlan, subjectTypes []string) string {
	tuplesTable := sqldsl.PrefixIdent("melange_tuples", plan.DatabaseSchema)
	subjectTypeList := formatSQLStringList(subjectTypes)

	// The subject-type allow-list (from the schema) AND the per-call
	// filter both narrow the SELECT. The per-call filter is optional
	// (NULL = no filter, matches OpenFGA).
	where := []string{
		"object_type = " + sqldsl.QuoteLiteral(plan.ObjectType),
		"object_id = p_object_id",
		"relation = " + sqldsl.QuoteLiteral(plan.Relation),
	}
	if subjectTypeList != "" {
		where = append(where, "subject_type IN ("+subjectTypeList+")")
	}
	where = append(where,
		"(p_subject_type IS NULL OR subject_type = p_subject_type)")

	usersExpr := fmt.Sprintf(
		"COALESCE((SELECT jsonb_agg(subject_type || ':' || subject_id ORDER BY subject_type, subject_id) FROM %s WHERE %s), '[]'::jsonb)",
		tuplesTable, strings.Join(where, " AND "))

	// users_truncated is reserved for slice 2.4 — pass empty so the key
	// is omitted entirely until that slice lands.
	return BuildExpandUsersLeafJSON(usersExpr, "")
}
