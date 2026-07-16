package test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/test/testutil"
)

// TestKitchenSink_GeneratorCoverage proves — DB-free, at analysis time — that the
// kitchen-sink schema actually exercises the whole generator surface: every list
// strategy, every relation feature, and the bug-prone feature combinations. If a
// future edit to the schema drops one of these shapes, this test fails naming the
// gap, so the schema can't silently stop covering a code path.
func TestKitchenSink_GeneratorCoverage(t *testing.T) {
	analyses, err := testutil.AnalyzeKitchenSink()
	require.NoError(t, err)
	require.NotEmpty(t, analyses)

	strategies := map[string]int{} // strategy -> count of list-generatable relations
	features := map[string]int{}   // feature flag -> count of relations
	combos := map[string]int{}     // Features.String() -> count

	for _, a := range analyses {
		f := a.Features
		for name, on := range map[string]bool{
			"Direct": f.HasDirect, "Implied": f.HasImplied, "Wildcard": f.HasWildcard,
			"Userset": f.HasUserset, "Recursive": f.HasRecursive,
			"Exclusion": f.HasExclusion, "Intersection": f.HasIntersection,
		} {
			if on {
				features[name]++
			}
		}
		combos[f.String()]++
		if a.Capabilities.ListAllowed {
			strategies[a.ListStrategy.String()]++
		}
	}

	// Every list strategy must be represented (DepthExceeded is deliberately out
	// of scope — it needs an artificial >=25-deep userset chain, an error path
	// better covered by a focused YAML test).
	for _, s := range []string{"Direct", "Userset", "Recursive", "Intersection", "SelfRefUserset", "Composed"} {
		require.Positivef(t, strategies[s], "no list-generatable relation uses the %s strategy", s)
	}

	// Every individual feature must appear somewhere.
	for _, feat := range []string{"Direct", "Implied", "Wildcard", "Userset", "Recursive", "Exclusion", "Intersection"} {
		require.Positivef(t, features[feat], "no relation exercises the %s feature", feat)
	}

	// The bug-prone combinations must each appear in at least one relation. These
	// are the shapes recent bugs lived in — the whole point of the schema.
	requiredCombos := []struct {
		name string
		pred func(compiler.RelationAnalysis) bool
	}{
		{"Recursive+Intersection", func(a compiler.RelationAnalysis) bool { return a.Features.HasRecursive && a.Features.HasIntersection }},
		{"Wildcard+Exclusion", func(a compiler.RelationAnalysis) bool { return a.Features.HasWildcard && a.Features.HasExclusion }},
		{"Userset+Recursive", func(a compiler.RelationAnalysis) bool { return a.Features.HasUserset && a.Features.HasRecursive }},
		{"Recursive+Exclusion", func(a compiler.RelationAnalysis) bool { return a.Features.HasRecursive && a.Features.HasExclusion }},
		{"Userset+Intersection", func(a compiler.RelationAnalysis) bool { return a.Features.HasUserset && a.Features.HasIntersection }},
		{"Wildcard+Recursive (wildcard via TTU)", func(a compiler.RelationAnalysis) bool { return a.Features.HasWildcard && a.Features.HasRecursive }},
		{"Implied+Recursive", func(a compiler.RelationAnalysis) bool { return a.Features.HasImplied && a.Features.HasRecursive }},
		{"Direct+Userset+Recursive (the #12 class)", func(a compiler.RelationAnalysis) bool {
			return a.Features.HasDirect && a.Features.HasUserset && a.Features.HasRecursive
		}},
	}
	for _, c := range requiredCombos {
		require.Truef(t, slices.ContainsFunc(analyses, c.pred), "no relation exercises the %s combination", c.name)
	}

	// Emit the coverage report so `go test -v` shows exactly what's exercised.
	t.Logf("list strategies: %s", fmtCounts(strategies))
	t.Logf("features: %s", fmtCounts(features))
	t.Logf("%d distinct feature combinations:\n%s", len(combos), fmtCounts(combos))
}

// TestKitchenSink_TupleSubjectShapes proves — DB-free — that the tuple generator
// still emits the non-plain query-subject shapes the benchmarks and differential
// subset checks depend on: userset-typed subjects (subject_id contains '#'),
// service_account subjects, and wildcard ('*') subjects. If a future generator
// edit drops one, the corresponding bench would silently go vacuous (or Fatal on
// setup); this fails first, naming the gap.
func TestKitchenSink_TupleSubjectShapes(t *testing.T) {
	tuples := testutil.GenerateKitchenSinkTuples(testutil.KitchenSinkScaleSmall)
	require.NotEmpty(t, tuples)

	var userset, svcAcct, wildcard, usersetOrgMember, svcGroupMember int
	for _, tp := range tuples {
		if strings.Contains(tp.SubjectID, "#") {
			userset++
			if tp.SubjectType == "group" && tp.Relation == "member" && tp.ObjectType == "organization" {
				usersetOrgMember++
			}
		}
		if tp.SubjectType == "service_account" && tp.SubjectID != "*" {
			svcAcct++
			if tp.Relation == "member" && tp.ObjectType == "group" {
				svcGroupMember++
			}
		}
		if tp.SubjectID == "*" {
			wildcard++
		}
	}
	require.Positive(t, userset, "no userset-typed subjects — F4 bench would be vacuous")
	require.Positive(t, svcAcct, "no service_account subjects — ServiceAccount bench would be vacuous")
	require.Positive(t, wildcard, "no wildcard subjects — Wildcard bench/exclusion coverage lost")
	require.Positivef(t, usersetOrgMember, "no group#member -> organization member tuple — the F4 userset-subject bench pair (setupKitchenSinkBench) would Fatal")
	require.Positivef(t, svcGroupMember, "no service_account -> group member tuple — the ServiceAccount bench pair would Fatal")
}

// TestKitchenSink_NoDeadHasWildcardCTE guards against dead codegen: the
// has_wildcard CTE in list_subjects functions is read only by the wildcard
// completion tail, which is emitted only for wildcard-reaching relations. Every
// emitted CTE must therefore have a matching CROSS JOIN reference — an
// unreferenced ordinary CTE is dead SQL (the same class as the removed
// depth_check preamble). DB-free.
func TestKitchenSink_NoDeadHasWildcardCTE(t *testing.T) {
	fns, err := testutil.KitchenSinkListSubjectsSQL()
	require.NoError(t, err)
	combined := strings.Join(fns, "\n")

	defs := strings.Count(combined, "has_wildcard AS (")
	refs := strings.Count(combined, "CROSS JOIN has_wildcard")
	require.Positive(t, refs, "expected some wildcard-reaching list_subjects functions to reference has_wildcard")
	require.Equalf(t, refs, defs,
		"has_wildcard CTE def/ref mismatch: %d definitions vs %d references — an unreferenced CTE is dead codegen", defs, refs)
}

func fmtCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-45s %d\n", k, m[k])
	}
	return b.String()
}
