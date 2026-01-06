{{- /*
  Template for list_objects function with userset patterns.
  Handles relations like: viewer: [group#member] or viewer: [user, group#member]

  This template generates UNION blocks for each userset pattern:
  - Path 1: Direct grants (subject type matches p_subject_type directly)
  - Path N: Via userset membership (subject has relation on the userset's object type)

  For complex userset patterns (where the subject relation has TTU/exclusion/etc.),
  we use check_permission_internal for membership verification.

  Also handles exclusions if present (combined userset + exclusion support).

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
    -- Path 1: Direct tuple lookup with simple closure relations
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
{{- range .UsersetPatterns }}
    UNION
    -- Path: Via {{.SubjectType}}#{{.SubjectRelation}} membership
{{- if .IsComplex }}
    -- Complex userset: use check_permission_internal for membership verification
    -- Note: No type guard needed here because check_permission_internal handles all validation
    -- including userset self-referential checks (e.g., group:1#member checking member on group:1)
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation IN ({{.SourceRelationList}})
      AND t.subject_type = '{{.SubjectType}}'
      AND position('#' in t.subject_id) > 0
      AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SubjectRelation}}', '{{.SubjectType}}', split_part(t.subject_id, '#', 1), ARRAY[]::TEXT[]) = 1
{{- if .IsClosurePattern }}
      -- Closure pattern: verify permission via source relation (applies exclusions)
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- else }}
    -- Simple userset: JOIN with membership tuples
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    JOIN melange_tuples m
      ON m.object_type = '{{.SubjectType}}'
      AND m.object_id = split_part(t.subject_id, '#', 1)
      AND m.relation IN ({{.SatisfyingRelationsList}})
      AND m.subject_type = p_subject_type
      AND p_subject_type IN ({{$.AllowedSubjectTypes}})  -- Type guard for userset expansion
{{- if .HasWildcard }}
      AND (m.subject_id = p_subject_id OR m.subject_id = '*')
{{- else }}
      AND m.subject_id = p_subject_id
{{- end }}
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation IN ({{.SourceRelationList}})
      AND t.subject_type = '{{.SubjectType}}'
      AND position('#' in t.subject_id) > 0
      AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if .IsClosurePattern }}
      -- Closure pattern: verify permission via source relation (applies exclusions)
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if $.SimpleExcludedRelations }}
      -- Apply simple exclusions to userset path
{{- range $.SimpleExcludedRelations }}
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
{{- if $.ComplexExcludedRelations }}
      -- Apply complex exclusions to userset path
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if $.ExcludedParentRelations }}
      -- Apply TTU exclusions to userset path
{{- range $.ExcludedParentRelations }}
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
{{- if $.ExcludedIntersectionGroups }}
      -- Apply intersection exclusions to userset path
{{- range $.ExcludedIntersectionGroups }}
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
