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
{{- if .SimpleExcludedRelations }}
      -- Simple exclusions: NOT EXISTS tuple lookup
{{- range .SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{$.ObjectType}}'
            AND excl.object_id = t.object_id
            AND excl.relation = '{{.}}'
            AND excl.subject_type = p_subject_type
            AND (excl.subject_id = p_subject_id OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
      -- Complex exclusions: check_permission_internal call
{{- range .ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if .ExcludedParentRelations }}
      -- TTU exclusions: check_permission_internal for each linked parent
{{- range .ExcludedParentRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples link
          WHERE link.object_type = '{{$.ObjectType}}'
            AND link.object_id = t.object_id
            AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
            AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
            AND check_permission_internal(p_subject_type, p_subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
      )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
      -- Intersection exclusions: AND of check_permission_internal calls
      -- For nested exclusions like "but not (editor but not owner)", ExcludedRelation is set
{{- range .ExcludedIntersectionGroups }}
      AND NOT (
{{- range $i, $part := .Parts }}
{{- if $i }}
          AND
{{- end }}
{{- if $part.ParentRelation }}
          EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{$.ObjectType}}'
                AND link.object_id = t.object_id
                AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- else }}
          (check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
           AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
          )
{{- end }}
{{- end }}
      )
{{- end }}
{{- end }}
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
{{- end }}
{{- end }}
    UNION
    -- Self-candidate: when subject is a userset on the same object type
    -- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'
    -- The object 'document:1' should be considered as a candidate
    -- NOTE: Exclusions DON'T apply to self-referential userset checks.
    -- This is a structural validity check ("does the userset relation satisfy the requested relation"),
    -- not a permission grant. See check_permission_internal for the same logic.
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          -- Verify the userset relation satisfies the requested relation via closure
          SELECT 1 FROM melange_relation_closure c
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
      -- No exclusion checks for self-candidate - this is a structural validity check
END;
$$ LANGUAGE plpgsql STABLE;
