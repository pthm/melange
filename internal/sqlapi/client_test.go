package sqlapi

import (
	"slices"
	"strings"
	"testing"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/internal/sqlgen"
	"github.com/pthm/melange/internal/version"
	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/parser"
	"github.com/pthm/melange/pkg/schema"
)

// clientTestSchema has a parent-key function (document#can_view by parent)
// and role relations the can_* permissions resolve through.
const clientTestSchema = `model
  schema 1.1

type user

type folder
  relations
    define viewer: [user]

type document
  relations
    define parent: [folder]
    define owner: [user]
    define editor: [user] or owner
    define can_view: viewer from parent
    define can_edit: editor
`

func clientTypes(t *testing.T) []schema.TypeDefinition {
	t.Helper()
	types, err := parser.ParseSchemaString(clientTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	return types
}

// clientNamesFor returns the names a client generated under local, with
// relations as generate.client.relations (nil inherits sql.relations).
func clientNamesFor(t *testing.T, types []schema.TypeDefinition, local sqlgen.Exposure, relations *sqlgen.RelationPatterns) ClientNames {
	t.Helper()
	m, err := clientgen.Resolve(types, &clientgen.Config{Exposure: &local, Relations: relations})
	if err != nil {
		t.Fatal(err)
	}
	return NewClientNames(m, nil, "")
}

// migratedDB is a database migrated with types under exposure: its report,
// the pg_proc rows the migration left, and its record. edit changes the rows
// and record before they are classified.
func migratedDB(t *testing.T, types []schema.TypeDefinition, exposure sqlgen.Exposure, edit func(rows []installedRow, rec *migrator.MigrationRecord) []installedRow) (*Report, *migrator.MigrationRecord) {
	t.Helper()
	r, err := Describe(types, exposure, "public")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := generatedFor(types, exposure, "public")
	if err != nil {
		t.Fatal(err)
	}
	rows := installedAsMigrated(t, r, gen)
	rec := recordFor(r, gen)
	rec.Exposure = exposure
	if edit != nil {
		rows = edit(rows, rec)
	}
	r.classify(rows, rec, nil)
	return r, rec
}

// codes returns the findings as code:function-or-pair, sorted.
func codes(cr *ClientReport) []string {
	out := make([]string, 0, len(cr.Findings))
	for _, f := range cr.Findings {
		subject := f.Function
		if subject == "" && f.Type != "" {
			subject = f.Type + "#" + f.Relation
		}
		out = append(out, f.Code+":"+subject)
	}
	slices.Sort(out)
	return out
}

func finding(t *testing.T, cr *ClientReport, code, subject string) ClientFinding {
	t.Helper()
	for _, f := range cr.Findings {
		if f.Code == code && (f.Function == subject || f.Type+"#"+f.Relation == subject) {
			return f
		}
	}
	t.Fatalf("no %s finding for %s in %v", code, subject, codes(cr))
	return ClientFinding{}
}

func exposure(include ...string) sqlgen.Exposure {
	return sqlgen.Exposure{Relations: sqlgen.RelationPatterns{Include: include}}
}

func TestCompareClient_InSync(t *testing.T) {
	types := clientTypes(t)
	for _, e := range []sqlgen.Exposure{{}, exposure("can_*"), {Functions: sqlgen.FunctionFamilies{ParentKey: off()}}} {
		r, rec := migratedDB(t, types, e, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, e, nil), e)
		if len(cr.Findings) != 0 || cr.FunctionsChecked == 0 || cr.PairsChecked == 0 {
			t.Errorf("%+v: a client generated under the database's exposure: %v (%d functions, %d relations)",
				e, codes(cr), cr.FunctionsChecked, cr.PairsChecked)
		}
	}
}

