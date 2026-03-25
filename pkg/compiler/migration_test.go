package compiler

import (
	"strings"
	"testing"
)

func TestGenerateMigrationSQL_FullMode(t *testing.T) {
	gen := GeneratedSQL{
		Functions:            []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() ..."},
		NoWildcardFunctions:  []string{"CREATE OR REPLACE FUNCTION check_doc_viewer_nw() ..."},
		Dispatcher:           "CREATE OR REPLACE FUNCTION check_permission() ...",
		DispatcherNoWildcard: "CREATE OR REPLACE FUNCTION check_permission_nw() ...",
		BulkDispatcher:       "CREATE OR REPLACE FUNCTION check_permission_bulk() ...",
	}
	listSQL := ListGeneratedSQL{
		ListObjectsFunctions:   []string{"CREATE OR REPLACE FUNCTION list_doc_viewer_obj() ..."},
		ListSubjectsFunctions:  []string{"CREATE OR REPLACE FUNCTION list_doc_viewer_sub() ..."},
		ListObjectsDispatcher:  "CREATE OR REPLACE FUNCTION list_accessible_objects() ...",
		ListSubjectsDispatcher: "CREATE OR REPLACE FUNCTION list_accessible_subjects() ...",
	}
	functions := []string{
		"check_doc_viewer",
		"check_doc_viewer_nw",
		"check_permission",
		"check_permission_internal",
		"check_permission_nw",
		"check_permission_nw_internal",
		"check_permission_bulk",
		"list_doc_viewer_obj",
		"list_doc_viewer_sub",
		"list_accessible_objects",
		"list_accessible_subjects",
	}
	opts := MigrationOptions{
		Version:        "v0.7.3",
		SchemaChecksum: "abc123",
		CodegenVersion: "1",
	}

	result := GenerateMigrationSQL(gen, listSQL, functions, opts)

	// UP should have all CREATEs and version
	if !strings.Contains(result.Up, "-- Melange Migration (UP)") {
		t.Error("UP missing header")
	}
	if !strings.Contains(result.Up, "Melange version: v0.7.3") {
		t.Error("UP missing melange version")
	}
	if !strings.Contains(result.Up, "Schema checksum: abc123") {
		t.Error("UP missing schema checksum")
	}
	if !strings.Contains(result.Up, "Codegen version: 1") {
		t.Error("UP missing codegen version")
	}
	if !strings.Contains(result.Up, "check_doc_viewer()") {
		t.Error("UP missing check function")
	}
	if !strings.Contains(result.Up, "check_permission_bulk()") {
		t.Error("UP missing bulk dispatcher")
	}
	if !strings.Contains(result.Up, "list_doc_viewer_obj()") {
		t.Error("UP missing list objects function")
	}
	if !strings.Contains(result.Up, "list_accessible_subjects()") {
		t.Error("UP missing list subjects dispatcher")
	}
	// Full mode: no orphan section
	if strings.Contains(result.Up, "Drop removed functions") {
		t.Error("UP should not have orphan drops in full mode")
	}
	// Full mode: no change detection header
	if strings.Contains(result.Up, "Changed functions:") {
		t.Error("UP should not have changed functions header in full mode")
	}

	// DOWN should have all DROPs
	if !strings.Contains(result.Down, "-- Melange Migration (DOWN)") {
		t.Error("DOWN missing header")
	}
	if !strings.Contains(result.Down, "DROP FUNCTION IF EXISTS check_doc_viewer CASCADE") {
		t.Error("DOWN missing specialized drop")
	}
	if !strings.Contains(result.Down, "DROP FUNCTION IF EXISTS check_permission CASCADE") {
		t.Error("DOWN missing dispatcher drop")
	}

	// DOWN: specialized before dispatchers
	specIdx := strings.Index(result.Down, "Drop specialized functions")
	dispIdx := strings.Index(result.Down, "Drop dispatchers")
	if specIdx >= dispIdx {
		t.Error("DOWN should have specialized functions before dispatchers")
	}
}

