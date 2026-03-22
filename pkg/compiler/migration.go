package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// MigrationSQL holds the UP and DOWN SQL for a single versioned migration.
// UP contains CREATE OR REPLACE statements for all current functions, plus DROP
// statements for any orphaned functions detected via comparison mode.
// DOWN drops all functions installed by UP; restoring a prior state requires
// re-applying that version's UP migration.
type MigrationSQL struct {
	Up   string
	Down string
}

// MigrationOptions controls migration SQL generation.
type MigrationOptions struct {
	// Version is the melange CLI/library version (e.g., "v0.7.3").
	Version string
	// SchemaChecksum is the SHA256 of the current schema content.
	SchemaChecksum string
	// CodegenVersion is the codegen version string.
	CodegenVersion string
	// PreviousFunctionNames enables orphan-aware output when non-nil.
	PreviousFunctionNames []string
	// PreviousSource describes where previous state came from (for header comment).
	// e.g., "database", "git:abc1234", "file:old.fga"
	PreviousSource string
	// PreviousChecksums maps function_name → SHA256(sql_body) from the previous state.
	// When set alongside NamedFunctions, only functions whose SQL body has changed (or
	// are new) are included in the UP migration. Dispatchers are always included
	// regardless. If nil, all specialized functions are emitted unconditionally.
	PreviousChecksums map[string]string
	// NamedFunctions pairs each specialized function name with its SQL body for
	// checksum comparison. Required alongside PreviousChecksums for change detection;
	// if either is absent, change detection is skipped and all functions are emitted.
	NamedFunctions []NamedFunction
}

// GenerateMigrationSQL is the terminal step of the generate migration pipeline.
// It assembles UP and DOWN SQL from functions already compiled by GenerateSQL and
// GenerateListSQL. Call CollectFunctionNames and CollectNamedFunctions first to
// populate expectedFunctions and opts.NamedFunctions respectively.
//
// When opts.PreviousFunctionNames is nil the output includes every function
// (full mode). When set, orphaned functions are dropped in UP and checksum-based
// filtering is applied if opts.PreviousChecksums is also provided.
func GenerateMigrationSQL(
	generatedSQL GeneratedSQL,
	listSQL ListGeneratedSQL,
	expectedFunctions []string,
	opts MigrationOptions,
) MigrationSQL {
	up := generateUpSQL(generatedSQL, listSQL, expectedFunctions, opts)
	down := generateDownSQL(expectedFunctions)
	return MigrationSQL{Up: up, Down: down}
}

// computeCurrentChecksums computes SHA256 hashes for each named function.
func computeCurrentChecksums(namedFunctions []NamedFunction) map[string]string {
	checksums := make(map[string]string, len(namedFunctions))
	for _, nf := range namedFunctions {
		h := sha256.Sum256([]byte(nf.SQL))
		checksums[nf.Name] = hex.EncodeToString(h[:])
	}
	return checksums
}

// changedFunctionNames returns the names of functions that are new or have a
// different SQL body compared to previous. Functions removed from the current
// schema are absent from current and therefore absent from the result — they
// must be handled separately as orphans via computeOrphans.
func changedFunctionNames(current, previous map[string]string) map[string]bool {
	changed := make(map[string]bool)
	for name, checksum := range current {
		prevChecksum, existed := previous[name]
		if !existed || prevChecksum != checksum {
			changed[name] = true
		}
	}
	return changed
}

func generateUpSQL(
	generatedSQL GeneratedSQL,
	listSQL ListGeneratedSQL,
	expectedFunctions []string,
	opts MigrationOptions,
) string {
	var b strings.Builder

	// Determine which specialized functions changed (nil = include all)
	var changed map[string]bool
	if opts.PreviousChecksums != nil && len(opts.NamedFunctions) > 0 {
		currentChecksums := computeCurrentChecksums(opts.NamedFunctions)
		changed = changedFunctionNames(currentChecksums, opts.PreviousChecksums)
	}

	// Header
	b.WriteString("-- Melange Migration (UP)\n")
	if opts.Version != "" {
		fmt.Fprintf(&b, "-- Melange version: %s\n", opts.Version)
	}
	if opts.SchemaChecksum != "" {
		fmt.Fprintf(&b, "-- Schema checksum: %s\n", opts.SchemaChecksum)
	}
	if opts.CodegenVersion != "" {
		fmt.Fprintf(&b, "-- Codegen version: %s\n", opts.CodegenVersion)
	}
	if opts.PreviousFunctionNames != nil && opts.PreviousSource != "" {
		fmt.Fprintf(&b, "-- Previous state: %s\n", opts.PreviousSource)
	}
	if changed != nil {
		fmt.Fprintf(&b, "-- Changed functions: %d of %d\n", len(changed), len(opts.NamedFunctions))
	}
	b.WriteString("\n")

	// Orphan drops (only in comparison mode)
	if opts.PreviousFunctionNames != nil {
		orphans := computeOrphans(opts.PreviousFunctionNames, expectedFunctions)
		if len(orphans) > 0 {
			writeSectionHeader(&b, "Drop removed functions")
			for _, fn := range orphans {
				fmt.Fprintf(&b, "DROP FUNCTION IF EXISTS %s CASCADE;\n", fn)
			}
			b.WriteString("\n")
		}
	}

	// When doing change detection, use named functions to filter
	if changed != nil {
		writeChangedFunctions(&b, opts.NamedFunctions, changed)
	} else {
		writeAllFunctions(&b, generatedSQL, listSQL)
	}

	// Dispatchers are always included (they reference all relations)
	writeDispatchers(&b, generatedSQL, listSQL)

	return b.String()
}