// A client generated under a wider exposure than the database was migrated
// with names exactly what the migration left out.
func TestCompareClient_ExposureSkew(t *testing.T) {
	types := clientTypes(t)
	t.Run("list families off", func(t *testing.T) {
		db := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ListObjects: off(), Accessible: off(), ParentKey: off()}}
		r, rec := migratedDB(t, types, db, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
		if cr.Errors != cr.FunctionsChecked || cr.Warnings != 0 {
			t.Fatalf("every accessible name is an error: %v", codes(cr))
		}
		f := finding(t, cr, "function_not_installed", "accessible_document_owner")
		if !strings.Contains(f.Message, "sql.functions.list_objects, accessible and parent_key off") || f.Remedy != RemedyMigrate || !strings.Contains(f.Fix, "run melange migrate") {
			t.Errorf("%+v", f)
		}
		f = finding(t, cr, "function_not_installed", "accessible_document_can_view_by_parent")
		if !strings.Contains(f.Message, "sql.functions.parent_key off") {
			t.Errorf("%+v", f)
		}
	})

	t.Run("parent_key off", func(t *testing.T) {
		db := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ParentKey: off()}}
		r, rec := migratedDB(t, types, db, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
		if got := codes(cr); !slices.Equal(got, []string{"function_not_installed:accessible_document_can_view_by_parent"}) {
			t.Errorf("findings %v", got)
		}
	})

	t.Run("accessible off: installed for the list functions only", func(t *testing.T) {
		db := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{Accessible: off()}}
		r, rec := migratedDB(t, types, db, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
		f := finding(t, cr, "function_internal", "accessible_document_owner")
		if f.Severity != SeverityWarning || !strings.Contains(f.Message, "sql.functions.accessible off") || f.Remedy != RemedyMigrate {
			t.Errorf("%+v", f)
		}
		if cr.Errors != 0 {
			t.Errorf("errors: %v", codes(cr))
		}
	})

	t.Run("relations not exposed", func(t *testing.T) {
		db := exposure("can_*")
		r, rec := migratedDB(t, types, db, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
		for _, p := range []string{"folder#viewer", "document#parent", "document#owner", "document#editor"} {
			if f := finding(t, cr, "relation_not_exposed", p); f.Severity != SeverityError || !strings.Contains(f.Message, "M2003") {
				t.Errorf("%+v", f)
			}
		}
		for _, f := range cr.Findings {
			if strings.Contains(f.Function, "can_") || strings.HasPrefix(f.Relation, "can_") {
				t.Errorf("an exposed name is reported: %+v", f)
			}
		}
	})

	t.Run("internal dependency", func(t *testing.T) {
		db := exposure("document#can_view")
		local := exposure("document#can_view", "folder#viewer")
		r, rec := migratedDB(t, types, db, nil)
		cr := CompareClient(r, rec, clientNamesFor(t, types, local, nil), local)
		if got := codes(cr); !slices.Equal(got, []string{"function_internal:accessible_folder_viewer", "relation_not_exposed:folder#viewer"}) {
			t.Fatalf("findings %v", got)
		}
		f := finding(t, cr, "function_internal", "accessible_folder_viewer")
		if f.Severity != SeverityWarning || !strings.Contains(f.Message, "does not expose folder#viewer") {
			t.Errorf("%+v", f)
		}
	})
}

// A client narrower than the database is fine; one whose constants are
// deliberately wider than any exposure gets info findings only.
func TestCompareClient_WiderAndNarrower(t *testing.T) {
	types := clientTypes(t)
	r, rec := migratedDB(t, types, sqlgen.Exposure{}, nil)
	if cr := CompareClient(r, rec, clientNamesFor(t, types, exposure("can_*"), nil), exposure("can_*")); len(cr.Findings) != 0 {
		t.Errorf("narrower client: %v", codes(cr))
	}

	db := exposure("can_*")
	r, rec = migratedDB(t, types, db, nil)
	cr := CompareClient(r, rec, clientNamesFor(t, types, db, &sqlgen.RelationPatterns{Include: []string{"*"}}), db)
	if cr.Errors != 0 || cr.Warnings != 0 || len(cr.Findings) != 4 {
		t.Fatalf("client constants wider than sql.relations: %v", codes(cr))
	}
	for _, f := range cr.Findings {
		if f.Code != "relation_internal" || f.Severity != SeverityInfo {
			t.Errorf("%+v", f)
		}
	}
}

