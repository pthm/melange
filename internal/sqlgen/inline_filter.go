package sqlgen

import (
	"slices"
	"strings"
)

// filterInlineForCheck returns an InlineSQLData carrying only the closure and
// userset VALUES rows a check function for relation a actually references,
// instead of the whole model's rows.
//
// Check functions embed the inline VALUES in exactly one place — the
// userset-subject branch (buildUsersetSubjectChecks) — with these object_type
// constraints:
//   - closure `c` and userset `m`: object_type = a.ObjectType (constant)
//   - closure `subj_c`: object_type = t.subject_type, bounded at runtime to the
//     userset patterns' subject types
//
// So the closure rows needed are those for a.ObjectType plus every type that
// can appear as the userset subject t.subject_type; userset rows are those for
// a.ObjectType.
//
// The subject-side types are the userset patterns' SubjectType — these are
// load-bearing: subj_c keys on t.subject_type (e.g. "group" for a
// [group#member] pattern), and that "group" comes ONLY from the pattern
// loops. AllowedSubjectTypes is added as extra safety but is NOT sufficient
// alone: it holds the types a userset resolves *to* (e.g. "user"), not the
// userset object type itself, so removing the pattern loops would drop needed
// closure rows and break userset-subject checks.
//
// Dropping the rest leaves the generated SQL semantically identical while
// making the embedded VALUES independent of unrelated schema growth: a
// userset-subject check no longer scans closure rows for object types it can
// never reference.
func filterInlineForCheck(ix inlineIndex, a RelationAnalysis) InlineSQLData {
	closureTypes := map[string]bool{a.ObjectType: true}
	for _, t := range a.AllowedSubjectTypes {
		closureTypes[t] = true
	}
	for _, p := range a.UsersetPatterns {
		closureTypes[p.SubjectType] = true // load-bearing (see doc)
	}
	for _, p := range a.ClosureUsersetPatterns {
		closureTypes[p.SubjectType] = true // load-bearing (see doc)
	}

	return InlineSQLData{
		ClosureRows: ix.closure.filter(closureTypes),
		UsersetRows: ix.userset.filter(map[string]bool{a.ObjectType: true}),
	}
}

// filterInlineForList returns an InlineSQLData carrying only the closure and
// userset VALUES rows a list function (objects or subjects) for relation a can
// reference, instead of the whole model's rows.
//
// Every closure/userset VALUES lookup in the list block builders keys its
// object_type column on one of exactly three things (verified by reading every
// use site — grep ClosureTable/UsersetTable/TypedClosureValuesTable across
// list_objects_blocks*.go, list_subjects_blocks*.go, list_helpers.go):
//
//   - Lit(plan.ObjectType) — the relation's own object type (constant).
//   - Param("v_filter_type") / SubjectType — the subject-type filter/param. This
//     is only ever reached under a userset guard (position('#' in subject_id) > 0
//     / HasUserset), so the value can only be a userset subject type this
//     relation grants to (e.g. "group" for a [group#member] pattern).
//   - Col{link.subject_type} — a TTU parent tuple's subject type (the recursive
//     list_subjects TTU blocks), bounded by the TTU parent AllowedLinkingTypes.
//
// relationReferences(&a) over-approximates every "type.relation" edge a's
// generated functions can reference — same-type closure/implied/excluded,
// TTU parents (direct + closure + excluded + intersection), and userset targets
// (direct + closure + selfref + indirect-anchor). The TYPE half of that edge set
// therefore covers every userset subject type and every TTU parent type any
// closure/userset lookup can key on. Adding a.ObjectType and AllowedSubjectTypes
// is extra safety. The result is a safe superset: it can only keep extra rows,
// never drop a needed one, so the generated SQL is semantically identical while
// the embedded VALUES stop growing with unrelated schema.
func filterInlineForList(ix inlineIndex, a RelationAnalysis) InlineSQLData {
	keep := map[string]bool{a.ObjectType: true}
	for _, t := range a.AllowedSubjectTypes {
		keep[t] = true
	}
	for _, ref := range relationReferences(&a) {
		if i := strings.IndexByte(ref, '.'); i > 0 {
			keep[ref[:i]] = true
		}
	}
	return InlineSQLData{
		ClosureRows: ix.closure.filter(keep),
		UsersetRows: ix.userset.filter(keep),
	}
}

// inlineIndex is the whole-model InlineSQLData grouped by object type, built
// once per generate call so each relation's filter costs O(kept rows) rather
// than a scan (and a whole-model-sized allocation) of every row.
type inlineIndex struct {
	closure rowIndex
	userset rowIndex
}

func newInlineIndex(inline InlineSQLData) inlineIndex {
	return inlineIndex{closure: newRowIndex(inline.ClosureRows), userset: newRowIndex(inline.UsersetRows)}
}

// rowIndex maps each VALUES row's object_type (first column, a Lit) to its
// positions. Rows whose first column is not a plain Lit go in other and are
// always kept (conservatively), so a future non-literal row shape can never be
// silently dropped. Empty rows are dropped.
type rowIndex struct {
	rows   []ValuesRow
	byType map[string][]int
	other  []int
}

func newRowIndex(rows []ValuesRow) rowIndex {
	ix := rowIndex{rows: rows, byType: make(map[string][]int)}
	for i, r := range rows {
		if len(r) == 0 {
			continue
		}
		if lit, ok := r[0].(Lit); ok {
			ix.byType[string(lit)] = append(ix.byType[string(lit)], i)
		} else {
			ix.other = append(ix.other, i)
		}
	}
	return ix
}

// filter returns the rows whose object_type is in keep, plus every non-Lit
// row, in their original order (so the emitted VALUES are byte-stable).
func (ix rowIndex) filter(keep map[string]bool) []ValuesRow {
	if len(ix.rows) == 0 {
		return nil
	}
	pos := slices.Clone(ix.other)
	for t := range keep {
		pos = append(pos, ix.byType[t]...)
	}
	slices.Sort(pos)
	out := make([]ValuesRow, len(pos))
	for i, p := range pos {
		out[i] = ix.rows[p]
	}
	return out
}
