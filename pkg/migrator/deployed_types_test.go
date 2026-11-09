package migrator

import (
	"testing"

	"github.com/pthm/melange/pkg/schema"
)

func TestDeployedTypes_PrefersModelJSON(t *testing.T) {
	encoded, err := schema.MarshalModel([]schema.TypeDefinition{
		{Name: "user"},
		{Name: "document", Relations: []schema.RelationDefinition{{Name: "viewer", SubjectTypeRefs: []schema.SubjectTypeRef{{Type: "user"}}}}},
	})
	if err != nil {
		t.Fatalf("MarshalModel: %v", err)
	}
	// A DSL that would parse to something different, so preferring the JSON is
	// observable rather than merely plausible.
	rec := &MigrationRecord{SchemaDSL: "model\n  schema 1.1\ntype user\n", ModelJSON: encoded}
	types, err := DeployedTypes(rec)
	if err != nil {
		t.Fatalf("DeployedTypes: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("types = %d, want 2 from model_json (not the 1-type DSL)", len(types))
	}
}

func TestDeployedTypes_FallsBackToDSL(t *testing.T) {
	for name, modelJSON := range map[string][]byte{"absent": nil, "empty object": []byte("{}"), "empty list": []byte("[]")} {
		types, err := DeployedTypes(&MigrationRecord{SchemaDSL: "model\n  schema 1.1\ntype user\n", ModelJSON: modelJSON})
		if err != nil {
			t.Fatalf("%s: DeployedTypes: %v", name, err)
		}
		if len(types) != 1 || types[0].Name != "user" {
			t.Fatalf("%s: types = %+v, want the single type parsed from the DSL", name, types)
		}
	}
}

func TestDeployedTypes_PreStorageRecordIsNotAnError(t *testing.T) {
	for _, rec := range []*MigrationRecord{nil, {SchemaChecksum: "abc"}} {
		types, err := DeployedTypes(rec)
		if err != nil {
			t.Fatalf("a record without a DSL must not error, got %v", err)
		}
		if types != nil {
			t.Errorf("types = %+v, want nil", types)
		}
	}
}

func TestDeployedTypes_CorruptDSLErrors(t *testing.T) {
	if _, err := DeployedTypes(&MigrationRecord{SchemaDSL: "this is not a schema"}); err == nil {
		t.Error("expected an error for an unparseable recorded DSL")
	}
}