func TestGenerateMigrationSQL_ComparisonWithOrphans(t *testing.T) {
	gen := GeneratedSQL{
		Functions:  []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() ..."},
		Dispatcher: "CREATE OR REPLACE FUNCTION check_permission() ...",
	}
	listSQL := ListGeneratedSQL{}
	functions := []string{
		"check_doc_viewer",
		"check_doc_viewer_nw",
		"check_permission",
		"check_permission_internal",
		"check_permission_nw",
		"check_permission_nw_internal",
		"check_permission_bulk",
		"list_accessible_objects",
		"list_accessible_subjects",
	}
	opts := MigrationOptions{
		SchemaChecksum: "abc123",
		CodegenVersion: "1",
		PreviousFunctionNames: []string{
			"check_old_type_old_relation",
			"check_old_type_old_relation_nw",
			"check_doc_viewer",
			"check_doc_viewer_nw",
			"check_permission",
			"check_permission_internal",
			"check_permission_nw",
			"check_permission_nw_internal",
			"check_permission_bulk",
			"list_accessible_objects",
			"list_accessible_subjects",
		},
		PreviousSource: "database",
	}

	result := GenerateMigrationSQL(gen, listSQL, functions, opts)

	// UP should include orphan drops
	if !strings.Contains(result.Up, "Drop removed functions") {
		t.Error("UP should have orphan drops section")
	}
	if !strings.Contains(result.Up, "DROP FUNCTION IF EXISTS check_old_type_old_relation CASCADE") {
		t.Error("UP missing orphan drop")
	}
	if !strings.Contains(result.Up, "DROP FUNCTION IF EXISTS check_old_type_old_relation_nw CASCADE") {
		t.Error("UP missing orphan no-wildcard drop")
	}
	if !strings.Contains(result.Up, "Previous state: database") {
		t.Error("UP missing previous source in header")
	}
	// Should NOT drop functions that still exist
	if strings.Contains(result.Up, "DROP FUNCTION IF EXISTS check_doc_viewer CASCADE") {
		t.Error("UP should not drop function that still exists in current schema")
	}
}

func TestGenerateMigrationSQL_ComparisonNoOrphans(t *testing.T) {
	gen := GeneratedSQL{
		Functions: []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() ..."},
	}
	functions := []string{"check_doc_viewer", "check_permission"}

	opts := MigrationOptions{
		PreviousFunctionNames: []string{"check_doc_viewer", "check_permission"},
		PreviousSource:        "git:abc1234",
	}

	result := GenerateMigrationSQL(gen, ListGeneratedSQL{}, functions, opts)

	// No orphan section when function sets are identical
	if strings.Contains(result.Up, "Drop removed functions") {
		t.Error("UP should not have orphan drops when function sets are identical")
	}
}

