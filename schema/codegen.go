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
	HasDirect    bool
	HasImplied   bool
	HasWildcard  bool
	HasUserset   bool
	HasRecursive bool
	HasExclusion bool

	// Pre-rendered SQL fragments
	DirectCheck    string // EXISTS clause for direct check
	UsersetCheck   string // EXISTS clause(s) for userset check
	ExclusionCheck string // EXISTS clause(s) for exclusion check
	AccessChecks   string // Combined access checks (OR'd together)

	// For recursive (TTU) patterns
	ParentRelations []ParentRelationData

	// For implied relations that need function calls
	ImpliedFunctionCalls []ImpliedFunctionCall
}

// ImpliedFunctionCall represents a function call to a complex implied relation.
// Used when an implied relation has exclusions and can't use simple tuple lookup.
type ImpliedFunctionCall struct {
	FunctionName string // e.g., "check_document_editor"
}

// ParentRelationData contains data for rendering recursive access checks.
type ParentRelationData struct {
	LinkingRelation    string
	ParentFunctionName string
}

// generateCheckFunction generates a specialized check function for a relation.
func generateCheckFunction(a RelationAnalysis) (string, error) {
	data, err := buildCheckFunctionData(a)
	if err != nil {
		return "", fmt.Errorf("building template data for %s.%s: %w", a.ObjectType, a.Relation, err)
	}

	// Choose template based on whether we need PL/pgSQL
	templateName := "check_sql.tpl.sql"
	if a.Features.NeedsPLpgSQL() {
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
		ObjectType:     a.ObjectType,
		Relation:       a.Relation,
		FunctionName:   functionName(a.ObjectType, a.Relation),
		FeaturesString: a.Features.String(),
		HasDirect:      a.Features.HasDirect,
		HasImplied:     a.Features.HasImplied,
		HasWildcard:    a.Features.HasWildcard,
		HasUserset:     a.Features.HasUserset,
		HasRecursive:   a.Features.HasRecursive,
		HasExclusion:   a.Features.HasExclusion,
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
		data.ParentRelations = append(data.ParentRelations, ParentRelationData{
			LinkingRelation:    parent.LinkingRelation,
			ParentFunctionName: functionName(a.ObjectType, parent.Relation),
		})
	}

	// Build function calls for complex implied relations
	data.ImpliedFunctionCalls = buildImpliedFunctionCalls(a)

	return data, nil
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

// buildUsersetCheck renders the userset check SQL fragment.
func buildUsersetCheck(a RelationAnalysis) (string, error) {
	if len(a.UsersetPatterns) == 0 {
		return "FALSE", nil
	}

	var checks []string
	for _, pattern := range a.UsersetPatterns {
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

		var buf bytes.Buffer
		if err := templates.ExecuteTemplate(&buf, "userset_check.tpl.sql", data); err != nil {
			return "", fmt.Errorf("executing userset_check template for %s#%s: %w", pattern.SubjectType, pattern.SubjectRelation, err)
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

// buildExclusionCheck renders the exclusion check SQL fragment.
func buildExclusionCheck(a RelationAnalysis) (string, error) {
	if len(a.ExcludedRelations) == 0 {
		return "FALSE", nil
	}

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

// DispatcherData contains data for rendering dispatcher template.
type DispatcherData struct {
	FunctionName            string
	GenericFunctionName     string
	HasSpecializedFunctions bool
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
