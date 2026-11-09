// Package typescript implements the TypeScript client code generator for melange.
//
// This generator produces type-safe TypeScript code from authorization schemas,
// including object type constants, relation constants, factory functions, and
// the names of the installed accessible functions.
//
// Generated code uses the @pthm/melange runtime package for type definitions.
package typescript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/pthm/melange/internal/clientgen"
	"github.com/pthm/melange/pkg/compiler"
	"github.com/pthm/melange/pkg/schema"
)

func init() {
	clientgen.Register(&Generator{})
}

// Generator implements clientgen.Generator for TypeScript.
type Generator struct{}

// Name returns "typescript" as the runtime identifier.
func (g *Generator) Name() string { return "typescript" }

// DefaultConfig returns default configuration for TypeScript code generation.
func (g *Generator) DefaultConfig() *clientgen.Config {
	return &clientgen.Config{
		Package:        "", // Not used for TypeScript
		RelationFilter: "",
		IDType:         "string", // TypeScript always uses string
		Options:        make(map[string]any),
	}
}

// Generate produces TypeScript client code from the given type definitions.
//
// Returns a multi-file map with keys: "types.ts", "schema.ts", "sql.ts",
// "index.ts".
//
// Generated code includes:
//   - types.ts: ObjectType constants for every type, Relation constants for
//     the relations the config selects (see clientgen.Config), and union types
//   - schema.ts: Factory functions and wildcard constructors
//   - sql.ts: AccessibleFunctions and AccessibleByParentFunctions, the names
//     of the installed accessible functions of the selected relations, keyed
//     by type, relation and link
//   - index.ts: Re-exports for clean imports
//
// It fails when two schema names map to one key or function name, or a
// function name is not a valid identifier or is a name the files import or
// export by name.
func (g *Generator) Generate(types []schema.TypeDefinition, cfg *clientgen.Config) (map[string][]byte, error) {
	if cfg == nil {
		cfg = g.DefaultConfig()
	}
	m, err := clientgen.Resolve(types, cfg)
	if err != nil {
		return nil, err
	}
	if err := declareIdentifiers(m); err != nil {
		return nil, err
	}

	// Generate each file
	files := make(map[string][]byte)

	typesContent, err := g.generateTypes(m.ObjectTypes, m.Relations, cfg)
	if err != nil {
		return nil, err
	}
	files["types.ts"] = typesContent

	schemaContent, err := g.generateSchema(m.ObjectTypes, cfg)
	if err != nil {
		return nil, err
	}
	files["schema.ts"] = schemaContent

	sqlContent, err := g.generateSQL(m)
	if err != nil {
		return nil, err
	}
	files["sql.ts"] = sqlContent

	indexContent, err := g.generateIndex(cfg)
	if err != nil {
		return nil, err
	}
	files["index.ts"] = indexContent

	return files, nil
}

