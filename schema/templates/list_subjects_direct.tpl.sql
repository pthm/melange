{{- /*
  Template for list_subjects function with direct/implied patterns.
  Returns all subjects with access to a specific object.

  This template is used for relations that only have Direct and/or Implied features.
  Complex features (Userset, Recursive, Exclusion, Intersection) require different templates.

  Complex closure relations (with exclusions, etc.) are handled via check_permission_internal.
  Simple closure relations use direct tuple lookup.

  Includes self-candidate logic for userset filters: when querying with a userset filter
  like "document#viewer" on "document:1.viewer", the self-referential "document:1#viewer"
  should be considered if it satisfies the relation via closure.

  Type restriction enforcement:
  - For regular subject types: returns empty if p_subject_type is not allowed
  - For userset filters: type guard applies to direct tuple lookup via WHERE clause
  - Self-candidate path is validated by closure check instead (filter type = object type is valid
    when the filter relation satisfies the requested relation via closure)

  When HasWildcard is false, excludes wildcard tuples (subject_id = '*') from results.
*/ -}}
-- Generated list_subjects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;
    v_filter_relation TEXT;
BEGIN
    -- Check if subject_type is a userset filter (e.g., "document#viewer")
    IF position('#' in p_subject_type) > 0 THEN
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        RETURN QUERY
        -- Direct tuple lookup with simple closure relations
        -- Normalize results to use the filter relation (e.g., group:1#admin -> group:1#member if admin implies member)
        -- Type guard: only return results if filter type is in allowed subject types
        SELECT DISTINCT substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
        FROM melange_tuples t
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.relation IN ({{.RelationList}})
          AND t.subject_type = v_filter_type
          AND v_filter_type IN ({{.AllowedSubjectTypes}})  -- Type guard in WHERE clause
          AND position('#' in t.subject_id) > 0
          AND (
              substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
              OR EXISTS (
                  SELECT 1 FROM melange_relation_closure subj_c
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
{{- if .ComplexClosureRelations }}
        UNION
        -- Complex closure relations: find candidates via tuples, validate via check_permission_internal
{{- range .ComplexClosureRelations }}
        SELECT DISTINCT substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.relation = '{{.}}'
          AND t.subject_type = v_filter_type
          AND v_filter_type IN ({{$.AllowedSubjectTypes}})
          AND position('#' in t.subject_id) > 0
          AND (
              substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
              OR EXISTS (
                  SELECT 1 FROM melange_relation_closure subj_c
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
          AND check_permission_internal(t.subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
        UNION
        -- Self-candidate: when filter type matches object type
        -- e.g., querying document:1.viewer with filter document#writer
        -- should return document:1#writer if writer satisfies the relation
        -- No type guard here - validity comes from the closure check below
        SELECT p_object_id || '#' || v_filter_relation AS subject_id
        WHERE v_filter_type = '{{.ObjectType}}'
          AND EXISTS (
              SELECT 1 FROM melange_relation_closure c
              WHERE c.object_type = '{{.ObjectType}}'
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          );
    ELSE
        -- Guard: return empty if subject type is not allowed by the model
        IF p_subject_type NOT IN ({{.AllowedSubjectTypes}}) THEN
            RETURN;
        END IF;

        -- Regular subject type (no userset filter)
        RETURN QUERY
        SELECT DISTINCT t.subject_id
        FROM melange_tuples t
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.relation IN ({{.RelationList}})
          AND t.subject_type = p_subject_type
{{- if not .HasWildcard }}
          -- Exclude wildcard tuples when model doesn't allow wildcards
          AND t.subject_id != '*'
{{- end }}
{{- if .ComplexClosureRelations }}
        UNION
        -- Complex closure relations: find candidates via tuples, validate via check_permission_internal
{{- range .ComplexClosureRelations }}
        SELECT DISTINCT t.subject_id
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.relation = '{{.}}'
          AND t.subject_type = p_subject_type
{{- if not $.HasWildcard }}
          AND t.subject_id != '*'
{{- end }}
          AND check_permission_internal(p_subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }};
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;
