package sqlgen

import (
	"fmt"
	"strings"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// Foundations for the Explain SQL codegen.
//
// These helpers emit `jsonb_build_object` / `jsonb_build_array` SQL fragments
// shaped to deserialise directly into the Trace / Node / TupleRef / SubjectRef
// types declared in melange/trace.go. They are the single source of truth for
// the JSON wire format — every node emitted by explain_render.go must route
// through here so the contract cannot drift.
//
// All helpers return plain SQL strings rather than sqldsl.Expr because the
// jsonb_build_* call sites in PL/pgSQL accept variadic positional arguments
// and the alternation between literals and column references is irregular
// enough that an Expr tree adds noise without buying composition.

// TraceNodeType mirrors melange.NodeType. It is duplicated here rather than
// imported from the runtime module so lib/sqlgen continues to depend only on
// stdlib + sqldsl + analysis (the runtime module is built on top of sqlgen,
// not the other way around).
type TraceNodeType string

const (
	TraceNodeDirect       TraceNodeType = "direct"
	TraceNodeImplied      TraceNodeType = "implied"
	TraceNodeUserset      TraceNodeType = "userset"
	TraceNodeTTU          TraceNodeType = "ttu"
	TraceNodeUnion        TraceNodeType = "union"
	TraceNodeIntersection TraceNodeType = "intersection"
	TraceNodeExclusion    TraceNodeType = "exclusion"
	TraceNodeWildcard     TraceNodeType = "wildcard"
	TraceNodeCycle        TraceNodeType = "cycle"
	TraceNodeTruncated    TraceNodeType = "truncated"
)

// BuildEvidenceJSON emits a SQL fragment that constructs a TupleRef JSONB
// from a tuple-row alias's columns. Use inside SELECT lists or as a
// jsonb_agg() input when collecting evidence for a node.
//
// alias is the row alias (commonly "t" against `melange_tuples t`) and is
// required. Bare column references would clash with PL/pgSQL variables in
// scope and risk ambiguity in CTE and join contexts.
func BuildEvidenceJSON(alias string) string {
	if alias == "" {
		panic("sqlgen: BuildEvidenceJSON requires a non-empty alias")
	}
	prefix := alias + "."
	return "jsonb_build_object(" +
		"'subject_type', " + prefix + "subject_type, " +
		"'subject_id', " + prefix + "subject_id, " +
		"'relation', " + prefix + "relation, " +
		"'object_type', " + prefix + "object_type, " +
		"'object_id', " + prefix + "object_id)"
}

// BuildSubjectRefJSON emits a SubjectRef JSONB. typeExpr and idExpr are SQL
// expressions (column refs, literals, concatenations) for the type and id.
// Useful for Expand leaf enumeration.
func BuildSubjectRefJSON(typeExpr, idExpr string) string {
	return "jsonb_build_object('type', " + typeExpr + ", 'id', " + idExpr + ")"
}

// NodeJSONArgs carries the optional pieces of a Node JSONB. All fields are
// SQL expressions — column references, sqldsl.QuoteLiteral-quoted literals,
// jsonb_build_array(...) calls, etc. Empty fields are omitted from the
// emitted object so the JSON shape matches the `omitempty` Go tags.
type NodeJSONArgs struct {
	// Label is a SQL expression for the human-readable description.
	// Wrap literals with sqldsl.QuoteLiteral.
	Label string
	// Evidence, Children, Users should evaluate to JSONB arrays
	// (commonly jsonb_agg(...) or jsonb_build_array(...)).
	Evidence string
	Children string
	Users    string
	// Result is a SQL expression evaluating to a boolean. Populated on
	// Explain nodes; omit on safety-stop nodes (cycle / truncated).
	Result string
}

// BuildCycleNode emits a NodeCycle with a label identifying the cycle key.
// keyExpr is the SQL expression for the visited-key string.
func BuildCycleNode(keyExpr string) string {
	return BuildNodeJSON(TraceNodeCycle, NodeJSONArgs{Label: keyExpr})
}

// BuildTruncatedNode emits the universal "subtree omitted, p_max_nodes hit"
// sentinel. No label is required — the truncation reason is implicit.
func BuildTruncatedNode() string {
	return BuildNodeJSON(TraceNodeTruncated, NodeJSONArgs{})
}

// BuildNodeJSON emits a Node JSONB. Fields are emitted in the order they
// appear on melange.Node (type, label, evidence, children, users, result)
// so jsonb_build_object preserves a deterministic key order. Empty
// NodeJSONArgs fields are omitted from the output.
func BuildNodeJSON(nodeType TraceNodeType, a NodeJSONArgs) string {
	args := []string{"'type'", sqldsl.QuoteLiteral(string(nodeType))}
	if a.Label != "" {
		args = append(args, "'label'", a.Label)
	}
	if a.Evidence != "" {
		args = append(args, "'evidence'", a.Evidence)
	}
	if a.Children != "" {
		args = append(args, "'children'", a.Children)
	}
	if a.Users != "" {
		args = append(args, "'users'", a.Users)
	}
	if a.Result != "" {
		args = append(args, "'result'", a.Result)
	}
	return "jsonb_build_object(" + strings.Join(args, ", ") + ")"
}

// BuildObjectIdentExpr emits the canonical "<type>:<id>" concatenation
// shared by every trace envelope so the format never drifts.
func BuildObjectIdentExpr(typeExpr, idExpr string) string {
	return fmt.Sprintf("(%s || ':' || %s)", typeExpr, idExpr)
}
