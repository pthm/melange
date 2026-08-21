package command

import (
	"encoding/json"
	"testing"

	"github.com/pthm/melange/pkg/migrator"
	"github.com/pthm/melange/pkg/schema"
)

// model builds a one-type model whose viewer relation grants the given subject
// types, which is enough shape to produce every class of drift under test.
func model(subjectTypes ...string) []schema.TypeDefinition {
	rels := []schema.RelationDefinition{{Name: "viewer"}}
	for _, st := range subjectTypes {
		rels[0].SubjectTypeRefs = append(rels[0].SubjectTypeRefs, schema.SubjectTypeRef{Type: st})
	}
	return []schema.TypeDefinition{
		{Name: "user"},
		{Name: "document", Relations: rels},
	}
}

func TestSummarizeDrift_Additive(t *testing.T) {
	deployed := model("user")
	local := append(model("user"), schema.TypeDefinition{Name: "folder"})

	d := summarizeDrift(deployed, local)
	if d == nil {
		t.Fatal("expected drift detail for an added type")
	}
	if d.Breaking != 0 {
		t.Errorf("added type must not be breaking, got %d breaking", d.Breaking)
	}
	if d.Additive != 1 {
		t.Errorf("additive = %d, want 1", d.Additive)
	}
	if len(d.Changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(d.Changes))
	}
}

func TestSummarizeDrift_Breaking(t *testing.T) {
	deployed := append(model("user"), schema.TypeDefinition{Name: "folder"})
	local := model("user")

	d := summarizeDrift(deployed, local)
	if d == nil {
		t.Fatal("expected drift detail for a removed type")
	}
	if d.Breaking != 1 {
		t.Errorf("breaking = %d, want 1 (removing a type narrows access)", d.Breaking)
	}
}

// Equivalent models with different checksums are real: reformatting or a
// comment edit moves the checksum without changing behavior. Status must not
// invent changes to explain that.
func TestSummarizeDrift_EquivalentModelsReportNoDetail(t *testing.T) {
	if d := summarizeDrift(model("user"), model("user")); d != nil {
		t.Errorf("equivalent models should produce no drift detail, got %+v", d)
	}
}

func TestDeployedTypesFromRecord_PrefersModelJSON(t *testing.T) {
	encoded, err := schema.MarshalModel(model("user"))
	if err != nil {
		t.Fatalf("MarshalModel: %v", err)
	}
	// A DSL that would parse to something different, so preferring the JSON is
	// observable rather than merely plausible.
	rec := &migrator.MigrationRecord{
		SchemaDSL: "model\n  schema 1.1\ntype user\n",
		ModelJSON: encoded,
	}

	types, err := deployedTypesFromRecord(rec)
	if err != nil {
		t.Fatalf("deployedTypesFromRecord: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("types = %d, want 2 from model_json (not the 1-type DSL)", len(types))
	}
}

func TestDeployedTypesFromRecord_FallsBackToDSL(t *testing.T) {
	rec := &migrator.MigrationRecord{
		SchemaDSL: "model\n  schema 1.1\ntype user\n",
		ModelJSON: nil, // never written, or written empty
	}

	types, err := deployedTypesFromRecord(rec)
	if err != nil {
		t.Fatalf("deployedTypesFromRecord: %v", err)
	}
	if len(types) != 1 || types[0].Name != "user" {
		t.Fatalf("types = %+v, want the single type parsed from the DSL", types)
	}
}

func TestDeployedTypesFromRecord_PreStorageRecordIsNotAnError(t *testing.T) {
	types, err := deployedTypesFromRecord(&migrator.MigrationRecord{SchemaChecksum: "abc"})
	if err != nil {
		t.Fatalf("a record without a DSL must not error, got %v", err)
	}
	if types != nil {
		t.Errorf("types = %+v, want nil", types)
	}
}

func TestDeployedTypesFromRecord_CorruptDSLErrors(t *testing.T) {
	rec := &migrator.MigrationRecord{SchemaDSL: "this is not a schema"}
	if _, err := deployedTypesFromRecord(rec); err == nil {
		t.Error("expected an error for an unparseable recorded DSL")
	}
}

// The drift block is omitted from JSON when absent, so consumers that only
// check `sync` are unaffected by this addition.
func TestStatusReport_DriftOmittedWhenAbsent(t *testing.T) {
	out, err := json.Marshal(statusReport{Sync: syncInSync})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(out); got != `{"schema_file":"","tuples_view":"","sync":"in_sync"}` {
		t.Errorf("unexpected JSON: %s", got)
	}
}