func TestGenerateMigrationSQL_ChangeDetection(t *testing.T) {
	gen := GeneratedSQL{
		Functions:            []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() CHANGED_BODY"},
		NoWildcardFunctions:  []string{"CREATE OR REPLACE FUNCTION check_doc_viewer_nw() SAME_BODY"},
		Dispatcher:           "CREATE OR REPLACE FUNCTION check_permission() ...",
		DispatcherNoWildcard: "CREATE OR REPLACE FUNCTION check_permission_nw() ...",
	}
	listSQL := ListGeneratedSQL{}

	// Named functions with their SQL
	namedFunctions := []NamedFunction{
		{Name: "check_doc_viewer", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer() CHANGED_BODY"},
		{Name: "check_doc_viewer_nw", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer_nw() SAME_BODY"},
	}

	// Previous checksums: check_doc_viewer has different body, no_wildcard is the same
	previousChecksums := computeCurrentChecksums([]NamedFunction{
		{Name: "check_doc_viewer", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer() OLD_BODY"},
		{Name: "check_doc_viewer_nw", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer_nw() SAME_BODY"},
	})

	functions := []string{
		"check_doc_viewer",
		"check_doc_viewer_nw",
		"check_permission",
		"check_permission_internal",
		"check_permission_nw",
		"check_permission_nw_internal",
		"check_permission_bulk",
		"list_accessible_objects",
		"list_accessible_subjects",
	}

	opts := MigrationOptions{
		Version:               "v0.7.3",
		SchemaChecksum:        "abc123",
		CodegenVersion:        "1",
		PreviousFunctionNames: functions,
		PreviousSource:        "database",
		PreviousChecksums:     previousChecksums,
		NamedFunctions:        namedFunctions,
	}

	result := GenerateMigrationSQL(gen, listSQL, functions, opts)

	// Should include changed function
	if !strings.Contains(result.Up, "CHANGED_BODY") {
		t.Error("UP should include the changed function")
	}
	// Should NOT include unchanged function
	if strings.Contains(result.Up, "SAME_BODY") {
		t.Error("UP should not include unchanged function")
	}
	// Should always include dispatchers
	if !strings.Contains(result.Up, "check_permission()") {
		t.Error("UP should always include dispatchers")
	}
	// Should have change detection header
	if !strings.Contains(result.Up, "Changed functions: 1 of 2") {
		t.Error("UP should show changed function count")
	}
}

func TestGenerateMigrationSQL_ChangeDetection_AllUnchanged(t *testing.T) {
	gen := GeneratedSQL{
		Functions:  []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() SAME"},
		Dispatcher: "CREATE OR REPLACE FUNCTION check_permission() ...",
	}

	namedFunctions := []NamedFunction{
		{Name: "check_doc_viewer", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer() SAME"},
	}
	previousChecksums := computeCurrentChecksums(namedFunctions)

	functions := []string{"check_doc_viewer", "check_permission"}

	opts := MigrationOptions{
		PreviousFunctionNames: functions,
		PreviousSource:        "database",
		PreviousChecksums:     previousChecksums,
		NamedFunctions:        namedFunctions,
	}

	result := GenerateMigrationSQL(gen, ListGeneratedSQL{}, functions, opts)

	// No changed functions section when all match
	if strings.Contains(result.Up, "Changed Functions") {
		t.Error("UP should not have changed functions section when all checksums match")
	}
	// Dispatchers should still be present
	if !strings.Contains(result.Up, "check_permission()") {
		t.Error("UP should always include dispatchers even when no specialized functions changed")
	}
	// Should show 0 changed
	if !strings.Contains(result.Up, "Changed functions: 0 of 1") {
		t.Error("UP should show 0 changed functions")
	}
}

func TestGenerateMigrationSQL_ChangeDetection_NewFunction(t *testing.T) {
	gen := GeneratedSQL{
		Functions:  []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() BODY"},
		Dispatcher: "CREATE OR REPLACE FUNCTION check_permission() ...",
	}

	namedFunctions := []NamedFunction{
		{Name: "check_doc_viewer", SQL: "CREATE OR REPLACE FUNCTION check_doc_viewer() BODY"},
	}
	// Previous had no functions
	previousChecksums := map[string]string{}

	functions := []string{"check_doc_viewer", "check_permission"}

	opts := MigrationOptions{
		PreviousFunctionNames: []string{"check_permission"},
		PreviousSource:        "database",
		PreviousChecksums:     previousChecksums,
		NamedFunctions:        namedFunctions,
	}

	result := GenerateMigrationSQL(gen, ListGeneratedSQL{}, functions, opts)

	// New function should be included
	if !strings.Contains(result.Up, "check_doc_viewer() BODY") {
		t.Error("UP should include new function not in previous checksums")
	}
}

func TestGenerateMigrationSQL_EmptySchema(t *testing.T) {
	gen := GeneratedSQL{}
	listSQL := ListGeneratedSQL{}
	opts := MigrationOptions{
		Version:        "v0.7.3",
		SchemaChecksum: "empty",
		CodegenVersion: "1",
	}

	result := GenerateMigrationSQL(gen, listSQL, nil, opts)

	if !strings.Contains(result.Up, "-- Melange Migration (UP)") {
		t.Error("UP missing header even for empty schema")
	}
	if !strings.Contains(result.Up, "Melange version: v0.7.3") {
		t.Error("UP missing version even for empty schema")
	}
	if !strings.Contains(result.Down, "-- Melange Migration (DOWN)") {
		t.Error("DOWN missing header even for empty schema")
	}
}

func TestGenerateMigrationSQL_DeterministicOutput(t *testing.T) {
	gen := GeneratedSQL{
		Functions: []string{"CREATE OR REPLACE FUNCTION check_doc_viewer() ..."},
	}
	functions := []string{
		"check_doc_viewer",
		"check_doc_viewer_nw",
		"check_permission",
		"check_permission_internal",
	}
	opts := MigrationOptions{
		PreviousFunctionNames: []string{
			"check_z_function",
			"check_a_function",
			"check_doc_viewer",
			"check_doc_viewer_nw",
			"check_permission",
			"check_permission_internal",
		},
		PreviousSource: "file:old.fga",
	}

	result1 := GenerateMigrationSQL(gen, ListGeneratedSQL{}, functions, opts)
	result2 := GenerateMigrationSQL(gen, ListGeneratedSQL{}, functions, opts)

	if result1.Up != result2.Up {
		t.Error("UP output should be deterministic")
	}
	if result1.Down != result2.Down {
		t.Error("DOWN output should be deterministic")
	}

	// Orphans should be sorted
	aIdx := strings.Index(result1.Up, "check_a_function")
	zIdx := strings.Index(result1.Up, "check_z_function")
	if aIdx >= zIdx {
		t.Error("orphan drops should be sorted alphabetically")
	}
}

func TestComputeOrphans(t *testing.T) {
	tests := []struct {
		name     string
		previous []string
		current  []string
		want     int
	}{
		{"no orphans", []string{"a", "b"}, []string{"a", "b"}, 0},
		{"some orphans", []string{"a", "b", "c"}, []string{"a"}, 2},
		{"all orphans", []string{"a", "b"}, []string{}, 2},
		{"empty previous", []string{}, []string{"a"}, 0},
		{"both empty", []string{}, []string{}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeOrphans(tt.previous, tt.current)
			if len(got) != tt.want {
				t.Errorf("got %d orphans, want %d", len(got), tt.want)
			}
		})
	}
}

func TestChangedFunctionNames(t *testing.T) {
	current := map[string]string{
		"fn_same":    "hash_a",
		"fn_changed": "hash_new",
		"fn_new":     "hash_c",
	}
	previous := map[string]string{
		"fn_same":    "hash_a",
		"fn_changed": "hash_old",
		"fn_removed": "hash_d",
	}

	changed := changedFunctionNames(current, previous)

	if changed["fn_same"] {
		t.Error("fn_same should not be marked as changed")
	}
	if !changed["fn_changed"] {
		t.Error("fn_changed should be marked as changed")
	}
	if !changed["fn_new"] {
		t.Error("fn_new should be marked as changed (new function)")
	}
	if changed["fn_removed"] {
		t.Error("fn_removed should not appear in changed (not in current)")
	}
}
