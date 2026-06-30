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
	// Exclusion names the relation in `but not X` when the relation has
	// a single simple exclusion (e.g., `viewer: writer but not banned`
	// gives Exclusion="banned"). When set, the renderer wraps the
	// rewrites-derived tree in `Difference{base, subtract}` where
	// subtract is a Leaf.Computed pointer to the excluded relation.
	//
	// Multi-exclusion patterns (`but not X but not Y`), TTU-excluded
	// (`but not X from Y`), and intersection-excluded
	// (`but not (A and B)`) are not yet handled — those relations
	// route to the dispatcher's empty-leaf sentinel until follow-up
	// slices land.
	Exclusion string
}

// ExpandRewrite is one of the per-rewrite shapes Expand can emit.
// Fields are mutually exclusive; the discriminator is which one is
// non-zero. Slice 2.1 introduced Direct + Computed; 2.2a added TTU;
// 2.2c adds Intersection.
type ExpandRewrite struct {
	// Direct is the subject-type whitelist for a direct rewrite. nil/empty
	// means this rewrite is not a direct grant.
	Direct []string
	// Computed is the implied relation name on the same object type for
	// a computed-userset rewrite. Empty means this rewrite is not a
	// computed pointer.
	Computed string
	// TTU carries the "X from Y" rewrite info. nil means this rewrite is
	// not a TTU. When set the renderer emits a Leaf.TupleToUserset with
	// tupleset = "<obj>:#<linking>" and one Computed per linked object
	// (enumerated at expand time via jsonb_agg over melange_tuples).
	TTU *ParentRelationInfo
	// Intersection lists the parts for `a and b [and …]` rewrites. nil
	// means this rewrite is not an intersection. When set the renderer
	// emits a Nodes intersection wrapping one child per part — each
	// child's value slot dispatches by part shape (Computed pointer for
	// plain-relation parts, Leaf.Users for IsThis, Leaf.TupleToUserset
	// for ParentRelation, Difference for per-part exclusion). Same
	// shallow-pointer treatment as the Computed rewrite because
	// OpenFGA's Expand never resolves intersection parts recursively.
	Intersection []IntersectionPart
}

// directSubjectTypes returns the subject types associated with the
// plan's top-level direct rewrite (if any), so IsThis intersection
// parts can reuse them. Returns nil when the plan has no direct
// rewrite, in which case the caller should default to "user".
func (p ExpandPlan) directSubjectTypes() []string {
	for _, r := range p.Rewrites {
		if len(r.Direct) > 0 {
			return r.Direct
		}
	}
	return nil
}

