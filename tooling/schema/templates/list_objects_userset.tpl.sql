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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
    UNION
    -- Direct userset subject matching: when the subject IS a userset (e.g., group:fga#member)
    -- and there's a tuple with that userset (or a satisfying relation) as the subject
    -- This handles cases like: tuple(document:1, viewer, group:fga#member_c4) queried by group:fga#member
    -- where member satisfies member_c4 via the closure (member → member_c1 → ... → member_c4)
    -- No type guard - we're matching userset subjects via closure
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{.ObjectType}}'
      AND t.relation IN ({{.RelationList}})
      AND t.subject_type = p_subject_type
      AND position('#' in p_subject_id) > 0  -- Subject is a userset
      AND position('#' in t.subject_id) > 0  -- Tuple subject is also a userset
      AND (
          -- Exact match (same object and relation)
          t.subject_id = p_subject_id
          OR (
              -- Same object, and query's relation satisfies tuple's relation via closure
              -- e.g., query 'fga#member' matches tuple 'fga#member_c4' if member satisfies member_c4
              split_part(t.subject_id, '#', 1) = split_part(p_subject_id, '#', 1)
              AND EXISTS (
                  SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
                  WHERE c.object_type = p_subject_type
                    AND c.relation = split_part(t.subject_id, '#', 2)
                    AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
              )
          )
      )
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}
    UNION
    -- Compose with intersection closure relation: {{.}}
    SELECT * FROM list_{{$.ObjectType}}_{{.}}_objects(p_subject_type, p_subject_id)
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
{{- end }}
{{template "list_objects_self_candidate.tpl.sql" .}}
      -- No exclusion checks for self-candidate - this is a structural validity check
END;
$$ LANGUAGE plpgsql STABLE;
