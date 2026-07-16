package sqlgen

import "strings"

// closureCTEName is the name of the per-function CTE that holds the closure
// VALUES table once, instead of re-inlining the ~60-row literal at every use
// site (Finding 7). Block builders reference it via closureCTERef; the render
// layer materializes it once with hoistClosureCTE.
const closureCTEName = "closure"

// closureCTERef returns a table reference to the hoisted `closure` CTE with the
// given alias. It replaces an inline TypedClosureValuesTable so the constant
// closure table is emitted once per function rather than at every use site.
func closureCTERef(alias string) TableRef {
	return TableAs("", closureCTEName, alias)
}

// hoistClosureCTE prepends a single `closure(object_type, relation,
// satisfying_relation) AS (VALUES ...)` CTE to a wrapped list_subjects branch
// query, but only when that query actually references the CTE. The pagination
// wrapper always emits a leading `WITH base_results AS (`, so we splice the
// closure definition in right after `WITH `.
//
// Gating on the reference keeps this behavior-preserving: branches without any
// closure use (or relations with no closure rows) are returned unchanged, so no
// dead or empty-VALUES CTE is emitted.
func hoistClosureCTE(wrapped string, rows []ValuesRow) string {
	const marker = "WITH "
	if len(rows) == 0 || !strings.HasPrefix(wrapped, marker) {
		return wrapped
	}
	// Only hoist if the branch references the closure CTE (as `closure AS c`).
	// Anchor the match on an identifier boundary so it does not falsely fire on
	// `parent_closure AS ` (which ends with the same substring) and emit a dead CTE.
	if !referencesIdent(wrapped, closureCTEName+" AS ") {
		return wrapped
	}

	valuesParts := make([]string, len(rows))
	for i, row := range rows {
		valuesParts[i] = row.SQL()
	}
	closureCTE := closureCTEName + "(object_type, relation, satisfying_relation) AS (\n" +
		"        VALUES " + strings.Join(valuesParts, ", ") + "\n" +
		"    ),\n    "

	return marker + closureCTE + strings.TrimPrefix(wrapped, marker)
}

// referencesIdent reports whether needle occurs in s at an identifier boundary,
// i.e. not as the suffix of a longer identifier. Used so a "closure AS " probe
// is not satisfied by "parent_closure AS ".
func referencesIdent(s, needle string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return false
		}
		pos := from + i
		if pos == 0 || !isIdentByte(s[pos-1]) {
			return true
		}
		from = pos + len(needle)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