// sliceContains is a small helper for the BuildExpandPlan union of
// direct subject types and userset pattern subject types. Local because
// the standard library's slices.Contains requires Go 1.21+ generics
// and the call site has fewer than half a dozen entries — a linear
// scan is the right ceiling.
func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
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
	// Slice 2.3: a single "direct" rewrite covers concrete users
	// (`[user]`), wildcards (`[user:*]`), AND userset references
	// (`[group#member]`) — all three shapes live in melange_tuples as
	// direct grant rows (the userset is encoded in subject_id as
	// "<id>#<relation>", not in a separate column). The renderer's
	// projection `subject_type || ':' || subject_id` naturally
	// produces OpenFGA-formatted strings for every shape:
	// "user:alice", "user:*", "group:eng#member".
	//
	// Subject-type whitelist unions the direct types and the userset
	// pattern subject types so a SELECT for "[group#member]" doesn't
	// silently drop group-typed rows. AllowedSubjectTypes is NOT used
	// here because it's the *transitive* closure (including types
	// reachable through closure relations); we want the IMMEDIATE
	// types valid for THIS relation's direct grants.
	directTypes := append([]string(nil), a.DirectSubjectTypes...)
	for _, p := range a.UsersetPatterns {
		if !sliceContains(directTypes, p.SubjectType) {
			directTypes = append(directTypes, p.SubjectType)
		}
	}
	if len(directTypes) > 0 {
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{Direct: directTypes})
	}
	for _, implied := range a.DirectImpliedBy {
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{Computed: implied})
	}
	for i := range a.ParentRelations {
		// Take a pointer to the slice entry so the rewrite captures the
		// AllowedLinkingTypes / LinkingRelation by reference — the per-
		// iteration loop variable would be reused otherwise.
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{TTU: &a.ParentRelations[i]})
	}
	for _, g := range a.IntersectionGroups {
		// Each intersection group emits one rewrite — the parts AND
		// together as a Nodes intersection. Multiple groups OR together
		// at the top level (via the existing multi-rewrite Union wrap).
		// The plan captures full IntersectionPart structs so the
		// renderer can dispatch by part shape (slice 1.9 — IsThis /
		// ParentRelation / per-part ExcludedRelation).
		plan.Rewrites = append(plan.Rewrites, ExpandRewrite{
			Intersection: append([]IntersectionPart(nil), g.Parts...),
		})
	}
	if a.Features.HasExclusion && isSimpleExclusion(a) {
		plan.Exclusion = a.ExcludedRelations[0]
	}
	if len(plan.Rewrites) == 0 {
		// Relation has no concrete access paths — let the dispatcher
		// sentinel handle it rather than emitting a structurally empty
		// tree the caller would have to special-case.
		return ExpandPlan{}, false
	}
	return plan, true
}

// expandLocalSupported is the slice 2.x eligibility predicate. Each
// slice drops one gate as it adds renderer support — 2.1 covered direct
// + computed; 2.2a added TTU (Leaf.TupleToUserset); 2.2b added simple
// exclusion (Difference); 2.2c added intersection (Nodes intersection);
// 2.3 added wildcards + userset references (inline as user-strings in
// Leaf.Users).
func expandLocalSupported(a RelationAnalysis) bool {
	f := a.Features
	// Slice 1.9 dropped the intersectionGroupsAreSimpleForExpand gate.
	// The per-part renderer now dispatches on IntersectionPart shape:
	// plain → Leaf.Computed, IsThis → Leaf.Users, ParentRelation →
	// Leaf.TupleToUserset, ExcludedRelation → Difference. The
	// predicate still exists below for documentation / spec reference
	// but is no longer consulted at eligibility time.
	if f.HasExclusion && !isSimpleExclusion(a) {
		return false // multi-exclusion / TTU-excluded / intersection-excluded
	}
	// SPIKE 1.8: dropped the HasComplexUsersetPatterns gate. Expand
	// inlines userset references as user-strings in Leaf.Users; the
	// membership relation's complexity doesn't affect the wire shape
	// (callers chase Computed/TupleToUserset pointers via
	// ExpandRecursive). Reinstate the gate if the spike surfaces FAILs.
	return f.HasDirect || f.HasImplied || f.HasRecursive || f.HasIntersection || f.HasUserset
}

// intersectionGroupsAreSimpleForExpand is true when every part of every
// intersection group is a plain relation reference — no `[user]`-direct
// (IsThis), no `X from Y` (ParentRelation), no per-part exclusion
// (ExcludedRelation). The renderer can only emit Leaf.Computed pointers
// for parts today; the exotic shapes need their own per-part renderer
// branches and land in a follow-up slice.
//
// Mirrors lib/sqlgen/explain_render.go's intersectionGroupsAreSimple
// but lives separately because Expand emits a different per-part shape
// (single Computed pointer vs Explain's recursive Node).
func intersectionGroupsAreSimpleForExpand(a RelationAnalysis) bool {
	for _, g := range a.IntersectionGroups {
		for _, p := range g.Parts {
			if p.IsThis || p.ParentRelation != nil || p.ExcludedRelation != "" {
				return false
			}
			if p.Relation == "" {
				return false // defensive — a part with no relation is unrenderable
			}
		}
	}
	return true
}