// declareIdentifiers checks the keys of ObjectTypes and Relations and the
// exported function names: each must be an identifier that names one thing,
// and a function name must not be a reserved word or eval or arguments, nor
// one of the names schema.ts imports or index.ts exports by name. sql.ts keys
// are the schema names themselves, quoted when needed, so they cannot
// collide.
func declareIdentifiers(m *clientgen.Model) error {
	keys := func(what string, names []string) error {
		ids := clientgen.NewIdentifiers(identifierPattern.MatchString)
		for _, n := range names {
			if err := ids.Declare(pascalCase(n), fmt.Sprintf("the %s key for %q", what, n)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := keys("ObjectTypes", m.ObjectTypes); err != nil {
		return err
	}
	if err := keys("Relations", m.Relations); err != nil {
		return err
	}
	funcs := clientgen.NewIdentifiers(func(s string) bool { return identifierPattern.MatchString(s) && !reservedWords[s] })
	// A factory named like an import of schema.ts conflicts with it, and one
	// named like an explicit export of index.ts is shadowed by it there.
	for _, d := range [][2]string{
		{"MelangeObject", "the MelangeObject import in schema.ts"},
		{"ObjectTypes", "the ObjectTypes import in schema.ts"},
		{"Relations", "the Relations export in index.ts"},
		{"ObjectType", "the ObjectType export in index.ts"},
		{"Relation", "the Relation export in index.ts"},
		{"AccessibleFunctions", "the AccessibleFunctions export in index.ts"},
		{"AccessibleByParentFunctions", "the AccessibleByParentFunctions export in index.ts"},
	} {
		if err := funcs.Declare(d[0], d[1]); err != nil {
			return err
		}
	}
	for _, t := range m.ObjectTypes {
		if err := funcs.Declare(camelCase(t), fmt.Sprintf("the factory for type %q", t)); err != nil {
			return err
		}
		if err := funcs.Declare("any"+pascalCase(t), fmt.Sprintf("the wildcard factory for type %q", t)); err != nil {
			return err
		}
	}
	return nil
}

// identifierPattern matches the identifiers the generator emits, and the
// object keys sql.ts writes without quotes.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// reservedWords cannot name a function declaration in a module: the
// reserved words of strict mode, and eval and arguments, which strict mode
// does not allow as binding names.
var reservedWords = map[string]bool{
	"await": true, "break": true, "case": true, "catch": true, "class": true, "const": true, "continue": true,
	"debugger": true, "default": true, "delete": true, "do": true, "else": true, "enum": true, "export": true,
	"extends": true, "false": true, "finally": true, "for": true, "function": true, "if": true, "implements": true,
	"import": true, "in": true, "instanceof": true, "interface": true, "let": true, "new": true, "null": true,
	"package": true, "private": true, "protected": true, "public": true, "return": true, "static": true,
	"super": true, "switch": true, "this": true, "throw": true, "true": true, "try": true, "typeof": true,
	"var": true, "void": true, "while": true, "with": true, "yield": true,
	"eval": true, "arguments": true,
}

// generateTypes creates the types.ts file with ObjectTypes and Relations constants.
func (g *Generator) generateTypes(objectTypes, relations []string, cfg *clientgen.Config) ([]byte, error) {
	var buf bytes.Buffer
	ew := &errWriter{w: &buf}

	// Write header
	ew.writeln("/**")
	ew.writeln(" * Generated by melange. DO NOT EDIT.")
	ew.writeln(" *")
	if cfg.Version != "" {
		ew.writef(" * melange version: %s\n", cfg.Version)
	}
	if cfg.SourcePath != "" {
		ew.writef(" * source: %s\n", cfg.SourcePath)
	}
	ew.writeln(" */")
	ew.writeln("")
	ew.writeln("import type { MelangeObject } from '@pthm/melange';")
	ew.writeln("")

	// Write ObjectTypes constant
	ew.writeln("/**")
	ew.writeln(" * ObjectTypes contains all object type constants from the schema.")
	ew.writeln(" */")
	ew.writeln("export const ObjectTypes = {")
	for _, t := range objectTypes {
		constName := pascalCase(t)
		ew.writef("  %s: %q,\n", constName, t)
	}
	ew.writeln("} as const;")
	ew.writeln("")

	// Write ObjectType union type
	ew.writeln("/**")
	ew.writeln(" * ObjectType is a union of all valid object types.")
	ew.writeln(" */")
	ew.writeln("export type ObjectType = (typeof ObjectTypes)[keyof typeof ObjectTypes];")
	ew.writeln("")

	// Write Relations constant
	ew.writeln("/**")
	ew.writeln(" * Relations contains all relation constants from the schema.")
	ew.writeln(" */")
	ew.writeln("export const Relations = {")
	for _, r := range relations {
		constName := pascalCase(r)
		ew.writef("  %s: %q,\n", constName, r)
	}
	ew.writeln("} as const;")
	ew.writeln("")

	// Write Relation union type
	ew.writeln("/**")
	ew.writeln(" * Relation is a union of all valid relations.")
	ew.writeln(" */")
	ew.writeln("export type Relation = (typeof Relations)[keyof typeof Relations];")
	ew.writeln("")

	if ew.err != nil {
		return nil, ew.err
	}

	return buf.Bytes(), nil
}

// generateSchema creates the schema.ts file with factory functions.
func (g *Generator) generateSchema(objectTypes []string, _ *clientgen.Config) ([]byte, error) {
	var buf bytes.Buffer
	ew := &errWriter{w: &buf}

	// Write header
	ew.writeln("/**")
	ew.writeln(" * Generated by melange. DO NOT EDIT.")
	ew.writeln(" */")
	ew.writeln("")
	ew.writeln("import type { MelangeObject } from '@pthm/melange';")
	ew.writeln("import { ObjectTypes } from './types.js';")
	ew.writeln("")

	// Write factory functions
	for _, t := range objectTypes {
		funcName := camelCase(t)
		constName := pascalCase(t)

		ew.writef("/**\n")
		ew.writef(" * %s creates a %s object for authorization checks.\n", funcName, t)
		ew.writef(" */\n")
		ew.writef("export function %s(id: string): MelangeObject {\n", funcName)
		ew.writef("  return { type: ObjectTypes.%s, id };\n", constName)
		ew.writef("}\n")
		ew.writeln("")

		// Write wildcard constructor
		wildcardName := "any" + constName
		ew.writef("/**\n")
		ew.writef(" * %s creates a wildcard %s object matching all instances.\n", wildcardName, t)
		ew.writef(" */\n")
		ew.writef("export function %s(): MelangeObject {\n", wildcardName)
		ew.writef("  return { type: ObjectTypes.%s, id: '*' };\n", constName)
		ew.writef("}\n")
		ew.writeln("")
	}

	if ew.err != nil {
		return nil, ew.err
	}

	return buf.Bytes(), nil
}

// generateSQL creates the sql.ts file with the accessible function names.
// Keys are the schema's type, relation and link names; a map is empty when
// its function family is off.
func (g *Generator) generateSQL(m *clientgen.Model) ([]byte, error) {
	var buf bytes.Buffer
	ew := &errWriter{w: &buf}

	ew.writeln("/**")
	ew.writeln(" * Generated by melange. DO NOT EDIT.")
	ew.writeln(" */")
	ew.writeln("")
	ew.writeln("/**")
	ew.writeln(" * AccessibleFunctions maps a type and relation to the accessible function")
	ew.writeln(" * installed for it, as migrate names it: names longer than 63 bytes are")
	ew.writeln(" * truncated and hashed, so use these instead of building them.")
	ew.writeln(" * accessible_<type>_<relation>(subject_type, subject_id) returns the IDs of")
	ew.writeln(" * the objects on which the subject holds the relation. Names are")
	ew.writeln(" * unqualified: qualify them with the database schema when it is not on")
	ew.writeln(" * search_path.")
	ew.writeln(" */")
	writeNested(ew, "AccessibleFunctions", m.Accessible, false)
	ew.writeln("")
	ew.writeln("/**")
	ew.writeln(" * AccessibleByParentFunctions maps a type, relation and link to the")
	ew.writeln(" * parent-key accessible function installed for it, named like")
	ew.writeln(" * AccessibleFunctions. It returns the IDs of the parent objects through")
	ew.writeln(" * whose link the subject holds the relation, to filter on the column that")
	ew.writeln(" * holds the link.")
	ew.writeln(" */")
	writeNested(ew, "AccessibleByParentFunctions", m.AccessibleByParent, true)

	if ew.err != nil {
		return nil, ew.err
	}
	return buf.Bytes(), nil
}

// writeNested writes fns, sorted by type, relation and link, as a const
// object nested by type and relation, and by link when byLink is set.
func writeNested(ew *errWriter, name string, fns []compiler.AccessibleFunction, byLink bool) {
	if len(fns) == 0 {
		ew.writef("export const %s = {} as const;\n", name)
		return
	}
	ew.writef("export const %s = {\n", name)
	for i, f := range fns {
		newType := i == 0 || fns[i-1].ObjectType != f.ObjectType
		newRelation := newType || fns[i-1].Relation != f.Relation
		if byLink && newRelation && i > 0 {
			ew.writeln("    },")
		}
		if newType {
			if i > 0 {
				ew.writeln("  },")
			}
			ew.writef("  %s: {\n", objectKey(f.ObjectType))
		}
		if !byLink {
			ew.writef("    %s: %s,\n", objectKey(f.Relation), jsString(f.Name))
			continue
		}
		if newRelation {
			ew.writef("    %s: {\n", objectKey(f.Relation))
		}
		ew.writef("      %s: %s,\n", objectKey(f.Link), jsString(f.Name))
	}
	if byLink {
		ew.writeln("    },")
	}
	ew.writeln("  },")
	ew.writeln("} as const;")
}

// objectKey returns name as an object literal key, quoted unless it is an
// identifier. __proto__ is computed: written plainly or quoted it sets the
// object's prototype instead of a property.
func objectKey(name string) string {
	switch {
	case name == "__proto__":
		return "[" + jsString(name) + "]"
	case identifierPattern.MatchString(name):
		return name
	default:
		return jsString(name)
	}
}

// jsString returns s as a double-quoted JavaScript string literal.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// A string always marshals.
		panic(err)
	}
	return string(b)
}

// generateIndex creates the index.ts file with re-exports.
func (g *Generator) generateIndex(_ *clientgen.Config) ([]byte, error) {
	var buf bytes.Buffer
	ew := &errWriter{w: &buf}

	// Write header
	ew.writeln("/**")
	ew.writeln(" * Generated by melange. DO NOT EDIT.")
	ew.writeln(" */")
	ew.writeln("")
	ew.writeln("export { ObjectTypes, Relations } from './types.js';")
	ew.writeln("export type { ObjectType, Relation } from './types.js';")
	ew.writeln("export * from './schema.js';")
	ew.writeln("export { AccessibleFunctions, AccessibleByParentFunctions } from './sql.js';")
	ew.writeln("")

	if ew.err != nil {
		return nil, ew.err
	}

	return buf.Bytes(), nil
}

// errWriter wraps a bytes.Buffer and captures the first error.
type errWriter struct {
	w   *bytes.Buffer
	err error
}

func (ew *errWriter) writeln(s string) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintln(ew.w, s)
}

func (ew *errWriter) writef(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

// pascalCase converts snake_case to PascalCase.
// Examples: "user" -> "User", "pull_request" -> "PullRequest"
func pascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// camelCase converts snake_case to camelCase.
// Examples: "user" -> "user", "pull_request" -> "pullRequest"
func camelCase(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if i == 0 {
			// First part stays lowercase
			parts[i] = p
		} else if p != "" {
			// Subsequent parts are capitalized
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}
