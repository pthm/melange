package migrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewMigrator(t *testing.T) {
	m := NewMigrator(nil, "schemas/schema.fga")

	if m.SchemaPath() != "schemas/schema.fga" {
		t.Errorf("SchemaPath() = %q, want %q", m.SchemaPath(), "schemas/schema.fga")
	}
}

func TestHasSchema(t *testing.T) {
	t.Run("returns false for nonexistent path", func(t *testing.T) {
		m := NewMigrator(nil, "/nonexistent/schema.fga")
		if m.HasSchema() {
			t.Error("HasSchema() should return false for nonexistent path")
		}
	})

	t.Run("returns true for existing file", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "schema.fga")
		if err := os.WriteFile(tmp, []byte("model"), 0o644); err != nil {
			t.Fatal(err)
		}

		m := NewMigrator(nil, tmp)
		if !m.HasSchema() {
			t.Error("HasSchema() should return true for existing file")
		}
	})

	t.Run("returns false for empty path", func(t *testing.T) {
		m := NewMigrator(nil, "")
		if m.HasSchema() {
			t.Error("HasSchema() should return false for empty path")
		}
	})
}

func TestApplyDDL(t *testing.T) {
	m := NewMigrator(nil, "")
	if err := m.ApplyDDL(context.Background()); err != nil {
		t.Errorf("ApplyDDL() should return nil (no-op), got %v", err)
	}
}

func TestComputeSchemaChecksum(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		content := "model\n  schema 1.1\ntype user"
		a := ComputeSchemaChecksum(content)
		b := ComputeSchemaChecksum(content)
		if a != b {
			t.Errorf("checksums should be deterministic: %q != %q", a, b)
		}
	})

	t.Run("different content gives different checksum", func(t *testing.T) {
		a := ComputeSchemaChecksum("schema A")
		b := ComputeSchemaChecksum("schema B")
		if a == b {
			t.Error("different content should produce different checksums")
		}
	})

	t.Run("returns 64-char lowercase hex", func(t *testing.T) {
		for _, input := range []string{"test", ""} {
			checksum := ComputeSchemaChecksum(input)
			if len(checksum) != 64 { // SHA256 hex = 64 chars
				t.Errorf("ComputeSchemaChecksum(%q) length = %d, want 64", input, len(checksum))
			}
			for _, c := range checksum {
				if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
					t.Errorf("ComputeSchemaChecksum(%q) contains non-hex char: %c", input, c)
					break
				}
			}
		}
	})
}

func TestShouldSkipMigration(t *testing.T) {
	checksum := ComputeSchemaChecksum("test schema")

	t.Run("nil record does not skip", func(t *testing.T) {
		if shouldSkipMigration(nil, checksum) {
			t.Error("should not skip when no previous migration exists")
		}
	})

	t.Run("matching checksum and codegen version skips", func(t *testing.T) {
		rec := &MigrationRecord{
			SchemaChecksum: checksum,
			CodegenVersion: CodegenVersion(),
		}
		if !shouldSkipMigration(rec, checksum) {
			t.Error("should skip when checksum and codegen version match")
		}
	})

	t.Run("different checksum does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			SchemaChecksum: "different-checksum",
			CodegenVersion: CodegenVersion(),
		}
		if shouldSkipMigration(rec, checksum) {
			t.Error("should not skip when checksum differs")
		}
	})

	t.Run("different codegen version does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			SchemaChecksum: checksum,
			CodegenVersion: "99",
		}
		if shouldSkipMigration(rec, checksum) {
			t.Error("should not skip when codegen version differs")
		}
	})

	t.Run("both different does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			SchemaChecksum: "other",
			CodegenVersion: "99",
		}
		if shouldSkipMigration(rec, checksum) {
			t.Error("should not skip when both differ")
		}
	})
}