// isSimpleExclusion is true when the relation has exactly one simple
// "but not X" exclusion — a single relation name in ExcludedRelations,
// no TTU exclusions, no intersection-group exclusions. The single
// exclusion can be either simple (tuple-lookup-resolvable) or complex
// (function-call-resolvable) because Expand emits a Computed pointer
// either way and the caller chases it; we don't actually evaluate the
// exclusion at the Expand call site.
//
// This is the predicate gate for slice 2.2b. The exotic variants
// remain gated until follow-up slices land.
func isSimpleExclusion(a RelationAnalysis) bool {
	if len(a.ExcludedRelations) != 1 {
		return false
	}
	if len(a.ExcludedParentRelations) > 0 {
		return false
	}
	if len(a.ExcludedIntersectionGroups) > 0 {
		return false
	}
	return true
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

	nameExpr := BuildExpandNodeName(Lit(plan.ObjectType).SQL(), "p_object_id", plan.Relation)

	if plan.Exclusion != "" {
		// Wrap the rewrites-derived tree as the Difference's base. The
		// base node shares the parent relation's name (it represents
		// "the relation without exclusion applied"); the subtract names
		// the excluded relation. OpenFGA's named-slot shape — base /
		// subtract are addressable by key rather than position.
		baseNode := BuildExpandNodeJSON(nameExpr, rootValue)

		subtractValue := BuildExpandComputedLeafJSON(
			BuildExpandNodeName(Lit(plan.ObjectType).SQL(), "p_object_id", plan.Exclusion))
		subtractNameExpr := BuildExpandNodeName(
			Lit(plan.ObjectType).SQL(), "p_object_id", plan.Exclusion)
		subtractNode := BuildExpandNodeJSON(subtractNameExpr, subtractValue)

		rootValue = BuildExpandDifferenceJSON(baseNode, subtractNode)
	}

	rootNode := BuildExpandNodeJSON(nameExpr, rootValue)
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
	case len(r.Intersection) > 0:
		return buildExpandIntersectionValue(plan, r.Intersection)
	case r.TTU != nil:
		return buildExpandTTULeaf(plan, *r.TTU)
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

// buildExpandIntersectionValue assembles the `Nodes intersection`
// envelope for an `a and b [and c …]` rewrite. Each part becomes a
// child UsersetTreeNode whose value slot dispatches by part shape:
//   - plain relation reference → Leaf.Computed pointer to <obj>:#<rel>
//     (shallow, caller chases via ExpandRecursive)
//   - IsThis (`[user]` inline)      → Leaf.Users via buildExpandDirectLeaf
//   - ParentRelation (`X from Y`)   → Leaf.TupleToUserset via buildExpandTTULeaf
//   - per-part ExcludedRelation     → Difference{base, subtract}
//     wrapping the per-part-shape's child as base and a Computed
//     pointer to the excluded relation as subtract
//
// Per-part child nodes are named after the part relation (or "this"
// for IsThis parts, matching OpenFGA's convention). The intersection
// node itself doesn't carry the parent relation's name — that lives
// on the wrapping UsersetTreeNode emitted by the caller.
func buildExpandIntersectionValue(plan ExpandPlan, parts []IntersectionPart) string {
	objectTypeLit := Lit(plan.ObjectType).SQL()
	children := make([]string, 0, len(parts))
	for _, part := range parts {
		children = append(children, buildExpandIntersectionPartNode(plan, objectTypeLit, part))
	}
	return BuildExpandIntersectionJSON(children)
}

// buildExpandIntersectionPartNode emits one child UsersetTreeNode for
// an intersection part. The shape depends on which fields of the
// IntersectionPart are populated. When the part also carries a
// per-part ExcludedRelation (`writer and (editor but not blocked)`),
// the part's base shape is wrapped in a Difference whose subtract is
// a Computed pointer to the excluded relation — composing the slice
// 2.2b exclusion primitive with the per-part shape.
func buildExpandIntersectionPartNode(plan ExpandPlan, objectTypeLit string, part IntersectionPart) string {
	var partName, partValue string

	switch {
	case part.IsThis:
		// IsThis: `[user]` direct grant probe at the parent relation.
		// Name the node after the parent — OpenFGA's convention is
		// that this-style parts share the parent's identity.
		partName = BuildExpandNodeName(objectTypeLit, "p_object_id", plan.Relation)
		// Reuse the direct-rewrite leaf renderer; AllowedSubjectTypes
		// comes from the analysis but the part itself doesn't carry
		// it — fall back to the plan's existing direct rewrite if
		// one exists, otherwise default to "user" which is the
		// near-universal IsThis case in OpenFGA schemas.
		subjectTypes := plan.directSubjectTypes()
		if len(subjectTypes) == 0 {
			subjectTypes = []string{"user"}
		}
		partValue = buildExpandDirectLeaf(plan, subjectTypes)
	case part.ParentRelation != nil:
		// ParentRelation: `X from Y` inside the intersection. Name the
		// node after the linking relation's target — same convention
		// as the top-level TTU rewrite.
		partName = BuildExpandNodeName(objectTypeLit, "p_object_id", part.ParentRelation.LinkingRelation)
		partValue = buildExpandTTULeaf(plan, *part.ParentRelation)
	default:
		// Plain relation reference: Leaf.Computed pointer.
		partName = BuildExpandNodeName(objectTypeLit, "p_object_id", part.Relation)
		partValue = BuildExpandComputedLeafJSON(partName)
	}

	if part.ExcludedRelation != "" {
		// Per-part exclusion wraps the part's base in a Difference,
		// with subtract = Computed pointer to the excluded relation.
		// Composes slice 2.2b's Difference primitive with the per-part
		// shape from above.
		baseNode := BuildExpandNodeJSON(partName, partValue)
		subtractName := BuildExpandNodeName(objectTypeLit, "p_object_id", part.ExcludedRelation)
		subtractValue := BuildExpandComputedLeafJSON(subtractName)
		subtractNode := BuildExpandNodeJSON(subtractName, subtractValue)
		return BuildExpandNodeJSON(partName, BuildExpandDifferenceJSON(baseNode, subtractNode))
	}

	return BuildExpandNodeJSON(partName, partValue)
}

// buildExpandTTULeaf renders the Leaf.TupleToUserset projection for a
// "X from Y" rewrite. The tupleset names the linking relation
// ("<obj>:#<linking>"); the computed array enumerates one Computed
// entry per linked object found in melange_tuples, projecting
// "<linked_type>:<linked_id>#<parent_relation>". This matches OpenFGA's
// shape exactly: tupleset is the pointer-to-list, computed is the
// pointer-to-userset-per-linked-object.
//
// The aggregation runs at expand-call time so the tree reflects the
// current tuples (consistent with the rest of Melange's read-after-write
// behaviour). When no linking tuples exist the computed array is
// empty — that's a valid OpenFGA response meaning "no parents to
// inherit from".
func buildExpandTTULeaf(plan ExpandPlan, ttu ParentRelationInfo) string {
	tuplesTable := sqldsl.PrefixIdent("melange_tuples", plan.DatabaseSchema)

	// Tupleset is built inline rather than via BuildExpandNodeName
	// because the tupleset references the current object (whose id is
	// p_object_id, a runtime variable) and the linking relation (a
	// schema literal).
	tuplesetExpr := fmt.Sprintf(
		"(%s || ':' || p_object_id || %s)",
		Lit(plan.ObjectType).SQL(),
		sqldsl.QuoteLiteral("#"+ttu.LinkingRelation))

	// Computed array projects each linked-object identifier paired with
	// the parent relation name. ORDER BY keeps the output deterministic
	// across pg query plans.
	parentRelLit := sqldsl.QuoteLiteral("#" + ttu.Relation)
	where := []string{
		"object_type = " + sqldsl.QuoteLiteral(plan.ObjectType),
		"object_id = p_object_id",
		"relation = " + sqldsl.QuoteLiteral(ttu.LinkingRelation),
	}
	if len(ttu.AllowedLinkingTypes) > 0 {
		where = append(where,
			"subject_type IN ("+formatSQLStringList(ttu.AllowedLinkingTypes)+")")
	}
	computedAgg := fmt.Sprintf(
		"COALESCE((SELECT jsonb_agg(jsonb_build_object('userset', subject_type || ':' || subject_id || %s) ORDER BY subject_type, subject_id) FROM %s WHERE %s), '[]'::jsonb)",
		parentRelLit, tuplesTable, strings.Join(where, " AND "))

	// Inline the OpenFGA TupleToUserset shape directly rather than via
	// BuildExpandTTULeafJSON because the helper takes a []string of
	// pre-built Computed exprs (one per static parent type); here the
	// `computed` field is a single dynamic JSONB array built by jsonb_agg.
	return fmt.Sprintf(
		"jsonb_build_object('leaf', jsonb_build_object('tuple_to_userset', jsonb_build_object('tupleset', %s, 'computed', %s)))",
		tuplesetExpr, computedAgg)
}

// buildExpandDirectLeaf renders the Leaf.Users projection for a direct
// rewrite: jsonb_agg over melange_tuples matching the relation's
// allowed subject types, optionally filtered by p_subject_type, capped
// by p_max_leaf. The emitted user-strings are OpenFGA-formatted
// (`<subject_type>:<subject_id>`) so they survive any later flattening
// helper unchanged.
//
// The aggregation is wrapped in COALESCE(..., '[]'::jsonb) so an
// empty result becomes `{users: []}` rather than `{users: null}` —
// OpenFGA tooling expects an array, not null.
//
// p_max_leaf cap (slice 2.4): when set, the aggregation runs against
// a subquery with `LIMIT p_max_leaf` and the leaf's `users_truncated`
// field is set to TRUE when more rows exist beyond the cap. The cap
// uses two SELECTs against the same WHERE — one to fetch the capped
// page, one EXISTS-OFFSET probe to detect overflow. PostgreSQL won't
// share the work between the two but Expand is the debugging-and-
// admin path, not the request hot path, so the simpler shape wins.
// When p_max_leaf IS NULL the LIMIT/EXISTS both no-op (matching
// OpenFGA's unbounded behaviour) and the users_truncated field is
// omitted from the JSONB via the helper's CASE wrapper.
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
	whereSQL := strings.Join(where, " AND ")

	// Capped page: SELECT the OpenFGA-formatted user string from the
	// inner subquery, ORDERed for deterministic output, LIMITed to
	// p_max_leaf. When p_max_leaf IS NULL the LIMIT effectively
	// disappears via the IS NULL guard (Postgres treats LIMIT NULL as
	// "no limit"). Aggregating the OUTER SELECT (rather than inside the
	// subquery) preserves the ORDER + LIMIT semantics.
	usersExpr := fmt.Sprintf(
		"COALESCE((SELECT jsonb_agg(u) FROM (SELECT subject_type || ':' || subject_id AS u FROM %s WHERE %s ORDER BY subject_type, subject_id LIMIT p_max_leaf) capped), '[]'::jsonb)",
		tuplesTable, whereSQL)

	// Truncation probe: a single-row EXISTS over the same WHERE with
	// OFFSET p_max_leaf. When at least one row exists past the cap the
	// page was truncated. The p_max_leaf IS NOT NULL guard short-
	// circuits the probe when the cap is unset so OpenFGA-equivalent
	// callers don't pay for a redundant query.
	usersTruncatedExpr := fmt.Sprintf(
		"(p_max_leaf IS NOT NULL AND EXISTS (SELECT 1 FROM %s WHERE %s OFFSET p_max_leaf))",
		tuplesTable, whereSQL)

	return BuildExpandUsersLeafJSON(usersExpr, usersTruncatedExpr)
}
