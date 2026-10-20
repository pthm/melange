package parser_test

// Semantic oracle for the parser's flattening. Each relation is evaluated
// twice over every assignment of the base tuples: once by walking OpenFGA's
// own userset tree, and once by interpreting melange's flat
// RelationDefinition the way codegen does. A mismatch is information the
// parser lost (an over- or under-grant). No database needed.

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/openfga/language/pkg/go/transformer"

	"github.com/pthm/melange/melange"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// oracleBits are the independent facts a tuple assignment can set: a direct
// tuple on each base relation, a wildcard grant, the parent folder's viewer,
// and a direct tuple on the relation under test ("self").
var oracleBits = []string{"a", "b", "c", "d", "e", "everyone", "folder.viewer", "self"}

type oracle struct {
	tree map[string]*openfgav1.Userset
	flat map[string]schema.RelationDefinition
	env  map[string]bool
}

const oracleRelation = "p"

func (o *oracle) direct(rel string) bool {
	if rel == oracleRelation {
		return o.env["self"]
	}
	return o.env[rel]
}

func (o *oracle) evalTree(rel string, us *openfgav1.Userset) bool {
	switch v := us.GetUserset().(type) {
	case *openfgav1.Userset_This:
		return o.direct(rel)
	case *openfgav1.Userset_ComputedUserset:
		r := v.ComputedUserset.GetRelation()
		return o.evalTree(r, o.tree[r])
	case *openfgav1.Userset_TupleToUserset:
		return o.env["folder."+v.TupleToUserset.GetComputedUserset().GetRelation()]
	case *openfgav1.Userset_Union:
		for _, c := range v.Union.GetChild() {
			if o.evalTree(rel, c) {
				return true
			}
		}
		return false
	case *openfgav1.Userset_Intersection:
		for _, c := range v.Intersection.GetChild() {
			if !o.evalTree(rel, c) {
				return false
			}
		}
		return true
	case *openfgav1.Userset_Difference:
		return o.evalTree(rel, v.Difference.GetBase()) && !o.evalTree(rel, v.Difference.GetSubtract())
	}
	panic(fmt.Sprintf("unknown userset %T", us.GetUserset()))
}

// evalGroup mirrors analysis: a group member named after its own relation is
// the relation's direct tuples, and every exclusion keyed on a member applies.
func (o *oracle) evalGroup(owner string, g schema.IntersectionGroup) bool {
	for _, r := range g.Relations {
		if r == owner {
			if !o.direct(owner) {
				return false
			}
		} else if !o.evalFlat(r) {
			return false
		}
	}
	for _, p := range g.ParentRelations {
		if !o.env["folder."+p.Relation] {
			return false
		}
	}
	for _, excluded := range g.Exclusions {
		if slices.ContainsFunc(excluded, o.evalFlat) {
			return false
		}
	}
	return true
}

func (o *oracle) evalFlat(rel string) bool {
	d := o.flat[rel]
	// Mirrors computeHasStandaloneAccess: direct grants stand alone unless an
	// intersection group uses this relation's own tuples and the relation has
	// no implied or parent path (either of which makes all of its tuple
	// lookups, its own included, standalone).
	thisInGroup := slices.ContainsFunc(d.IntersectionGroups, func(g schema.IntersectionGroup) bool {
		return slices.Contains(g.Relations, rel)
	})
	standalone := !thisInGroup || len(d.ImpliedBy) > 0 || len(d.ParentRelations) > 0
	allowed := len(d.SubjectTypeRefs) > 0 && standalone && o.direct(rel)
	for _, r := range d.ImpliedBy {
		allowed = allowed || o.evalFlat(r)
	}
	for _, p := range d.ParentRelations {
		allowed = allowed || o.env["folder."+p.Relation]
	}
	for _, g := range d.IntersectionGroups {
		allowed = allowed || o.evalGroup(rel, g)
	}
	if !allowed {
		return false
	}
	if slices.ContainsFunc(d.ExcludedRelations, o.evalFlat) {
		return false
	}
	for _, p := range d.ExcludedParentRelations {
		if o.env["folder."+p.Relation] {
			return false
		}
	}
	for _, g := range d.ExcludedIntersectionGroups {
		if o.evalGroup(rel, g) {
			return false
		}
	}
	return true
}

func oracleSchema(expr string) string {
	var sb strings.Builder
	sb.WriteString("model\n  schema 1.1\ntype user\ntype folder\n  relations\n    define viewer: [user]\ntype document\n  relations\n    define parent: [folder]\n")
	for _, r := range []string{"a", "b", "c", "d", "e"} {
		fmt.Fprintf(&sb, "    define %s: [user]\n", r)
	}
	sb.WriteString("    define everyone: [user:*]\n")
	fmt.Fprintf(&sb, "    define %s: %s\n", oracleRelation, expr)
	return sb.String()
}

