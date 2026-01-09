{{- /*
  Template for list_objects function with exclusion patterns.
  Uses NOT EXISTS for simple exclusions, check_permission_internal for complex exclusions.

  This template handles relations like:
  - viewer: [user] but not blocked (simple exclusion)
  - viewer: writer but not editor (implied with simple exclusion)
  - viewer: writer but not viewer from parent (TTU exclusion)
  - viewer: writer but not (editor and owner) (intersection exclusion)

  The base access path (direct/implied) is handled via tuple lookup with closure,
  then exclusions are applied:
  - Simple excluded relations: NOT EXISTS tuple lookup
  - Complex excluded relations: check_permission_internal call
  - TTU exclusions: check_permission_internal for each linked parent
  - Intersection exclusions: AND of check_permission_internal calls

  Includes self-candidate logic from the direct template.
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    -- Direct tuple lookup with closure-inlined relations
    -- Type guard: only return results if subject type is in allowed subject types
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{.ObjectType}}'
      AND t.relation IN ({{.RelationList}})
      AND t.subject_type = p_subject_type
      AND p_subject_type IN ({{.AllowedSubjectTypes}})  -- Type guard in WHERE clause
      AND {{.SubjectIDCheck}}
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
{{- if .ComplexClosureRelations }}
    UNION
    -- Complex closure relations: find candidates via tuples, validate via check_permission_internal
    -- These relations have exclusions or other complex features that require full permission check
{{- range .ComplexClosureRelations }}
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation = '{{.}}'
      AND t.subject_type = p_subject_type
      AND p_subject_type IN ({{$.AllowedSubjectTypes}})
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
{{- end }}
{{- end }}
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}
    UNION
    -- Compose with intersection closure relation: {{.}}
    -- Validate with parent relation's check to apply exclusions
    SELECT DISTINCT icr.object_id
    FROM list_{{$.ObjectType}}_{{.}}_objects(p_subject_type, p_subject_id) AS icr
    WHERE check_permission_internal(p_subject_type, p_subject_id, '{{$.Relation}}', '{{$.ObjectType}}', icr.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{template "list_objects_self_candidate.tpl.sql" .}}
      -- No exclusion checks for self-candidate - this is a structural validity check
END;
$$ LANGUAGE plpgsql STABLE;
