package parser

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"github.com/pthm/melange/pkg/schema"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("creating directory for %s: %v", name, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func findType(types []schema.TypeDefinition, name string) *schema.TypeDefinition {
	for i := range types {
		if types[i].Name == name {
			return &types[i]
		}
	}
	return nil
}

func relationNames(td *schema.TypeDefinition) []string {
	names := make([]string, len(td.Relations))
	for i, r := range td.Relations {
		names[i] = r.Name
	}
	return names
}

func typeNames(types []schema.TypeDefinition) []string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.Name
	}
	sort.Strings(names)
	return names
}

func TestParseModularSchema_Basic(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - app.fga
`)
	writeTestFile(t, dir, "core.fga", `module core

type user

type organization
  relations
    define member: [user]
    define admin: [user]
`)
	writeTestFile(t, dir, "app.fga", `module app

extend type organization
  relations
    define can_create_project: admin

type project
  relations
    define organization: [organization]
    define viewer: member from organization
`)

	types, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := typeNames(types)
	expected := []string{"organization", "project", "user"}
	if !slices.Equal(names, expected) {
		t.Fatalf("expected types %v, got %v", expected, names)
	}

	// Verify organization has both base and extended relations
	org := findType(types, "organization")
	if org == nil {
		t.Fatal("organization type not found")
	}
	rels := relationNames(org)
	for _, want := range []string{"member", "admin", "can_create_project"} {
		if !slices.Contains(rels, want) {
			t.Errorf("expected relation %s on organization, got %v", want, rels)
		}
	}
}

func TestParseModularSchema_MultiExtend(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - app1.fga
  - app2.fga
`)
	writeTestFile(t, dir, "core.fga", `module core

type user

type organization
  relations
    define member: [user]
    define admin: [user]
`)
	writeTestFile(t, dir, "app1.fga", `module app1

extend type organization
  relations
    define can_create_doc: admin
`)
	writeTestFile(t, dir, "app2.fga", `module app2

extend type organization
  relations
    define can_create_wiki: admin or member
`)

	types, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	org := findType(types, "organization")
	if org == nil {
		t.Fatal("organization type not found")
	}

	rels := relationNames(org)
	for _, want := range []string{"member", "admin", "can_create_doc", "can_create_wiki"} {
		if !slices.Contains(rels, want) {
			t.Errorf("expected relation %s on organization, got %v", want, rels)
		}
	}
}

func TestParseModularSchema_NestedDirectories(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - tracker/projects.fga
`)
	writeTestFile(t, dir, "core.fga", `module core

type user

type organization
  relations
    define member: [user]
`)
	writeTestFile(t, dir, "tracker/projects.fga", `module tracker

type project
  relations
    define organization: [organization]
    define viewer: member from organization
`)

	types, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := typeNames(types)
	expected := []string{"organization", "project", "user"}
	if !slices.Equal(names, expected) {
		t.Fatalf("expected types %v, got %v", expected, names)
	}
}

func TestParseModularSchema_MissingModuleFile(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - nonexistent.fga
`)
	writeTestFile(t, dir, "core.fga", `module core
type user
`)

	_, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err == nil {
		t.Fatal("expected error for missing module file")
	}
	if testing.Verbose() {
		t.Logf("error: %v", err)
	}
}

