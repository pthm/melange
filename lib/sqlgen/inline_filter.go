package sqlgen

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
func filterInlineForCheck(inline InlineSQLData, a RelationAnalysis) InlineSQLData {
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
		ClosureRows: filterRowsByObjectType(inline.ClosureRows, closureTypes),
		UsersetRows: filterRowsByObjectType(inline.UsersetRows, map[string]bool{a.ObjectType: true}),
	}
}

// filterRowsByObjectType keeps only VALUES rows whose first column (object_type,
// a Lit) is in keep. Rows whose first column is not a plain Lit are kept
// (conservatively) so a future non-literal row shape can never be silently
// dropped.
func filterRowsByObjectType(rows []ValuesRow, keep map[string]bool) []ValuesRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]ValuesRow, 0, len(rows))
	for _, r := range rows {
		if len(r) == 0 {
			continue
		}
		lit, ok := r[0].(Lit)
		if !ok || keep[string(lit)] {
			out = append(out, r)
		}
	}
	return out
}
