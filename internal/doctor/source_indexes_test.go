package doctor

import (
	"slices"
	"testing"
)

func TestClassifyRef(t *testing.T) {
	for _, tc := range []struct {
		expr          string
		literal, cast bool
		column        string
		ok            bool
	}{
		{expr: `'org'::text`, literal: true, ok: true},
		{expr: `'it''s'`, literal: true, ok: true},
		{expr: `user_id::text`, column: "user_id", cast: true, ok: true},
		{expr: `trails.organization_id::text`, column: "organization_id", cast: true, ok: true},
		{expr: `(t.id)::text`, column: "id", cast: true, ok: true},
		{expr: `role`, column: "role", ok: true},
		{expr: `"Role"`, column: "Role", ok: true},
		{expr: `user_id::uuid`, ok: false},
		{expr: `CASE WHEN banned THEN '*'::text ELSE user_id::text END`, ok: false},
		{expr: `lower(email)`, ok: false},
	} {
		literal, column, cast, ok := classifyRef(tc.expr)
		if literal != tc.literal || column != tc.column || cast != tc.cast || ok != tc.ok {
			t.Errorf("classifyRef(%q) = %v %q %v %v, want %v %q %v %v", tc.expr, literal, column, cast, ok, tc.literal, tc.column, tc.cast, tc.ok)
		}
	}
}

func TestNormalizedIndexKeys(t *testing.T) {
	for def, want := range map[string][]string{
		"CREATE INDEX a ON public.t USING btree (org_id, ((user_id)::text))":                {"org_id", "user_id::text"},
		`CREATE UNIQUE INDEX t_pkey ON public.t USING btree ("Id")`:                         {"id"},
		"CREATE INDEX p ON public.t USING btree (org_id, user_id) WHERE (role IS NOT NULL)": {"org_id", "user_id"},
	} {
		if got := normalizedIndexKeys(def); !slices.Equal(got, want) {
			t.Errorf("normalizedIndexKeys(%q) = %q, want %q", def, got, want)
		}
	}
}

func TestObjectLookupCoverage(t *testing.T) {
	keys := []sourceKey{{Column: "org_id", Expr: true}, {Column: "role"}, {Column: "user_id"}}
	for name, tc := range map[string]struct {
		defs []string
		want bool
	}{
		"exact":                 {[]string{"CREATE INDEX a ON t USING btree (((org_id)::text), role, user_id)"}, true},
		"subject right after":   {[]string{"CREATE INDEX a ON t USING btree (((org_id)::text), user_id)"}, true},
		"object only":           {[]string{"CREATE INDEX a ON t USING btree (((org_id)::text))"}, false},
		"plain object, no cast": {[]string{"CREATE INDEX a ON t USING btree (org_id, role, user_id)"}, false},
		"partial":               {[]string{"CREATE INDEX a ON t USING btree (((org_id)::text), user_id) WHERE (role = 'x'::text)"}, false},
	} {
		if got := coversObjectLookup(keys, tc.defs); got != tc.want {
			t.Errorf("%s: coversObjectLookup = %v, want %v", name, got, tc.want)
		}
	}
	if !uniqueOn(sourceKey{Column: "id", Expr: true}, []string{"CREATE UNIQUE INDEX t_pkey ON t USING btree (id)"}) {
		t.Error("a primary key on the column makes it unique, cast or not")
	}
	if uniqueOn(sourceKey{Column: "id"}, []string{"CREATE UNIQUE INDEX u ON t USING btree (id, other)"}) {
		t.Error("a composite unique index doesn't make the column unique")
	}
}

func TestLeadsWithSubject(t *testing.T) {
	plain := subjectLookupKey{Key: sourceKey{Column: "user_id"}}
	relabel := subjectLookupKey{Key: sourceKey{Column: "user_id"}, Relabel: true}
	converted := subjectLookupKey{Key: sourceKey{Column: "user_id", Expr: true}}
	for name, tc := range map[string]struct {
		key  subjectLookupKey
		def  string
		want bool
	}{
		"plain, leading":            {plain, "CREATE INDEX a ON t USING btree (user_id, org_id)", true},
		"plain, behind another key": {plain, "CREATE INDEX a ON t USING btree (org_id, user_id)", false},
		"relabel, plain index":      {relabel, "CREATE INDEX a ON t USING btree (user_id)", true},
		"relabel, expression":       {relabel, "CREATE INDEX a ON t USING btree (((user_id)::text))", true},
		"converted, expression":     {converted, "CREATE INDEX a ON t USING btree (((user_id)::text), org_id)", true},
		"converted, plain index":    {converted, "CREATE INDEX a ON t USING btree (user_id)", false},
		"partial":                   {plain, "CREATE INDEX a ON t USING btree (user_id) WHERE (org_id IS NOT NULL)", false},
	} {
		if got := leadsWithSubject(tc.key, []string{tc.def}); got != tc.want {
			t.Errorf("%s: leadsWithSubject = %v, want %v", name, got, tc.want)
		}
	}
}
