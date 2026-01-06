{{- /*
  Template for list_subjects function with exclusion patterns.
  Returns all subjects with access to a specific object, excluding those matching exclusion patterns.

  This template handles relations like:
  - viewer: [user] but not blocked (simple exclusion)
  - viewer: writer but not editor (implied with simple exclusion)
  - viewer: writer but not viewer from parent (TTU exclusion)
  - viewer: writer but not (editor and owner) (intersection exclusion)

  The base access path (direct/implied) is handled via tuple lookup with closure,
  then exclusions are applied to filter out subjects that match the excluded relation.

  Has two code paths:
  1. Userset filter (when p_subject_type contains '#')
  2. Regular subject type

  Both paths apply exclusion checks.
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
        -- Direct tuple lookup with closure-inlined relations
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
{{- if .SimpleExcludedRelations }}
          -- Simple exclusions: NOT EXISTS tuple lookup for the NORMALIZED subject (type#filter_relation)
          -- Check against normalized subject to match generic list_subjects behavior
{{- range .SimpleExcludedRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples excl
              WHERE excl.object_type = '{{$.ObjectType}}'
                AND excl.object_id = p_object_id
                AND excl.relation = '{{.}}'
                AND excl.subject_type = v_filter_type
                AND (excl.subject_id = substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation OR excl.subject_id = '*')
          )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
          -- Complex exclusions: check_permission_internal call
          -- Check against NORMALIZED subject (type#filter_relation) to match generic behavior
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(v_filter_type, substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if .ExcludedParentRelations }}
          -- TTU exclusions for userset filter path
          -- Check against NORMALIZED subject (type#filter_relation) to match generic behavior
{{- range .ExcludedParentRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{$.ObjectType}}'
                AND link.object_id = p_object_id
                AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal(v_filter_type, substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
          -- Intersection exclusions for userset filter path
          -- Check against NORMALIZED subject (type#filter_relation) to match generic behavior
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
                    AND link.object_id = p_object_id
                    AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                    AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                    AND check_permission_internal(v_filter_type, substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- else }}
              (check_permission_internal(v_filter_type, substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
               AND check_permission_internal(v_filter_type, substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
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
          )
{{- if .SimpleExcludedRelations }}
          -- Apply simple exclusions to self-candidate
{{- range .SimpleExcludedRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples excl
              WHERE excl.object_type = '{{$.ObjectType}}'
                AND excl.object_id = p_object_id
                AND excl.relation = '{{.}}'
                AND excl.subject_type = '{{$.ObjectType}}'
                AND (excl.subject_id = p_object_id || '#' || v_filter_relation OR excl.subject_id = '*')
          )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
          -- Apply complex exclusions to self-candidate
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal('{{$.ObjectType}}', p_object_id || '#' || v_filter_relation, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if .ExcludedParentRelations }}
          -- Apply TTU exclusions to self-candidate
{{- range .ExcludedParentRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{$.ObjectType}}'
                AND link.object_id = p_object_id
                AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal('{{$.ObjectType}}', p_object_id || '#' || v_filter_relation, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
          -- Apply intersection exclusions to self-candidate
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
                    AND link.object_id = p_object_id
                    AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                    AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                    AND check_permission_internal('{{$.ObjectType}}', p_object_id || '#' || v_filter_relation, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- else }}
              (check_permission_internal('{{$.ObjectType}}', p_object_id || '#' || v_filter_relation, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
               AND check_permission_internal('{{$.ObjectType}}', p_object_id || '#' || v_filter_relation, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
              )
{{- end }}
{{- end }}
          )
{{- end }}
{{- end }};
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
{{- if .SimpleExcludedRelations }}
          -- Simple exclusions: NOT EXISTS tuple lookup
{{- range .SimpleExcludedRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples excl
              WHERE excl.object_type = '{{$.ObjectType}}'
                AND excl.object_id = p_object_id
                AND excl.relation = '{{.}}'
                AND excl.subject_type = p_subject_type
                AND (excl.subject_id = t.subject_id OR excl.subject_id = '*')
          )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
          -- Complex exclusions: check_permission_internal call
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if .ExcludedParentRelations }}
          -- TTU exclusions
{{- range .ExcludedParentRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{$.ObjectType}}'
                AND link.object_id = p_object_id
                AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal(p_subject_type, t.subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
          -- Intersection exclusions
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
                    AND link.object_id = p_object_id
                    AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                    AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                    AND check_permission_internal(p_subject_type, t.subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- else }}
              (check_permission_internal(p_subject_type, t.subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
               AND check_permission_internal(p_subject_type, t.subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
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
