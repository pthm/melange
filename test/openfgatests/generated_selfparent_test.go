package openfgatests_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	openfgav1 "github.com/openfga/api/proto/openfga/v1"
	"github.com/stretchr/testify/require"
)

// TestOpenFGA_GeneratedSelfParent is TestOpenFGA_GeneratedNested with a
// same-type parent (document.parent: [document]). Parent lookups may target
// any relation, including the one being defined, so the models mix one-hop
// TTUs ("x: [user] or y from parent"), self-recursion ("x from parent" in
// x), and recursion through other relations. The cross-type generators
// cannot produce these shapes: list strategies used to decide recursion on
// the linking type alone, which only a same-type parent exposes.
//
// Each sample is a chain dg -> dp -> d, compared at the leaf d (where
// over-reporting list_subjects shows) and over every document for
// list_objects (where under-reporting shows).
func TestOpenFGA_GeneratedSelfParent(t *testing.T) {
	if testing.Short() {
		t.Skip("generated differential testing requires PostgreSQL")
	}
	seed := algebraEnvInt(t, "MELANGE_SELFPARENT_SEED", 1)
	models := algebraEnvInt(t, "MELANGE_SELFPARENT_MODELS", 12)
	require.Positive(t, models)
	accepted := 0
	for i := 0; i < models*10 && accepted < models; i++ {
		modelSeed := seed + i
		if !t.Run(fmt.Sprintf("seed_%d", modelSeed), func(t *testing.T) {
			// multi comes from the program as in FuzzSelfParentAlgebra, so a
			// failure's printed input replays the same model there.
			program := algebraProgram(int64(modelSeed))
			if runSelfParentDifferential(t, program, program[0]%2 == 1) {
				accepted++
			}
		}) {
			return // Report one reproducible counterexample.
		}
	}
	require.Equal(t, models, accepted, "not enough OpenFGA-valid generated models")
}

// FuzzSelfParentAlgebra lets Go mutate the generator's choices. Run with
// -parallel=1 to bound database creation.
func FuzzSelfParentAlgebra(f *testing.F) {
	if testing.Short() {
		f.Skip("generated differential testing requires PostgreSQL")
	}
	for seed := int64(1); seed <= 4; seed++ {
		f.Add(algebraProgram(seed))
	}
	f.Fuzz(func(t *testing.T, program []byte) {
		if !runSelfParentDifferential(t, program, len(program) > 0 && program[0]%2 == 1) {
			t.Skip("outside the OpenFGA-valid model domain")
		}
	})
}

// selfParentExpr is nestedGen.expr with parent lookups over pool plus self,
// the relation being defined, through any of links.
func (g *nestedGen) selfParentExpr(pool []string, self string, links []string, depth int, leftmost bool) string {
	if depth == 0 || g.take()%3 == 0 {
		if leftmost && g.take()%5 == 0 {
			return "[user]"
		}
		if g.take()%3 == 0 {
			targets := slices.Concat(pool, []string{self, self})
			return targets[g.take()%len(targets)] + " from " + links[g.take()%len(links)]
		}
		return pool[g.take()%len(pool)]
	}
	op := []string{" or ", " or ", " and ", " but not "}[g.take()%4]
	return "(" + g.selfParentExpr(pool, self, links, depth-1, leftmost) + op + g.selfParentExpr(pool, self, links, depth-1, false) + ")"
}

// selfParentMultiTypes adds a second same-type linking relation that can also
// point at another type: parent_closure walks every linking relation at once,
// and per-type splits of one linking relation are easy to get wrong.
const selfParentMultiTypes = `type folder
  relations
    define a: [user]
    define b: [user, user:*]
    define v: [user]
`

// runSelfParentDifferential with multi also gives document a second linking
// relation, owner: [document, folder].
func runSelfParentDifferential(t *testing.T, program []byte, multi bool) bool {
	t.Helper()
	g := &nestedGen{program: program}
	links := []string{"parent"}
	var b strings.Builder
	b.WriteString(`model
  schema 1.1
type user
type group
  relations
    define member: [user, user:*]
`)
	if multi {
		links = append(links, "owner")
		b.WriteString(selfParentMultiTypes)
	}
	b.WriteString(`type document
  relations
    define parent: [document]
    define a: [user]
    define b: [user, user:*]
    define c: [user, group#member]
    define v: [user] or v from parent
`)
	if multi {
		b.WriteString("    define owner: [document, folder]\n")
	}
	count := 3 + g.take()%3
	direct := []string{"a", "b", "c", "v"}
	pool := append(make([]string, 0, 4+count), "a", "b", "c", "v")
	relations := append(make([]string, 0, 4+count), "a", "b", "c", "v")
	for i := range count {
		name := fmt.Sprintf("r%d", i)
		e := g.selfParentExpr(pool, name, links, 3, true)
		e = strings.TrimSuffix(strings.TrimPrefix(e, "("), ")")
		if strings.Contains(e, "[user]") {
			direct = append(direct, name)
		}
		fmt.Fprintf(&b, "    define %s: %s\n", name, e)
		pool = append(pool, name)
		relations = append(relations, name)
	}
	dsl := b.String()
	t.Logf("model choices: %x\n%s", program, dsl)

	var all []*openfgav1.TupleKey
	ids := make([]string, nestedSamples)
	for i := range ids {
		id := fmt.Sprint(i)
		ids[i] = id
		leaf, mid, top, grp := "document:d"+id, "document:dp"+id, "document:dg"+id, "group:g"+id
		candidates := []*openfgav1.TupleKey{
			{User: "user:alice", Relation: "member", Object: grp},
			{User: "user:*", Relation: "member", Object: grp},
			{User: "user:*", Relation: "b", Object: mid},
			{User: grp + "#member", Relation: "c", Object: top},
		}
		for _, user := range []string{"user:alice", "user:bob"} {
			for _, rel := range direct {
				for _, obj := range []string{leaf, mid, top} {
					candidates = append(candidates, &openfgav1.TupleKey{User: user, Relation: rel, Object: obj})
				}
			}
		}
		// The chain is always present: the point is what flows down it.
		all = append(all,
			&openfgav1.TupleKey{User: mid, Relation: "parent", Object: leaf},
			&openfgav1.TupleKey{User: top, Relation: "parent", Object: mid})
		if multi {
			// owner crosses the parent chain (leaf -> top skips mid) and also
			// reaches a folder, so walks over both links can mix.
			fld := "folder:f" + id
			all = append(all,
				&openfgav1.TupleKey{User: top, Relation: "owner", Object: leaf},
				&openfgav1.TupleKey{User: fld, Relation: "owner", Object: mid})
			for _, user := range []string{"user:alice", "user:bob"} {
				for _, rel := range []string{"a", "b", "v"} {
					candidates = append(candidates, &openfgav1.TupleKey{User: user, Relation: rel, Object: fld})
				}
			}
		}
		for _, tk := range candidates {
			if g.take()%4 == 0 {
				all = append(all, tk)
			}
		}
	}
	return compareNested(t, dsl, relations, ids, all, g.exposure(relations), fmt.Sprintf("FuzzSelfParentAlgebra input: %x", program))
}
