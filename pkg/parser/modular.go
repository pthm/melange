package parser

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/openfga/language/pkg/go/transformer"

	"github.com/pthm/melange/pkg/schema"
)

// manifestData holds the parsed manifest and the module files it references.
type manifestData struct {
	Raw           []byte                   // Original manifest bytes
	SchemaVersion string                   // Schema version from the manifest
	Modules       []transformer.ModuleFile // Module files in manifest order
}

// readManifest reads an fga.mod manifest and all referenced module files.
// Shared by ParseModularSchema (which compiles modules) and ReadManifestContents
// (which concatenates raw contents for hashing).
func readManifest(manifestPath string) (*manifestData, error) {
	raw, err := os.ReadFile(manifestPath) //nolint:gosec // path is from trusted source
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}

	schemaVersion, paths, err := ParseManifestEntries(string(raw))
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Dir(manifestPath)
	modules := make([]transformer.ModuleFile, 0, len(paths))

	for _, p := range paths {
		fullPath := filepath.Join(baseDir, p)
		content, err := os.ReadFile(fullPath) //nolint:gosec // path is from trusted source
		if err != nil {
			return nil, fmt.Errorf("reading module %s: %w", p, err)
		}
		modules = append(modules, transformer.ModuleFile{
			Name:     p,
			Contents: string(content),
		})
	}

	return &manifestData{
		Raw:           raw,
		SchemaVersion: schemaVersion,
		Modules:       modules,
	}, nil
}

// ParseModularSchemaFromStrings parses pre-read module contents and merges
// them into unified type definitions. Useful for testing and embedded schemas
// where module files are already in memory.
//
// Module names are sorted for deterministic processing order.
func ParseModularSchemaFromStrings(modules map[string]string, schemaVersion string) ([]schema.TypeDefinition, error) {
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)

	moduleFiles := make([]transformer.ModuleFile, 0, len(modules))
	for _, name := range names {
		moduleFiles = append(moduleFiles, transformer.ModuleFile{
			Name:     name,
			Contents: modules[name],
		})
	}

	model, err := transformer.TransformModuleFilesToModel(moduleFiles, schemaVersion)
	if err != nil {
		return nil, fmt.Errorf("compiling modules: %w", err)
	}

	return convertModel(model), nil
}

// ParseManifestEntries parses an fga.mod manifest string and returns the
// schema version and relative paths of all referenced module files.
// This enables callers to read a manifest from any source (filesystem, git,
// etc.) and then fetch module files individually.
func ParseManifestEntries(manifestContent string) (schemaVersion string, paths []string, err error) {
	modFile, err := transformer.TransformModFile(manifestContent)
	if err != nil {
		return "", nil, fmt.Errorf("parsing manifest: %w", err)
	}

	paths = make([]string, 0, len(modFile.Contents.Value))
	for _, entry := range modFile.Contents.Value {
		paths = append(paths, entry.Value)
	}

	return modFile.Schema.Value, paths, nil
}

// ReadManifestContents reads an fga.mod manifest and all referenced module
// files, returning their concatenated contents as a single byte slice.
// The output is deterministic (files ordered as listed in the manifest)
// and suitable for content hashing in migration skip detection.
func ReadManifestContents(manifestPath string) ([]byte, error) {
	data, err := readManifest(manifestPath)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	buf.Write(data.Raw)

	for _, mod := range data.Modules {
		buf.WriteString("\n---\n")
		buf.WriteString(mod.Name)
		buf.WriteString("\n")
		buf.WriteString(mod.Contents)
	}

	return buf.Bytes(), nil
}