func TestShouldSkipApply(t *testing.T) {
	checksums := map[string]string{
		"check_doc_viewer": "hash_a",
		"check_doc_owner":  "hash_b",
	}
	functions := []string{"check_doc_viewer", "check_doc_owner", "check_permission"}

	t.Run("nil record does not skip", func(t *testing.T) {
		if shouldSkipApply(nil, checksums, functions) {
			t.Error("should not skip when no previous migration exists")
		}
	})

	t.Run("nil previous checksums does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			FunctionNames:     functions,
			FunctionChecksums: nil,
		}
		if shouldSkipApply(rec, checksums, functions) {
			t.Error("should not skip when previous checksums are nil")
		}
	})

	t.Run("identical checksums and no orphans skips", func(t *testing.T) {
		rec := &MigrationRecord{
			FunctionNames:     functions,
			FunctionChecksums: checksums,
		}
		if !shouldSkipApply(rec, checksums, functions) {
			t.Error("should skip when all checksums match and no orphans")
		}
	})

	t.Run("changed checksum does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			FunctionNames: functions,
			FunctionChecksums: map[string]string{
				"check_doc_viewer": "old_hash",
				"check_doc_owner":  "hash_b",
			},
		}
		if shouldSkipApply(rec, checksums, functions) {
			t.Error("should not skip when a checksum changed")
		}
	})

	t.Run("new function does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			FunctionNames:     []string{"check_doc_viewer", "check_permission"},
			FunctionChecksums: map[string]string{"check_doc_viewer": "hash_a"},
		}
		if shouldSkipApply(rec, checksums, functions) {
			t.Error("should not skip when there are new functions")
		}
	})

	t.Run("orphaned function does not skip", func(t *testing.T) {
		rec := &MigrationRecord{
			FunctionNames: []string{
				"check_doc_viewer", "check_doc_owner",
				"check_old_removed", // orphan
				"check_permission",
			},
			FunctionChecksums: map[string]string{
				"check_doc_viewer":  "hash_a",
				"check_doc_owner":   "hash_b",
				"check_old_removed": "hash_c",
			},
		}
		if shouldSkipApply(rec, checksums, functions) {
			t.Error("should not skip when there are orphaned functions")
		}
	})
}

func TestOutputDryRun(t *testing.T) {
	m := NewMigrator(nil, "")

	t.Run("includes header and sections", func(t *testing.T) {
		var buf bytes.Buffer
		gen := GeneratedSQL{
			Functions:            []string{"CREATE FUNCTION check_repo_viewer()"},
			NoWildcardFunctions:  []string{"CREATE FUNCTION check_repo_viewer_nw()"},
			Dispatcher:           "CREATE FUNCTION check_permission()",
			DispatcherNoWildcard: "CREATE FUNCTION check_permission_nw()",
			BulkDispatcher:       "CREATE FUNCTION bulk_check_permission()",
		}
		listSQL := ListGeneratedSQL{
			ListObjectsFunctions:   []string{"CREATE FUNCTION list_objects_repo_viewer()"},
			ListSubjectsFunctions:  []string{"CREATE FUNCTION list_subjects_repo_viewer()"},
			ListObjectsDispatcher:  "CREATE FUNCTION list_objects()",
			ListSubjectsDispatcher: "CREATE FUNCTION list_subjects()",
		}
		expected := []string{"check_repo_viewer", "check_permission"}

		m.outputDryRun(&buf, "v0.5.0", "abc123", gen, listSQL, expected)
		output := buf.String()

		// Header
		if !strings.Contains(output, "-- Melange Migration (dry-run)") {
			t.Error("should contain dry-run header")
		}
		if !strings.Contains(output, "-- Melange version: v0.5.0") {
			t.Error("should contain melange version")
		}
		if !strings.Contains(output, "-- Schema checksum: abc123") {
			t.Error("should contain schema checksum")
		}
		if !strings.Contains(output, "-- Codegen version: "+CodegenVersion()) {
			t.Error("should contain codegen version")
		}

		// Sections
		if !strings.Contains(output, "Check Functions (1 functions)") {
			t.Error("should contain check functions section")
		}
		if !strings.Contains(output, "No-Wildcard Check Functions (1 functions)") {
			t.Error("should contain no-wildcard section")
		}
		if !strings.Contains(output, "List Objects Functions (1 functions)") {
			t.Error("should contain list objects section")
		}
		if !strings.Contains(output, "List Subjects Functions (1 functions)") {
			t.Error("should contain list subjects section")
		}

		// Function content
		if !strings.Contains(output, "CREATE FUNCTION check_repo_viewer()") {
			t.Error("should contain check function SQL")
		}
		if !strings.Contains(output, "CREATE FUNCTION check_permission()") {
			t.Error("should contain dispatcher SQL")
		}
		if !strings.Contains(output, "CREATE FUNCTION list_objects()") {
			t.Error("should contain list objects dispatcher")
		}

		// Migration record (schema-qualified with default "public")
		if !strings.Contains(output, "INSERT INTO") || !strings.Contains(output, "melange_migrations") {
			t.Error("should contain migration record INSERT")
		}
	})

	t.Run("omits version when empty", func(t *testing.T) {
		var buf bytes.Buffer
		m.outputDryRun(&buf, "", "abc", GeneratedSQL{}, ListGeneratedSQL{}, nil)
		output := buf.String()

		if strings.Contains(output, "-- Melange version:") {
			t.Error("should not include melange version line when empty")
		}
	})

	t.Run("sorts function names in migration record", func(t *testing.T) {
		var buf bytes.Buffer
		expected := []string{"check_z_viewer", "check_a_owner", "check_m_editor"}
		m.outputDryRun(&buf, "", "abc", GeneratedSQL{}, ListGeneratedSQL{}, expected)
		output := buf.String()

		// Should appear sorted in the INSERT
		idx_a := strings.Index(output, "'check_a_owner'")
		idx_m := strings.Index(output, "'check_m_editor'")
		idx_z := strings.Index(output, "'check_z_viewer'")

		if idx_a == -1 || idx_m == -1 || idx_z == -1 {
			t.Fatal("all function names should appear in output")
		}
		if idx_a >= idx_m || idx_m >= idx_z {
			t.Error("function names should be sorted alphabetically in migration record")
		}
	})
}

