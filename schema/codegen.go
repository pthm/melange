package schema

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/*.tpl.sql
var templatesFS embed.FS

// templates holds the parsed SQL templates.
var templates *template.Template

func init() {
	var err error
	templates, err = template.ParseFS(templatesFS, "templates/*.tpl.sql")
	if err != nil {
		panic(fmt.Sprintf("failed to parse SQL templates: %v", err))
	}
}

// formatSQLStringList formats a list of strings as a SQL-safe list.
// For example, ["user", "org"] becomes "'user', 'org'".
// Returns empty string if the list is empty.
func formatSQLStringList(items []string) string {
	if len(items) == 0 {
		return ""
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("'%s'", item)
	}
	return strings.Join(quoted, ", ")
}

// GeneratedSQL contains all SQL generated for a schema.
// This is applied atomically during migration.
type GeneratedSQL struct {
	// Functions contains CREATE OR REPLACE FUNCTION statements
	// for each specialized check function.
	Functions []string

	// Dispatcher contains the check_permission dispatcher function
	// that routes to specialized functions.
	Dispatcher string

	// DispatcherNoWildcard contains the check_permission_no_wildcard dispatcher.
	DispatcherNoWildcard string
}

// GenerateSQL generates specialized SQL functions for all relations.
// The generated SQL includes:
//   - Per-relation check functions (check_{type}_{relation})
//   - A dispatcher that routes check_permission to specialized functions
func GenerateSQL(analyses []RelationAnalysis) (GeneratedSQL, error) {
	var result GeneratedSQL

	// Generate specialized function for each relation
	for _, a := range analyses {
		if !a.CanGenerate {
			continue
		}
		fn, err := generateCheckFunction(a)
		if err != nil {
			return GeneratedSQL{}, fmt.Errorf("generating check function: %w", err)
		}
		result.Functions = append(result.Functions, fn)
	}

	// Generate dispatchers
	var err error
	result.Dispatcher, err = generateDispatcher(analyses, false)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating dispatcher: %w", err)
	}
	result.DispatcherNoWildcard, err = generateDispatcher(analyses, true)
	if err != nil {
		return GeneratedSQL{}, fmt.Errorf("generating no-wildcard dispatcher: %w", err)
	}

	return result, nil
}

// functionName returns the name for a specialized check function.
func functionName(objectType, relation string) string {
	return fmt.Sprintf("check_%s_%s", sanitizeIdentifier(objectType), sanitizeIdentifier(relation))
}

// sanitizeIdentifier converts a type/relation name to a valid SQL identifier.
func sanitizeIdentifier(s string) string {
	var result strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			result.WriteRune(c)
		} else {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// CheckFunctionData contains data for rendering check function templates.
type CheckFunctionData struct {
	ObjectType     string
	Relation       string
	FunctionName   string
	FeaturesString string

	// Feature flags
	HasDirect       bool
	HasImplied      bool
	HasWildcard     bool
	HasUserset      bool
	HasRecursive    bool
	HasExclusion    bool
	HasIntersection bool

	// HasStandaloneAccess is true if the relation has access paths outside of intersections.
	// When false and HasIntersection is true, the only access is through intersection groups.
	// For example, "viewer: [user] and writer" has NO standalone access - the [user] is
	// inside the intersection. But "viewer: [user] or (writer and editor)" HAS standalone
	// access via the [user] path.
	HasStandaloneAccess bool

	// Pre-rendered SQL fragments
	DirectCheck    string // EXISTS clause for direct check
	UsersetCheck   string // EXISTS clause(s) for userset check
	ExclusionCheck string // EXISTS clause(s) for exclusion check
	AccessChecks   string // Combined access checks (OR'd together)

	// For recursive (TTU) patterns
	ParentRelations []ParentRelationData

	// For implied relations that need function calls
	ImpliedFunctionCalls []ImpliedFunctionCall

	// For intersection patterns - each group is AND'd, groups are OR'd
	IntersectionGroups []IntersectionGroupData
}

// IntersectionGroupData contains data for a single intersection group.
// All parts within a group must be satisfied (AND semantics).
type IntersectionGroupData struct {
	Parts []IntersectionPartData
}

// IntersectionPartData contains data for a single part of an intersection.
type IntersectionPartData struct {
	// FunctionName is the check function to call (e.g., "check_document_writer")
	FunctionName string

	// IsThis is true if this part is a self-reference ([user] pattern)
	// When true, we check for a direct tuple on the relation being defined
	IsThis bool

	// ThisHasWildcard is true if this "This" part allows wildcard tuples.
	// This is only relevant when IsThis is true. It reflects whether the relation's
	// own direct subject types allow wildcards, NOT whether the relation's overall
	// HasWildcard flag is set (which may include wildcards from closure relations).
	ThisHasWildcard bool

	// HasExclusion is true if this part has a nested exclusion (e.g., "editor but not owner")
	HasExclusion bool

	// ExcludedRelation is the relation to exclude (for nested exclusions)
	ExcludedRelation string

	// IsTTU is true if this part is a tuple-to-userset pattern
	IsTTU bool

	// TTULinkingRelation is the linking relation for TTU patterns (e.g., "parent")
	TTULinkingRelation string

	// TTURelation is the relation to check on the parent for TTU patterns
	TTURelation string
}

// ImpliedFunctionCall represents a function call to a complex implied relation.
// Used when an implied relation has exclusions and can't use simple tuple lookup.
type ImpliedFunctionCall struct {
	FunctionName string // e.g., "check_document_editor"
}

// ParentRelationData contains data for rendering recursive access checks.
type ParentRelationData struct {
	LinkingRelation     string // The relation that links to parent (e.g., "parent", "org")
	ParentRelation      string // The relation to check on the parent (e.g., "viewer", "member")
	AllowedLinkingTypes string // SQL-formatted list of allowed parent types (e.g., "'folder', 'org'")
}

// generateCheckFunction generates a specialized check function for a relation.
func generateCheckFunction(a RelationAnalysis) (string, error) {
	data, err := buildCheckFunctionData(a)
	if err != nil {
		return "", fmt.Errorf("building template data for %s.%s: %w", a.ObjectType, a.Relation, err)
	}

	// Choose template based on whether we need PL/pgSQL.
	// PL/pgSQL is required for:
	// - Recursive patterns (TTU) that need cycle detection
	// - Complex userset patterns that call check_permission_internal and may create cycles
	templateName := "check_sql.tpl.sql"
	if a.Features.NeedsPLpgSQL() || a.HasComplexUsersetPatterns {
		templateName = "check_plpgsql.tpl.sql"
	}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, templateName, data); err != nil {
		return "", fmt.Errorf("executing template %s for %s.%s: %w", templateName, a.ObjectType, a.Relation, err)
	}
	return buf.String(), nil
}

// buildCheckFunctionData constructs template data from RelationAnalysis.
func buildCheckFunctionData(a RelationAnalysis) (CheckFunctionData, error) {
	data := CheckFunctionData{
		ObjectType:      a.ObjectType,
		Relation:        a.Relation,
		FunctionName:    functionName(a.ObjectType, a.Relation),
		FeaturesString:  a.Features.String(),
		HasDirect:       a.Features.HasDirect,
		HasImplied:      a.Features.HasImplied,
		HasWildcard:     a.Features.HasWildcard,
		HasUserset:      a.Features.HasUserset,
		HasRecursive:    a.Features.HasRecursive,
		HasExclusion:    a.Features.HasExclusion,
		HasIntersection: a.Features.HasIntersection,
	}

	// Build SQL fragments
	var err error
	data.DirectCheck, err = buildDirectCheck(a)
	if err != nil {
		return CheckFunctionData{}, fmt.Errorf("building direct check: %w", err)
	}
	data.UsersetCheck, err = buildUsersetCheck(a)
	if err != nil {
		return CheckFunctionData{}, fmt.Errorf("building userset check: %w", err)
	}
	data.ExclusionCheck, err = buildExclusionCheck(a)
	if err != nil {
		return CheckFunctionData{}, fmt.Errorf("building exclusion check: %w", err)
	}

	// Build combined access checks
	var checks []string
	if a.Features.HasDirect || a.Features.HasImplied {
		checks = append(checks, data.DirectCheck)
	}
	if a.Features.HasUserset {
		checks = append(checks, data.UsersetCheck)
	}
	data.AccessChecks = strings.Join(checks, "\n    OR\n    ")

	// Build parent relation data for recursive checks
	for _, parent := range a.ParentRelations {
		// Format allowed linking types as SQL list (e.g., "'group1', 'group2'")
		var allowedTypes string
		if len(parent.AllowedLinkingTypes) > 0 {
			quoted := make([]string, len(parent.AllowedLinkingTypes))
			for i, t := range parent.AllowedLinkingTypes {
				quoted[i] = fmt.Sprintf("'%s'", t)
			}
			allowedTypes = strings.Join(quoted, ", ")
		}
		data.ParentRelations = append(data.ParentRelations, ParentRelationData{
			LinkingRelation:     parent.LinkingRelation,
			ParentRelation:      parent.Relation,
			AllowedLinkingTypes: allowedTypes,
		})
	}

	// Build function calls for complex implied relations
	data.ImpliedFunctionCalls = buildImpliedFunctionCalls(a)

	// Build intersection groups
	data.IntersectionGroups = buildIntersectionGroups(a)

	// Compute HasStandaloneAccess - whether there are access paths outside of intersections.
	// When an intersection contains a "This" pattern (e.g., "viewer: [user] and writer"),
	// the direct types are constrained by the intersection and should NOT be treated as
	// standalone access paths.
	data.HasStandaloneAccess = computeHasStandaloneAccess(a, data.IntersectionGroups)

	return data, nil
}

// computeHasStandaloneAccess determines if the relation has access paths outside of intersections.
func computeHasStandaloneAccess(a RelationAnalysis, intersectionGroups []IntersectionGroupData) bool {
	// If no intersection, all access paths are standalone
	if !a.Features.HasIntersection {
		return a.Features.HasDirect || a.Features.HasImplied || a.Features.HasUserset || a.Features.HasRecursive
	}

	// Check if any intersection group has a "This" part, meaning direct access is
	// constrained by the intersection rather than being standalone.
	hasIntersectionWithThis := false
	for _, group := range intersectionGroups {
		for _, part := range group.Parts {
			if part.IsThis {
				hasIntersectionWithThis = true
				break
			}
		}
		if hasIntersectionWithThis {
			break
		}
	}

	// If direct types are inside an intersection (This pattern), don't count them as standalone.
	// Userset patterns from subject type restrictions (e.g., [group#member]) are also part of
	// the "This" pattern, so they shouldn't be standalone either.
	// Check for other standalone access paths (implied, recursive).
	hasStandaloneDirect := a.Features.HasDirect && !hasIntersectionWithThis
	hasStandaloneImplied := a.Features.HasImplied
	hasStandaloneUserset := a.Features.HasUserset && !hasIntersectionWithThis
	hasStandaloneRecursive := a.Features.HasRecursive

	return hasStandaloneDirect || hasStandaloneImplied || hasStandaloneUserset || hasStandaloneRecursive
}

// buildIntersectionGroups creates intersection group data from RelationAnalysis.
func buildIntersectionGroups(a RelationAnalysis) []IntersectionGroupData {
	var groups []IntersectionGroupData

	for _, ig := range a.IntersectionGroups {
		group := IntersectionGroupData{}

		for _, part := range ig.Parts {
			partData := IntersectionPartData{
				IsThis:          part.IsThis,
				ThisHasWildcard: part.HasWildcard, // For "This" parts, use the part's own wildcard flag
			}

			if part.ParentRelation != nil {
				// TTU pattern within intersection
				partData.IsTTU = true
				partData.TTULinkingRelation = part.ParentRelation.LinkingRelation
				partData.TTURelation = part.ParentRelation.Relation
			} else if !part.IsThis {
				// Regular relation check - call its function
				partData.FunctionName = functionName(a.ObjectType, part.Relation)
			}

			// Handle nested exclusions
			if part.ExcludedRelation != "" {
				partData.HasExclusion = true
				partData.ExcludedRelation = part.ExcludedRelation
			}

			group.Parts = append(group.Parts, partData)
		}

		if len(group.Parts) > 0 {
			groups = append(groups, group)
		}
	}

	return groups
}

// buildImpliedFunctionCalls creates function call data for complex closure relations.
func buildImpliedFunctionCalls(a RelationAnalysis) []ImpliedFunctionCall {
	var calls []ImpliedFunctionCall
	for _, rel := range a.ComplexClosureRelations {
		calls = append(calls, ImpliedFunctionCall{
			FunctionName: functionName(a.ObjectType, rel),
		})
	}
	return calls
}

// DirectCheckData contains data for rendering direct check template.
type DirectCheckData struct {
	ObjectType        string
	RelationList      string
	SubjectTypeFilter string // e.g., "'user', 'employee'" - allowed subject types
	SubjectIDCheck    string
}

// buildDirectCheck renders the direct check SQL fragment.
func buildDirectCheck(a RelationAnalysis) (string, error) {
	// If there are no allowed subject types, the direct check can never match.
	// Return FALSE to avoid generating invalid SQL like "subject_type IN ()".
	if len(a.AllowedSubjectTypes) == 0 && len(a.DirectSubjectTypes) == 0 {
		return "FALSE", nil
	}

	// Build relation list from self + simple closure relations
	// Complex closure relations are handled via function calls, not tuple lookup
	relations := []string{a.Relation}
	relations = append(relations, a.SimpleClosureRelations...)

	// Fallback to satisfying relations only if no partition was computed at all
	// (for backwards compatibility when closure relations not yet partitioned).
	// If ComplexClosureRelations is non-empty, the partition was computed and
	// we should use only the simple relations (even if that's just self).
	if len(a.SimpleClosureRelations) == 0 && len(a.ComplexClosureRelations) == 0 && len(a.SatisfyingRelations) > 0 {
		relations = a.SatisfyingRelations
	}

	relationList := make([]string, len(relations))
	for i, r := range relations {
		relationList[i] = fmt.Sprintf("'%s'", r)
	}

	// Build subject type filter from allowed types
	// This ensures type restrictions from the model are enforced
	subjectTypes := a.AllowedSubjectTypes
	if len(subjectTypes) == 0 {
		// Fallback to direct subject types if allowed types not computed
		subjectTypes = a.DirectSubjectTypes
	}
	subjectTypeList := make([]string, len(subjectTypes))
	for i, t := range subjectTypes {
		subjectTypeList[i] = fmt.Sprintf("'%s'", t)
	}

	// Build subject_id check (with or without wildcard)
	// When HasWildcard is true: allow wildcard tuples to grant access to any subject
	// When HasWildcard is false: don't match wildcard tuples (they're invalid per the model)
	var subjectIDCheck string
	if a.Features.HasWildcard {
		subjectIDCheck = "(subject_id = p_subject_id OR subject_id = '*')"
	} else {
		// Exclude wildcard tuples - they shouldn't grant access when model doesn't allow wildcards
		subjectIDCheck = "subject_id = p_subject_id AND subject_id != '*'"
	}

	data := DirectCheckData{
		ObjectType:        a.ObjectType,
		RelationList:      strings.Join(relationList, ", "),
		SubjectTypeFilter: strings.Join(subjectTypeList, ", "),
		SubjectIDCheck:    subjectIDCheck,
	}

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "direct_check.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing direct_check template: %w", err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// UsersetCheckData contains data for rendering userset check template.
type UsersetCheckData struct {
	ObjectType      string
	Relation        string
	SubjectType     string
	SubjectRelation string

	// SatisfyingRelationsList is a SQL-formatted list of relations that satisfy SubjectRelation.
	// For example: "'member_c4', 'member_c3', 'member_c2', 'member_c1', 'member'"
	SatisfyingRelationsList string

	// HasWildcard is true if the subject relation supports wildcards.
	// When true, the membership check should also match subject_id = '*'.
	HasWildcard bool
}

// ComplexUsersetCheckData contains data for rendering complex userset check template.
// Used when the userset closure contains relations with complex features.
type ComplexUsersetCheckData struct {
	ObjectType      string
	Relation        string
	SubjectType     string
	SubjectRelation string
}

// buildUsersetCheck renders the userset check SQL fragment.
func buildUsersetCheck(a RelationAnalysis) (string, error) {
	if len(a.UsersetPatterns) == 0 {
		return "FALSE", nil
	}

	var checks []string
	for _, pattern := range a.UsersetPatterns {
		var buf bytes.Buffer

		if pattern.IsComplex {
			// Complex pattern: use check_permission_internal to verify membership.
			// This handles cases where the userset closure contains relations with
			// exclusions, usersets, TTU, or intersections.
			data := ComplexUsersetCheckData{
				ObjectType:      a.ObjectType,
				Relation:        a.Relation,
				SubjectType:     pattern.SubjectType,
				SubjectRelation: pattern.SubjectRelation,
			}
			if err := templates.ExecuteTemplate(&buf, "complex_userset_check.tpl.sql", data); err != nil {
				return "", fmt.Errorf("executing complex_userset_check template for %s#%s: %w", pattern.SubjectType, pattern.SubjectRelation, err)
			}
		} else {
			// Simple pattern: use tuple JOIN for membership lookup.
			// Build SQL-formatted list of satisfying relations for the subject relation.
			// For [group#member_c4], if member_c4 is satisfied by member, we generate:
			// membership.relation IN ('member_c4', 'member_c3', 'member_c2', 'member_c1', 'member')
			satisfyingRels := pattern.SatisfyingRelations
			if len(satisfyingRels) == 0 {
				// Fallback: use just the subject relation itself
				satisfyingRels = []string{pattern.SubjectRelation}
			}
			quotedRels := make([]string, len(satisfyingRels))
			for i, rel := range satisfyingRels {
				quotedRels[i] = fmt.Sprintf("'%s'", rel)
			}

			data := UsersetCheckData{
				ObjectType:              a.ObjectType,
				Relation:                a.Relation,
				SubjectType:             pattern.SubjectType,
				SubjectRelation:         pattern.SubjectRelation,
				SatisfyingRelationsList: strings.Join(quotedRels, ", "),
				HasWildcard:             pattern.HasWildcard,
			}
			if err := templates.ExecuteTemplate(&buf, "userset_check.tpl.sql", data); err != nil {
				return "", fmt.Errorf("executing userset_check template for %s#%s: %w", pattern.SubjectType, pattern.SubjectRelation, err)
			}
		}
		checks = append(checks, strings.TrimSpace(buf.String()))
	}

	if len(checks) == 1 {
		return checks[0], nil
	}
	return "(" + strings.Join(checks, " OR ") + ")", nil
}

// ExclusionCheckData contains data for rendering exclusion check template.
type ExclusionCheckData struct {
	ObjectType       string
	ExcludedRelation string
}

// ComplexExclusionCheckData contains data for rendering complex exclusion checks.
// These use check_permission_internal instead of direct tuple lookup.
type ComplexExclusionCheckData struct {
	ObjectType       string
	ExcludedRelation string
}

// TTUExclusionCheckData contains data for rendering TTU exclusion checks.
// These check "but not X from Y" patterns by looking up the linking relation
// and calling check_permission_internal for each linked object.
type TTUExclusionCheckData struct {
	ObjectType          string
	ExcludedRelation    string // The relation to check on the parent (e.g., "viewer")
	LinkingRelation     string // The linking relation (e.g., "parent")
	AllowedLinkingTypes string // SQL-formatted list of allowed parent types (e.g., "'folder', 'org'")
}

// IntersectionExclusionCheckData contains data for rendering intersection exclusion checks.
// These check "but not (A and B)" patterns by ANDing together check_permission_internal calls.
type IntersectionExclusionCheckData struct {
	ObjectType string
	Parts      []string // Relations that must ALL be satisfied for exclusion to apply
}

// buildExclusionCheck renders the exclusion check SQL fragment.
// Simple exclusions use direct tuple lookup; complex exclusions use check_permission_internal.
// TTU exclusions check linked objects; intersection exclusions AND together checks.
func buildExclusionCheck(a RelationAnalysis) (string, error) {
	// Check if there are any exclusions to handle
	hasSimpleOrComplex := len(a.SimpleExcludedRelations) > 0 || len(a.ComplexExcludedRelations) > 0
	hasLegacy := len(a.ExcludedRelations) > 0
	hasTTU := len(a.ExcludedParentRelations) > 0
	hasIntersection := len(a.ExcludedIntersectionGroups) > 0

	if !hasSimpleOrComplex && !hasLegacy && !hasTTU && !hasIntersection {
		return "FALSE", nil
	}

	// Legacy path: use ExcludedRelations if no classification was done
	if !hasSimpleOrComplex && hasLegacy && !hasTTU && !hasIntersection {
		var checks []string
		for _, excl := range a.ExcludedRelations {
			data := ExclusionCheckData{
				ObjectType:       a.ObjectType,
				ExcludedRelation: excl,
			}
			var buf bytes.Buffer
			if err := templates.ExecuteTemplate(&buf, "exclusion_check.tpl.sql", data); err != nil {
				return "", fmt.Errorf("executing exclusion_check template for %s: %w", excl, err)
			}
			checks = append(checks, strings.TrimSpace(buf.String()))
		}
		return strings.Join(checks, " OR "), nil
	}

	var checks []string

	// Simple exclusions: direct tuple lookup
	for _, excl := range a.SimpleExcludedRelations {
		data := ExclusionCheckData{
			ObjectType:       a.ObjectType,
			ExcludedRelation: excl,
		}
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "exclusion_check.tpl.sql", data); err != nil {
			return "", fmt.Errorf("executing exclusion_check template for %s: %w", excl, err)
		}
		checks = append(checks, strings.TrimSpace(buf.String()))
	}

	// Complex exclusions: use check_permission_internal
	for _, excl := range a.ComplexExcludedRelations {
		data := ComplexExclusionCheckData{
			ObjectType:       a.ObjectType,
			ExcludedRelation: excl,
		}
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "complex_exclusion_check.tpl.sql", data); err != nil {
			return "", fmt.Errorf("executing complex_exclusion_check template for %s: %w", excl, err)
		}
		checks = append(checks, strings.TrimSpace(buf.String()))
	}

	// TTU exclusions: "but not X from Y" patterns
	for _, excl := range a.ExcludedParentRelations {
		data := TTUExclusionCheckData{
			ObjectType:          a.ObjectType,
			ExcludedRelation:    excl.Relation,
			LinkingRelation:     excl.LinkingRelation,
			AllowedLinkingTypes: formatSQLStringList(excl.AllowedLinkingTypes),
		}
		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "ttu_exclusion_check.tpl.sql", data); err != nil {
			return "", fmt.Errorf("executing ttu_exclusion_check template for %s from %s: %w", excl.Relation, excl.LinkingRelation, err)
		}
		checks = append(checks, strings.TrimSpace(buf.String()))
	}

	// Intersection exclusions: "but not (A and B)" or "but not (A but not B)" patterns
	for _, group := range a.ExcludedIntersectionGroups {
		var parts []string
		for _, part := range group.Parts {
			if part.ParentRelation != nil {
				// TTU part within intersection exclusion
				data := TTUExclusionCheckData{
					ObjectType:          a.ObjectType,
					ExcludedRelation:    part.ParentRelation.Relation,
					LinkingRelation:     part.ParentRelation.LinkingRelation,
					AllowedLinkingTypes: formatSQLStringList(part.ParentRelation.AllowedLinkingTypes),
				}
				var buf bytes.Buffer
				if err := templates.ExecuteTemplate(&buf, "ttu_exclusion_check.tpl.sql", data); err != nil {
					return "", fmt.Errorf("executing ttu_exclusion_check template for intersection: %w", err)
				}
				parts = append(parts, strings.TrimSpace(buf.String()))
			} else if part.ExcludedRelation != "" {
				// Nested exclusion: "editor but not owner" in the exclusion
				// The exclusion applies when: part.Relation AND NOT part.ExcludedRelation
				// We check: relation is true AND excluded_relation is false
				mainCheck := fmt.Sprintf(
					"check_permission_internal(p_subject_type, p_subject_id, '%s', '%s', p_object_id, p_visited) = 1",
					part.Relation, a.ObjectType)
				excludeCheck := fmt.Sprintf(
					"check_permission_internal(p_subject_type, p_subject_id, '%s', '%s', p_object_id, p_visited) = 0",
					part.ExcludedRelation, a.ObjectType)
				parts = append(parts, "("+mainCheck+" AND "+excludeCheck+")")
			} else {
				// Regular relation part
				data := ComplexExclusionCheckData{
					ObjectType:       a.ObjectType,
					ExcludedRelation: part.Relation,
				}
				var buf bytes.Buffer
				if err := templates.ExecuteTemplate(&buf, "complex_exclusion_check.tpl.sql", data); err != nil {
					return "", fmt.Errorf("executing complex_exclusion_check template for intersection part %s: %w", part.Relation, err)
				}
				parts = append(parts, strings.TrimSpace(buf.String()))
			}
		}
		if len(parts) > 0 {
			// All parts must be true for exclusion to apply (AND)
			checks = append(checks, "("+strings.Join(parts, " AND ")+")")
		}
	}

	if len(checks) == 0 {
		return "FALSE", nil
	}
	return strings.Join(checks, " OR "), nil
}

// DispatcherData contains data for rendering dispatcher template.
type DispatcherData struct {
	FunctionName            string
	GenericFunctionName     string
	HasSpecializedFunctions bool
	IsNoWildcard            bool // True for check_permission_no_wildcard dispatcher
	Cases                   []DispatcherCase
}

// DispatcherCase represents a single CASE WHEN branch in the dispatcher.
type DispatcherCase struct {
	ObjectType        string
	Relation          string
	CheckFunctionName string
}

// generateDispatcher generates the check_permission dispatcher function.
func generateDispatcher(analyses []RelationAnalysis, noWildcard bool) (string, error) {
	data := DispatcherData{
		FunctionName:        "check_permission",
		GenericFunctionName: "check_permission_generic",
		IsNoWildcard:        noWildcard,
	}
	if noWildcard {
		data.FunctionName = "check_permission_no_wildcard"
		data.GenericFunctionName = "check_permission_no_wildcard_generic"
	}

	// Build CASE branches - only for regular dispatcher, not no-wildcard
	// The no-wildcard dispatcher always falls back to generic because
	// the specialized functions include wildcard handling that would
	// incorrectly match wildcard tuples in no-wildcard contexts.
	if !noWildcard {
		for _, a := range analyses {
			if !a.CanGenerate {
				continue
			}
			data.Cases = append(data.Cases, DispatcherCase{
				ObjectType:        a.ObjectType,
				Relation:          a.Relation,
				CheckFunctionName: functionName(a.ObjectType, a.Relation),
			})
		}
	}

	data.HasSpecializedFunctions = len(data.Cases) > 0

	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "dispatcher.tpl.sql", data); err != nil {
		return "", fmt.Errorf("executing dispatcher template: %w", err)
	}
	return buf.String(), nil
}