// writeFunctionSection writes a labeled section of SQL functions if non-empty.
func writeFunctionSection(b *strings.Builder, label string, functions []string) {
	if len(functions) == 0 {
		return
	}
	writeSectionHeader(b, fmt.Sprintf("%s (%d functions)", label, len(functions)))
	for _, fn := range functions {
		fmt.Fprintf(b, "%s\n\n", fn)
	}
}

// writeAllFunctions writes all specialized functions (no filtering).
func writeAllFunctions(b *strings.Builder, generatedSQL GeneratedSQL, listSQL ListGeneratedSQL) {
	writeFunctionSection(b, "Check Functions", generatedSQL.Functions)
	writeFunctionSection(b, "No-Wildcard Check Functions", generatedSQL.NoWildcardFunctions)
	writeFunctionSection(b, "List Objects Functions", listSQL.ListObjectsFunctions)
	writeFunctionSection(b, "List Subjects Functions", listSQL.ListSubjectsFunctions)
}

// writeChangedFunctions writes only the functions that have changed.
func writeChangedFunctions(b *strings.Builder, namedFunctions []NamedFunction, changed map[string]bool) {
	var changedFns []NamedFunction
	for _, nf := range namedFunctions {
		if changed[nf.Name] {
			changedFns = append(changedFns, nf)
		}
	}

	if len(changedFns) == 0 {
		return
	}

	writeSectionHeader(b, fmt.Sprintf("Changed Functions (%d functions)", len(changedFns)))
	for _, nf := range changedFns {
		fmt.Fprintf(b, "%s\n\n", nf.SQL)
	}
}

// writeSectionHeader writes a SQL section separator with a label.
func writeSectionHeader(b *strings.Builder, label string) {
	b.WriteString("-- ============================================================\n")
	fmt.Fprintf(b, "-- %s\n", label)
	b.WriteString("-- ============================================================\n\n")
}

// writeDispatchers writes all dispatcher functions (always included).
func writeDispatchers(b *strings.Builder, generatedSQL GeneratedSQL, listSQL ListGeneratedSQL) {
	checkDispatchers := collectNonEmpty(generatedSQL.Dispatcher, generatedSQL.DispatcherNoWildcard, generatedSQL.BulkDispatcher)
	if len(checkDispatchers) > 0 {
		writeSectionHeader(b, "Check Dispatchers")
		for _, d := range checkDispatchers {
			fmt.Fprintf(b, "%s\n\n", d)
		}
	}

	listDispatchers := collectNonEmpty(listSQL.ListObjectsDispatcher, listSQL.ListSubjectsDispatcher)
	if len(listDispatchers) > 0 {
		writeSectionHeader(b, "List Dispatchers")
		for _, d := range listDispatchers {
			fmt.Fprintf(b, "%s\n\n", d)
		}
	}
}

// collectNonEmpty returns only the non-empty strings from the input.
func collectNonEmpty(values ...string) []string {
	var result []string
	for _, v := range values {
		if v != "" {
			result = append(result, v)
		}
	}
	return result
}

// dispatcherFunctionNames lists the well-known dispatcher functions in the
// stable order used for DOWN migrations. Specialized functions are handled
// separately in generateDownSQL.
var dispatcherFunctionNames = []string{
	"check_permission",
	"check_permission_internal",
	"check_permission_no_wildcard",
	"check_permission_no_wildcard_internal",
	"check_permission_bulk",
	"list_accessible_objects",
	"list_accessible_subjects",
}

func generateDownSQL(expectedFunctions []string) string {
	var b strings.Builder

	b.WriteString("-- Melange Migration (DOWN)\n")
	b.WriteString("-- To restore a previous version, apply that version's UP migration.\n\n")

	// Separate specialized functions from dispatchers
	dispatcherSet := make(map[string]bool, len(dispatcherFunctionNames))
	for _, name := range dispatcherFunctionNames {
		dispatcherSet[name] = true
	}

	var specialized []string
	var dispatchers []string
	for _, fn := range expectedFunctions {
		if dispatcherSet[fn] {
			dispatchers = append(dispatchers, fn)
		} else {
			specialized = append(specialized, fn)
		}
	}

	sort.Strings(specialized)
	// Keep dispatchers in a stable order matching dispatcherFunctionNames
	dispatcherPresent := make(map[string]bool, len(dispatchers))
	for _, fn := range dispatchers {
		dispatcherPresent[fn] = true
	}
	sortedDispatchers := make([]string, 0, len(dispatchers))
	for _, d := range dispatcherFunctionNames {
		if dispatcherPresent[d] {
			sortedDispatchers = append(sortedDispatchers, d)
		}
	}

	if len(specialized) > 0 {
		b.WriteString("-- Drop specialized functions\n")
		for _, fn := range specialized {
			fmt.Fprintf(&b, "DROP FUNCTION IF EXISTS %s CASCADE;\n", fn)
		}
		b.WriteString("\n")
	}

	if len(sortedDispatchers) > 0 {
		b.WriteString("-- Drop dispatchers\n")
		for _, fn := range sortedDispatchers {
			fmt.Fprintf(&b, "DROP FUNCTION IF EXISTS %s CASCADE;\n", fn)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// computeOrphans returns function names that exist in previous but not in current,
// sorted for deterministic output.
func computeOrphans(previous, current []string) []string {
	currentSet := make(map[string]bool, len(current))
	for _, fn := range current {
		currentSet[fn] = true
	}

	var orphans []string
	for _, fn := range previous {
		if !currentSet[fn] {
			orphans = append(orphans, fn)
		}
	}
	sort.Strings(orphans)
	return orphans
}