// Each change to the database is reported as what it is.
func TestCompareClient_DatabaseChanges(t *testing.T) {
	types := clientTypes(t)
	names := clientNamesFor(t, types, sqlgen.Exposure{}, nil)
	byParent := "accessible_document_can_view_by_parent"
	// The database's catalog leaves the parent-key function out, as when
	// this melange no longer generates it; the rows keep it.
	keepByParent := func(recordIt bool) func([]installedRow, *migrator.MigrationRecord) []installedRow {
		return func(rows []installedRow, rec *migrator.MigrationRecord) []installedRow {
			if recordIt {
				rec.FunctionNames = append(rec.FunctionNames, byParent)
			}
			return append(rows, installedRow{name: byParent, args: "p_subject_type text, p_subject_id text"})
		}
	}
	noParentKey := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ParentKey: off()}}

	t.Run("missing", func(t *testing.T) {
		r, rec := migratedDB(t, types, sqlgen.Exposure{}, func(rows []installedRow, _ *migrator.MigrationRecord) []installedRow {
			return slices.DeleteFunc(rows, func(row installedRow) bool { return row.name == "accessible_document_owner" })
		})
		cr := CompareClient(r, rec, names, sqlgen.Exposure{})
		f := finding(t, cr, "function_not_installed", "accessible_document_owner")
		if f.Remedy != RemedyReinstall || !strings.Contains(f.Fix, "--force") || len(cr.Findings) != 1 {
			t.Errorf("%+v in %v", f, codes(cr))
		}
	})

	t.Run("signature differs", func(t *testing.T) {
		r, rec := migratedDB(t, types, sqlgen.Exposure{}, func(rows []installedRow, _ *migrator.MigrationRecord) []installedRow {
			for i := range rows {
				if rows[i].name == "accessible_document_owner" {
					rows[i].args = "subject_type text, subject_id text"
				}
			}
			return rows
		})
		cr := CompareClient(r, rec, names, sqlgen.Exposure{})
		if f := finding(t, cr, "function_signature", "accessible_document_owner"); f.Severity != SeverityError || !strings.Contains(f.Fix, "DROP FUNCTION") {
			t.Errorf("%+v", f)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		r, rec := migratedDB(t, types, noParentKey, keepByParent(true))
		cr := CompareClient(r, rec, names, sqlgen.Exposure{})
		if f := finding(t, cr, "function_orphan", byParent); f.Severity != SeverityWarning || !strings.Contains(f.Message, "next melange migrate drops it") {
			t.Errorf("%+v", f)
		}
	})

	t.Run("orphan of a newer melange", func(t *testing.T) {
		prev := version.Version
		version.Version = "v0.10.0"
		t.Cleanup(func() { version.Version = prev })
		r, rec := migratedDB(t, types, noParentKey, func(rows []installedRow, rec *migrator.MigrationRecord) []installedRow {
			rec.CodegenVersion = "v0.11.0"
			return keepByParent(true)(rows, rec)
		})
		cr := CompareClient(r, rec, names, sqlgen.Exposure{})
		if f := finding(t, cr, "function_orphan", byParent); f.Severity != SeverityInfo || cr.Errors+cr.Warnings != 0 {
			t.Errorf("%+v in %v", f, codes(cr))
		}
		if !slices.ContainsFunc(cr.Notes, func(n string) bool { return strings.Contains(n, "melange v0.11.0") }) {
			t.Errorf("notes %v", cr.Notes)
		}
	})

	t.Run("unmanaged", func(t *testing.T) {
		r, rec := migratedDB(t, types, noParentKey, keepByParent(false))
		cr := CompareClient(r, rec, names, sqlgen.Exposure{})
		if f := finding(t, cr, "function_unmanaged", byParent); f.Severity != SeverityWarning {
			t.Errorf("%+v", f)
		}
	})

	t.Run("another relation owns the name", func(t *testing.T) {
		r, rec := migratedDB(t, types, sqlgen.Exposure{}, nil)
		wrong := ClientNames{Functions: []sqlgen.AccessibleFunction{{ObjectType: "document", Relation: "editor", Name: "accessible_document_owner"}}}
		cr := CompareClient(r, rec, wrong, sqlgen.Exposure{})
		if f := finding(t, cr, "function_owner", "accessible_document_owner"); f.Severity != SeverityError || !strings.Contains(f.Message, "document#owner (accessible)") {
			t.Errorf("%+v", f)
		}
	})

	t.Run("another relation owns the name under a newer melange", func(t *testing.T) {
		prev := version.Version
		version.Version = "v0.10.0"
		t.Cleanup(func() { version.Version = prev })
		r, rec := migratedDB(t, types, sqlgen.Exposure{}, func(rows []installedRow, rec *migrator.MigrationRecord) []installedRow {
			rec.CodegenVersion = "v0.11.0"
			return rows
		})
		wrong := ClientNames{Functions: []sqlgen.AccessibleFunction{{ObjectType: "document", Relation: "editor", Name: "accessible_document_owner"}}}
		cr := CompareClient(r, rec, wrong, sqlgen.Exposure{})
		if f := finding(t, cr, "function_owner", "accessible_document_owner"); f.Severity != SeverityInfo || cr.Errors != 0 {
			t.Errorf("%+v in %v", f, codes(cr))
		}
	})

	t.Run("relation the deployed model does not define", func(t *testing.T) {
		r, rec := migratedDB(t, types, sqlgen.Exposure{}, nil)
		cr := CompareClient(r, rec, ClientNames{Pairs: []clientgen.RelationRef{{Type: "document", Relation: "auditor"}}}, sqlgen.Exposure{})
		if f := finding(t, cr, "relation_undefined", "document#auditor"); f.Severity != SeverityWarning || f.Remedy != RemedyMigrate {
			t.Errorf("%+v", f)
		}
	})
}