func TestMigrateNoSchemaFile(t *testing.T) {
	t.Run("Migrate", func(t *testing.T) {
		err := Migrate(context.Background(), nil, "/nonexistent/schema.fga")
		if err == nil {
			t.Fatal("should return error for nonexistent schema")
		}
		if !strings.Contains(err.Error(), "no schema found") {
			t.Errorf("error should mention 'no schema found', got: %v", err)
		}
	})

	t.Run("MigrateWithOptions", func(t *testing.T) {
		_, err := MigrateWithOptions(context.Background(), nil, "/nonexistent/schema.fga", MigrateOptions{})
		if err == nil {
			t.Fatal("should return error for nonexistent schema")
		}
		if !strings.Contains(err.Error(), "no schema found") {
			t.Errorf("error should mention 'no schema found', got: %v", err)
		}
	})
}

func TestMigrationsDDL(t *testing.T) {
	t.Run("no schema uses unqualified table name", func(t *testing.T) {
		sql := migrationsDDL("")
		if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS melange_migrations") {
			t.Error("should use unqualified table name")
		}
		if strings.Contains(sql, `"."`) {
			t.Error("should not contain schema qualification")
		}
	})

	t.Run("with schema qualifies table name", func(t *testing.T) {
		sql := migrationsDDL("authz")
		if !strings.Contains(sql, `CREATE TABLE IF NOT EXISTS "authz"."melange_migrations"`) {
			t.Errorf("should schema-qualify table name, got:\n%s", sql)
		}
		if !strings.Contains(sql, `ON "authz"."melange_migrations"`) {
			t.Errorf("should schema-qualify index target, got:\n%s", sql)
		}
	})
}

func TestAddMelangeVersionColumn(t *testing.T) {
	t.Run("no schema", func(t *testing.T) {
		sql := addMelangeVersionColumn("")
		if !strings.Contains(sql, "ALTER TABLE melange_migrations") {
			t.Error("should use unqualified table name")
		}
	})

	t.Run("with schema", func(t *testing.T) {
		sql := addMelangeVersionColumn("authz")
		if !strings.Contains(sql, `ALTER TABLE "authz"."melange_migrations"`) {
			t.Errorf("should schema-qualify table name, got:\n%s", sql)
		}
	})
}

func TestAddFunctionChecksumsColumn(t *testing.T) {
	t.Run("no schema", func(t *testing.T) {
		sql := addFunctionChecksumsColumn("")
		if !strings.Contains(sql, "ALTER TABLE melange_migrations") {
			t.Error("should use unqualified table name")
		}
	})

	t.Run("with schema", func(t *testing.T) {
		sql := addFunctionChecksumsColumn("authz")
		if !strings.Contains(sql, `ALTER TABLE "authz"."melange_migrations"`) {
			t.Errorf("should schema-qualify table name, got:\n%s", sql)
		}
	})
}

func TestOutputDryRun_DatabaseSchema(t *testing.T) {
	t.Run("with schema shows hint comment", func(t *testing.T) {
		m := NewMigrator(nil, "")
		m.SetDatabaseSchema("authz")

		var buf bytes.Buffer
		m.outputDryRun(&buf, "", "abc", GeneratedSQL{}, ListGeneratedSQL{}, nil)
		output := buf.String()

		if !strings.Contains(output, "Database schema: authz") {
			t.Error("should mention the configured schema")
		}
		if !strings.Contains(output, `CREATE SCHEMA IF NOT EXISTS "authz"`) {
			t.Error("should show CREATE SCHEMA hint")
		}
		if !strings.Contains(output, `"authz"."melange_migrations"`) {
			t.Error("DDL should use schema-qualified table name")
		}
	})

	t.Run("default schema shows public", func(t *testing.T) {
		m := NewMigrator(nil, "")

		var buf bytes.Buffer
		m.outputDryRun(&buf, "", "abc", GeneratedSQL{}, ListGeneratedSQL{}, nil)
		output := buf.String()

		if !strings.Contains(output, "public") {
			t.Error("should show public as default schema")
		}
	})
}

func TestMigrateFromString_InvalidSchema(t *testing.T) {
	err := MigrateFromString(context.Background(), nil, "this is not valid OpenFGA")
	if err == nil {
		t.Fatal("MigrateFromString should return error for invalid schema")
	}
	if !strings.Contains(err.Error(), "parsing schema") {
		t.Errorf("error should mention 'parsing schema', got: %v", err)
	}
}