// checkOracle parses expr and compares both evaluations over every tuple
// assignment. It returns the parse error, or fails t on the first mismatch.
func checkOracle(t *testing.T, expr string) error {
	t.Helper()
	dsl := oracleSchema(expr)
	model, err := transformer.TransformDSLToProto(dsl)
	if err != nil {
		t.Fatalf("%s: OpenFGA rejects the schema: %v", expr, err)
	}
	types, err := parser.ParseSchemaString(dsl)
	if err != nil {
		return err
	}
	o := &oracle{flat: map[string]schema.RelationDefinition{}}
	for _, td := range model.GetTypeDefinitions() {
		if td.GetType() == "document" {
			o.tree = td.GetRelations()
		}
	}
	for _, td := range types {
		if td.Name == "document" {
			for _, r := range td.Relations {
				o.flat[r.Name] = r
			}
		}
	}
	for mask := 0; mask < 1<<len(oracleBits); mask++ {
		o.env = map[string]bool{}
		var set []string
		for b, name := range oracleBits {
			if mask&(1<<b) != 0 {
				o.env[name] = true
				set = append(set, name)
			}
		}
		want, got := o.evalTree(oracleRelation, o.tree[oracleRelation]), o.evalFlat(oracleRelation)
		if want != got {
			t.Errorf("%s: with {%s} OpenFGA says %v, melange says %v", expr, strings.Join(set, ", "), want, got)
			return nil
		}
	}
	return nil
}

func TestFlatteningMatchesOpenFGA(t *testing.T) {
	shapes := []string{
		"((a but not b) but not c) and d",
		"(a but not (b or c)) and d",
		"(a but not (b and c)) and d",
		"(viewer from parent but not b) and d",
		"([user] but not b) and d",
		"((a or e) but not b) and d",
		"d and (a and (b or c))",
		"(a but not b) and (a but not c)",
		"d and (a or (b and c))",
		"d and (a or (b but not c))",
		"d and ((b but not c) or (e but not c))",
		"([user] or a) and d",
		"a or (b but not c)",
		"(a but not c) or (b but not e)",
		"a or ((b and d) but not c)",
		"a but not ((b but not c) but not d)",
		"a but not (b and (c or d))",
		"a but not (b and (c and (d or e)))",
		"a but not (b and (c or (d and e)))",
		"a but not ((b and c) or (d but not e))",
		"[user] and (b but not c) and (b but not d)",
		"(a and b) or (c but not d)",
		"((everyone but not b) but not c) and d",
		"a but not (b or (c and d))",
		"(a and b) but not c",
		"d and ((a and b) but not c)",
		"([user] and a) but not b",
		"([user] and a) or c",
		// folder defines viewer only; `a from parent` would be a dangling
		// reference, which the parser rejects.
		"([user] and viewer from parent) or c",
		"(([user] and a) but not b) or viewer from parent",
		"([user] but not b) or a",
		"([user] but not (c or c)) or a",
		"([user] but not b) or viewer from parent",
		"[user] or (a but not b)",
		"([user] or a) but not b",
		"[user] but not viewer from parent",
		"a but not (b or viewer from parent)",
	}
	for _, expr := range shapes {
		if err := checkOracle(t, expr); err != nil {
			t.Errorf("%s: unexpectedly rejected: %v", expr, err)
		}
	}
}

// Excluding a tuple-to-userset (or direct tuples) inside a compound
// expression has no flat encoding; it must be rejected, never dropped.
func TestFlatteningRejectsUnrepresentable(t *testing.T) {
	for _, expr := range []string{
		"(a but not viewer from parent) and d",
		"a but not (b but not viewer from parent)",
		"a but not (viewer from parent but not b)",
	} {
		err := checkOracle(t, expr)
		if err == nil {
			t.Errorf("%s: expected rejection", expr)
			continue
		}
		if !melange.IsInvalidSchemaErr(err) {
			t.Errorf("%s: expected ErrInvalidSchema, got %v", expr, err)
		}
	}
}

// Random nested expressions: every accepted one must match OpenFGA exactly.
func TestFlatteningMatchesOpenFGARandom(t *testing.T) {
	atoms := []string{"a", "b", "c", "d", "e", "everyone", "viewer from parent"}
	ops := []string{" or ", " and ", " but not "}
	var gen func(r *rand.Rand, depth int) string
	gen = func(r *rand.Rand, depth int) string {
		if depth == 0 || r.Intn(3) == 0 {
			return atoms[r.Intn(len(atoms))]
		}
		op := ops[r.Intn(len(ops))]
		return "(" + gen(r, depth-1) + op + gen(r, depth-1) + ")"
	}
	r := rand.New(rand.NewSource(1))
	n := 2000
	if testing.Short() {
		n = 200
	}
	for range n {
		expr := gen(r, 3)
		if !strings.HasPrefix(expr, "(") {
			continue // a bare atom
		}
		expr = expr[1 : len(expr)-1]
		// A direct type restriction is only valid as the leftmost operand.
		if r.Intn(3) == 0 {
			expr = "[user]" + ops[r.Intn(len(ops))] + "(" + expr + ")"
		}
		_ = checkOracle(t, expr) // rejections are allowed here
		if t.Failed() {
			return
		}
	}
}