// Families the client names no function of are info only.
func TestCompareClient_FamilyOffInfo(t *testing.T) {
	types := clientTypes(t)
	db := sqlgen.Exposure{Functions: sqlgen.FunctionFamilies{ListSubjects: off(), Explain: off()}}
	r, rec := migratedDB(t, types, db, nil)
	cr := CompareClient(r, rec, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
	if got := codes(cr); !slices.Equal(got, []string{"family_off:", "family_off:"}) || cr.Errors+cr.Warnings != 0 {
		t.Errorf("findings %v", got)
	}
}

// Without a record the report is the local model under the local exposure,
// so a client generated from it has no findings; the notes say why.
func TestCompareClient_NoRecord(t *testing.T) {
	types := clientTypes(t)
	r, err := Describe(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := generatedFor(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	r.Notes = append(r.Notes, "No migration is recorded (as for a database built from melange generate migration files), so the local schema file is compared under the local exposure.")
	r.classify(installedAsMigrated(t, r, gen), nil, nil)
	names := clientNamesFor(t, types, sqlgen.Exposure{}, nil)
	names.Functions = append(names.Functions, sqlgen.AccessibleFunction{ObjectType: "document", Relation: "gone", Name: "accessible_document_gone"})
	r.Unmanaged = append(r.Unmanaged, InstalledFunction{Name: "accessible_document_gone"})
	cr := CompareClient(r, nil, names, sqlgen.Exposure{})
	if len(cr.Findings) != 0 {
		t.Errorf("findings %v", codes(cr))
	}
	if len(cr.Notes) < 2 || !strings.Contains(cr.Notes[0], "No migration is recorded") || !strings.Contains(cr.Notes[len(cr.Notes)-1], "taken as melange's") {
		t.Errorf("notes %v", cr.Notes)
	}
}

// Without a record no migration installed a missing function, so the
// finding claims none and its fix is a migrate under melange.yaml (doctor
// adds the generate migration files case).
func TestCompareClient_NoRecordMissing(t *testing.T) {
	types := clientTypes(t)
	r, err := Describe(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := generatedFor(types, sqlgen.Exposure{}, "public")
	if err != nil {
		t.Fatal(err)
	}
	rows := slices.DeleteFunc(installedAsMigrated(t, r, gen), func(row installedRow) bool { return row.name == "accessible_document_owner" })
	r.classify(rows, nil, nil)
	cr := CompareClient(r, nil, clientNamesFor(t, types, sqlgen.Exposure{}, nil), sqlgen.Exposure{})
	f := finding(t, cr, "function_not_installed", "accessible_document_owner")
	if f.Severity != SeverityError || f.Remedy != RemedyMigrate || strings.Contains(f.Message, "last migration") ||
		!strings.Contains(f.Message, "no migration is recorded") || len(cr.Findings) != 1 {
		t.Errorf("%+v in %v", f, codes(cr))
	}
}

// Truncated and hashed names are compared as the exact strings migrate
// creates.
func TestCompareClient_TruncatedNames(t *testing.T) {
	types, err := parser.ParseSchema("../../test/testutil/testdata/schema.fga")
	if err != nil {
		t.Fatal(err)
	}
	const long = "accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c"
	names := clientNamesFor(t, types, sqlgen.Exposure{}, nil)
	if !slices.ContainsFunc(names.Functions, func(f sqlgen.AccessibleFunction) bool { return f.Name == long }) {
		t.Fatalf("the client does not name %s", long)
	}
	r, rec := migratedDB(t, types, sqlgen.Exposure{}, nil)
	if cr := CompareClient(r, rec, names, sqlgen.Exposure{}); len(cr.Findings) != 0 {
		t.Errorf("in sync: %v", codes(cr))
	}
	r, rec = migratedDB(t, types, sqlgen.Exposure{}, func(rows []installedRow, _ *migrator.MigrationRecord) []installedRow {
		return slices.DeleteFunc(rows, func(row installedRow) bool { return row.name == long })
	})
	cr := CompareClient(r, rec, names, sqlgen.Exposure{})
	if got := codes(cr); !slices.Equal(got, []string{"function_not_installed:" + long}) {
		t.Errorf("findings %v", got)
	}
}

// Two relations whose functions share a name (a_b#c and a#b_c both make
// accessible_a_b_c) leave one definition in the database. A client that
// selects only one of them must not take the name as its own.
func TestCompareClient_NameCollision(t *testing.T) {
	types, err := parser.ParseSchemaString(`model
  schema 1.1
type user
type a_b
  relations
    define c: [user]
type a
  relations
    define b_c: [user]
`)
	if err != nil {
		t.Fatal(err)
	}
	names := clientNamesFor(t, types, sqlgen.Exposure{}, &sqlgen.RelationPatterns{Include: []string{"a_b#c"}})
	r, rec := migratedDB(t, types, sqlgen.Exposure{}, nil)
	cr := CompareClient(r, rec, names, sqlgen.Exposure{})
	i := slices.IndexFunc(cr.Findings, func(f ClientFinding) bool { return f.Function == "accessible_a_b_c" })
	if i < 0 {
		t.Fatalf("no finding for accessible_a_b_c: %v", codes(cr))
	}
	if f := cr.Findings[i]; f.Code != "function_collision" || f.Severity != SeverityError {
		t.Errorf("finding = %s/%s, want function_collision/error", f.Code, f.Severity)
	}
}