func TestParseModularSchema_InvalidManifest(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `not valid yaml at all [[[`)

	_, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestParseModularSchema_MissingManifest(t *testing.T) {
	_, err := ParseSchema("/nonexistent/path/fga.mod")
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestParseSchema_SingleFileStillWorks(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "schema.fga", `model
  schema 1.1

type user

type document
  relations
    define viewer: [user]
`)

	types, err := ParseSchema(filepath.Join(dir, "schema.fga"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := typeNames(types)
	expected := []string{"document", "user"}
	if !slices.Equal(names, expected) {
		t.Fatalf("expected types %v, got %v", expected, names)
	}
}

func TestParseModularSchemaFromStrings(t *testing.T) {
	modules := map[string]string{
		"core.fga": `module core

type user

type organization
  relations
    define member: [user]
`,
		"app.fga": `module app

extend type organization
  relations
    define can_create: member

type project
  relations
    define organization: [organization]
`,
	}

	types, err := ParseModularSchemaFromStrings(modules, "1.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := typeNames(types)
	expected := []string{"organization", "project", "user"}
	if !slices.Equal(names, expected) {
		t.Fatalf("expected types %v, got %v", expected, names)
	}

	org := findType(types, "organization")
	if org == nil {
		t.Fatal("organization type not found")
	}
	rels := relationNames(org)
	for _, want := range []string{"member", "can_create"} {
		if !slices.Contains(rels, want) {
			t.Errorf("expected relation %s on organization, got %v", want, rels)
		}
	}
}

func TestParseModularSchemaFromStrings_DuplicateRelation(t *testing.T) {
	modules := map[string]string{
		"core.fga": `module core

type organization
  relations
    define viewer: [user]

type user
`,
		"app.fga": `module app

extend type organization
  relations
    define viewer: [user]
`,
	}

	_, err := ParseModularSchemaFromStrings(modules, "1.2")
	if err == nil {
		t.Fatal("expected error for duplicate relation")
	}
	if testing.Verbose() {
		t.Logf("error: %v", err)
	}
}

func TestParseModularSchemaFromStrings_ExtendUndefinedType(t *testing.T) {
	modules := map[string]string{
		"app.fga": `module app

extend type organization
  relations
    define can_create: [user]

type user
`,
	}

	_, err := ParseModularSchemaFromStrings(modules, "1.2")
	if err == nil {
		t.Fatal("expected error for extending undefined type")
	}
	if testing.Verbose() {
		t.Logf("error: %v", err)
	}
}

func TestReadManifestContents(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - app.fga
`)
	writeTestFile(t, dir, "core.fga", `module core
type user
`)
	writeTestFile(t, dir, "app.fga", `module app
type document
  relations
    define viewer: [user]
`)

	contents, err := ReadManifestContents(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(contents) == 0 {
		t.Fatal("expected non-empty contents")
	}

	// Verify determinism: calling again produces identical output
	contents2, err := ReadManifestContents(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("unexpected error on second read: %v", err)
	}

	if !bytes.Equal(contents, contents2) {
		t.Error("ReadManifestContents is not deterministic")
	}
}

func TestParseManifestEntries(t *testing.T) {
	manifest := `schema: '1.2'
contents:
  - core.fga
  - app.fga
  - tracker/projects.fga
`
	version, paths, err := ParseManifestEntries(manifest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if version != "1.2" {
		t.Errorf("expected schema version 1.2, got %s", version)
	}

	expected := []string{"core.fga", "app.fga", "tracker/projects.fga"}
	if !slices.Equal(paths, expected) {
		t.Errorf("expected paths %v, got %v", expected, paths)
	}
}

func TestParseManifestEntries_InvalidManifest(t *testing.T) {
	_, _, err := ParseManifestEntries(`not valid yaml [[[`)
	if err == nil {
		t.Fatal("expected error for invalid manifest")
	}
}

func TestParseModularSchema_Equivalence(t *testing.T) {
	// Verify that a modular schema produces the same types as an equivalent single-file schema
	dir := t.TempDir()

	// Modular version
	writeTestFile(t, dir, "fga.mod", `schema: '1.2'
contents:
  - core.fga
  - app.fga
`)
	writeTestFile(t, dir, "core.fga", `module core

type user

type organization
  relations
    define member: [user]
    define admin: [user]
`)
	writeTestFile(t, dir, "app.fga", `module app

extend type organization
  relations
    define can_create_project: admin

type project
  relations
    define organization: [organization]
    define viewer: member from organization
`)

	// Equivalent single-file version
	singleFileContent := `model
  schema 1.1

type user

type organization
  relations
    define member: [user]
    define admin: [user]
    define can_create_project: admin

type project
  relations
    define organization: [organization]
    define viewer: member from organization
`

	modularTypes, err := ParseSchema(filepath.Join(dir, "fga.mod"))
	if err != nil {
		t.Fatalf("modular parse error: %v", err)
	}

	singleTypes, err := ParseSchemaString(singleFileContent)
	if err != nil {
		t.Fatalf("single-file parse error: %v", err)
	}

	// Compare type names
	modNames := typeNames(modularTypes)
	singleNames := typeNames(singleTypes)

	if !slices.Equal(modNames, singleNames) {
		t.Fatalf("type name mismatch: modular=%v, single=%v", modNames, singleNames)
	}

	// Compare relations on each type
	for _, typeName := range modNames {
		modType := findType(modularTypes, typeName)
		singleType := findType(singleTypes, typeName)

		modRels := relationNames(modType)
		singleRels := relationNames(singleType)
		sort.Strings(modRels)
		sort.Strings(singleRels)

		if !slices.Equal(modRels, singleRels) {
			t.Errorf("type %s: relation mismatch: modular=%v, single=%v", typeName, modRels, singleRels)
		}
	}
}
