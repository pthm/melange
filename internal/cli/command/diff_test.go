package command

import (
	"strings"
	"testing"
)

// setDiffFlags points the package-level diff flags at one source and restores
// them afterwards, so each test exercises resolution in isolation.
func setDiffFlags(t *testing.T, gitRef, previousSchema string) {
	t.Helper()
	origRef, origPrev := diffGitRef, diffPreviousSchema
	t.Cleanup(func() { diffGitRef, diffPreviousSchema = origRef, origPrev })
	diffGitRef, diffPreviousSchema = gitRef, previousSchema
}

func TestDiffPreviousModel_GitRefReadsSchemaAtThatCommit(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1, schemaV2)
	setDiffFlags(t, "HEAD~1", "")

	types, source, err := diffPreviousModel("schema.fga", "public")
	if err != nil {
		t.Fatalf("diffPreviousModel: %v", err)
	}
	if source != "git:HEAD~1" {
		t.Errorf("source = %q, want git:HEAD~1", source)
	}
	// HEAD~1 is the one-type version; the working tree has two.
	if len(types) != 1 || types[0].Name != "user" {
		t.Errorf("types = %+v, want only the type present at HEAD~1", types)
	}
}

func TestDiffPreviousModel_PreviousSchemaReadsTheFile(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV2)
	writeFile(t, "old.fga", schemaV1)
	setDiffFlags(t, "", "old.fga")

	types, source, err := diffPreviousModel("schema.fga", "public")
	if err != nil {
		t.Fatalf("diffPreviousModel: %v", err)
	}
	if source != "file:old.fga" {
		t.Errorf("source = %q, want file:old.fga", source)
	}
	if len(types) != 1 {
		t.Errorf("types = %d, want 1 from old.fga", len(types))
	}
}

// A modular previous schema cannot be read as one file, and saying so beats
// diffing against a half-parsed model.
func TestDiffPreviousModel_PreviousSchemaRejectsModular(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	writeFile(t, "fga.mod", "schema: 1.2\ncontents:\n  - core.fga\n")
	setDiffFlags(t, "", "fga.mod")

	_, _, err := diffPreviousModel("schema.fga", "public")
	if err == nil {
		t.Fatal("expected an error for a modular --previous-schema")
	}
	if !strings.Contains(err.Error(), "modular") {
		t.Errorf("error = %q, want it to name the modular limitation", err)
	}
}

// An unknown ref must surface as an error rather than an empty previous model,
// which would misreport every existing type as newly added.
func TestDiffPreviousModel_UnknownGitRefErrors(t *testing.T) {
	gitRepoWithSchema(t, "schema.fga", schemaV1)
	setDiffFlags(t, "no-such-ref", "")

	if _, _, err := diffPreviousModel("schema.fga", "public"); err == nil {
		t.Error("expected an error for an unknown git ref")
	}
}
