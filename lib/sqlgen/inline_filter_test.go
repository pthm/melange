package sqlgen

import "testing"

func closureRow(objectType, relation, satisfying string) ValuesRow {
	return ValuesRow{Lit(objectType), Lit(relation), Lit(satisfying)}
}

func usersetRow(objectType, relation, subjectType, subjectRelation string) ValuesRow {
	return ValuesRow{Lit(objectType), Lit(relation), Lit(subjectType), Lit(subjectRelation)}
}

func objectTypesOf(rows []ValuesRow) map[string]bool {
	got := map[string]bool{}
	for _, r := range rows {
		got[string(r[0].(Lit))] = true
	}
	return got
}

func TestFilterInlineForCheck_KeepsObjectAndSubjectTypes(t *testing.T) {
	inline := InlineSQLData{
		ClosureRows: []ValuesRow{
			closureRow("element", "view", "view"),
			closureRow("group", "member", "member"),        // userset subject type
			closureRow("workspace", "view", "view"),        // allowed subject type
			closureRow("aaa", "rel001", "rel001"),          // unrelated → drop
			closureRow("organization", "manage", "manage"), // unrelated → drop
		},
		UsersetRows: []ValuesRow{
			usersetRow("element", "view", "group", "member"),
			usersetRow("workspace", "view", "organization", "view"), // other object type → drop
		},
	}
	a := RelationAnalysis{
		ObjectType:          "element",
		Relation:            "view",
		AllowedSubjectTypes: []string{"user", "group", "workspace"},
		UsersetPatterns: []UsersetPattern{
			{SubjectType: "group", SubjectRelation: "member"},
			{SubjectType: "workspace", SubjectRelation: "view"},
		},
	}

	out := filterInlineForCheck(inline, a)

	gotClosure := objectTypesOf(out.ClosureRows)
	for _, want := range []string{"element", "group", "workspace"} {
		if !gotClosure[want] {
			t.Errorf("closure filter dropped needed object type %q", want)
		}
	}
	for _, unwant := range []string{"aaa", "organization"} {
		if gotClosure[unwant] {
			t.Errorf("closure filter kept unrelated object type %q", unwant)
		}
	}

	gotUserset := objectTypesOf(out.UsersetRows)
	if !gotUserset["element"] {
		t.Errorf("userset filter dropped the object type's own rows")
	}
	if gotUserset["workspace"] {
		t.Errorf("userset filter kept another object type's rows (only object side needed)")
	}
}

func TestFilterInlineForList_KeepsReferencedTypesDropsUnrelated(t *testing.T) {
	inline := InlineSQLData{
		ClosureRows: []ValuesRow{
			closureRow("element", "view", "view"),       // own object type
			closureRow("group", "member", "member"),      // userset subject type
			closureRow("folder", "view", "view"),         // TTU parent linking type
			closureRow("workspace", "view", "view"),      // allowed subject type
			closureRow("aaa", "rel001", "rel001"),        // unrelated → drop
			closureRow("organization", "own", "own"),     // unrelated → drop
		},
		UsersetRows: []ValuesRow{
			usersetRow("element", "view", "group", "member"),
			usersetRow("group", "member", "user", ""),          // userset subject type kept (list_objects keys on it)
			usersetRow("aaa", "rel001", "user", ""),            // unrelated → drop
		},
	}
	a := RelationAnalysis{
		ObjectType:          "element",
		Relation:            "view",
		AllowedSubjectTypes: []string{"user", "workspace"},
		UsersetPatterns: []UsersetPattern{
			{SubjectType: "group", SubjectRelation: "member"},
		},
		ParentRelations: []ParentRelationInfo{
			{Relation: "view", LinkingRelation: "parent", AllowedLinkingTypes: []string{"folder"}},
		},
	}

	out := filterInlineForList(inline, a)

	gotClosure := objectTypesOf(out.ClosureRows)
	for _, want := range []string{"element", "group", "folder", "workspace"} {
		if !gotClosure[want] {
			t.Errorf("closure filter dropped needed object type %q", want)
		}
	}
	for _, unwant := range []string{"aaa", "organization"} {
		if gotClosure[unwant] {
			t.Errorf("closure filter kept unrelated object type %q", unwant)
		}
	}

	gotUserset := objectTypesOf(out.UsersetRows)
	for _, want := range []string{"element", "group"} {
		if !gotUserset[want] {
			t.Errorf("userset filter dropped needed object type %q", want)
		}
	}
	if gotUserset["aaa"] {
		t.Errorf("userset filter kept unrelated object type %q", "aaa")
	}
}

func TestFilterRowsByObjectType_KeepsNonLitRows(t *testing.T) {
	// A row whose first column is not a plain Lit must be kept conservatively.
	rows := []ValuesRow{
		{Raw("something_dynamic"), Lit("r"), Lit("s")},
		closureRow("keep", "r", "s"),
		closureRow("drop", "r", "s"),
	}
	out := filterRowsByObjectType(rows, map[string]bool{"keep": true})
	if len(out) != 2 {
		t.Fatalf("expected 2 rows (non-Lit + keep), got %d", len(out))
	}
}
