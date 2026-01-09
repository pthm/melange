{{- /*
  Template for list_subjects function with intersection patterns.

  This template handles intersection patterns by gathering candidate subjects
  and filtering with check_permission, which handles the intersection logic.

  For intersection patterns like "viewer: writer and editor", we need to find
  subjects that satisfy ALL parts of the intersection for the given object.

  The approach:
  1. Gather candidate subjects from all relevant tuple sources
  2. Filter each candidate with check_permission (which handles intersection)

  This leverages the existing intersection logic in check functions rather than
  duplicating it in set-based operations.
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
    -- Check if p_subject_type is a userset filter (contains '#')
    IF position('#' in p_subject_type) > 0 THEN
        -- Parse userset filter
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        -- Userset filter: find userset tuples and filter with check_permission
        RETURN QUERY
        WITH userset_candidates AS (
            -- Direct userset tuples on this object
            SELECT DISTINCT
                substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.AllSatisfyingRelations}})
              AND t.subject_type = v_filter_type
              AND position('#' in t.subject_id) > 0
              AND (
                  substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
                  OR EXISTS (
                      SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
                      WHERE subj_c.object_type = v_filter_type
                        AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                        AND subj_c.satisfying_relation = v_filter_relation
                  )
              )
{{- range .IntersectionGroups }}
{{- range .Parts }}
{{- if not .IsThis }}
{{- if .ParentRelation }}
            UNION
            -- Userset candidates via TTU: {{.ParentRelation.LinkingRelation}} -> {{.ParentRelation.Relation}}
            SELECT DISTINCT
                substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_tuples link
            JOIN melange_tuples pt
              ON pt.object_type = link.subject_type
              AND pt.object_id = link.subject_id
              AND pt.subject_type = v_filter_type
              AND position('#' in pt.subject_id) > 0
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.ParentRelation.LinkingRelation}}'
              AND (
                  substring(pt.subject_id from position('#' in pt.subject_id) + 1) = v_filter_relation
                  OR EXISTS (
                      SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
                      WHERE subj_c.object_type = v_filter_type
                        AND subj_c.relation = substring(pt.subject_id from position('#' in pt.subject_id) + 1)
                        AND subj_c.satisfying_relation = v_filter_relation
                  )
              )
{{- else }}
            UNION
            -- Userset candidates from intersection part: {{.Relation}}
            SELECT DISTINCT
                substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation = '{{.Relation}}'
              AND t.subject_type = v_filter_type
              AND position('#' in t.subject_id) > 0
              AND (
                  substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
                  OR EXISTS (
                      SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
                      WHERE subj_c.object_type = v_filter_type
                        AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                        AND subj_c.satisfying_relation = v_filter_relation
                  )
              )
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- range .ParentRelations }}
            UNION
            -- Userset candidates via TTU: {{.LinkingRelation}} -> {{.Relation}}
            SELECT DISTINCT
                substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_tuples link
            JOIN melange_tuples pt
              ON pt.object_type = link.subject_type
              AND pt.object_id = link.subject_id
              AND pt.subject_type = v_filter_type
              AND position('#' in pt.subject_id) > 0
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
              AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
              AND (
                  substring(pt.subject_id from position('#' in pt.subject_id) + 1) = v_filter_relation
                  OR EXISTS (
                      SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
                      WHERE subj_c.object_type = v_filter_type
                        AND subj_c.relation = substring(pt.subject_id from position('#' in pt.subject_id) + 1)
                        AND subj_c.satisfying_relation = v_filter_relation
                  )
              )
{{- end }}
        )
        SELECT DISTINCT c.subject_id
        FROM userset_candidates c
        WHERE check_permission(v_filter_type, c.subject_id, '{{.Relation}}', '{{.ObjectType}}', p_object_id) = 1

        UNION

        -- Self-referential userset
        SELECT p_object_id || '#' || v_filter_relation AS subject_id
        WHERE v_filter_type = '{{.ObjectType}}'
          AND EXISTS (
              SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
              WHERE c.object_type = '{{.ObjectType}}'
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          );
    ELSE
        -- Regular subject type: gather candidates and filter with check_permission
        -- Guard: return empty if subject type is not allowed by the model
        IF p_subject_type NOT IN ({{.AllowedSubjectTypes}}) THEN
            RETURN;
        END IF;

        RETURN QUERY
        WITH subject_candidates AS (
            -- Candidates from direct tuples on this object
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.AllSatisfyingRelations}})
              AND t.subject_type = p_subject_type
{{- if not .HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
{{- range .IntersectionGroups }}
{{- range .Parts }}
{{- if not .IsThis }}
{{- if .ParentRelation }}
            UNION
            -- Candidates via TTU in intersection: {{.ParentRelation.LinkingRelation}} -> {{.ParentRelation.Relation}}
            SELECT DISTINCT pt.subject_id
            FROM melange_tuples link
            JOIN melange_tuples pt
              ON pt.object_type = link.subject_type
              AND pt.object_id = link.subject_id
              AND pt.subject_type = p_subject_type
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.ParentRelation.LinkingRelation}}'
{{- if not $.HasWildcard }}
              AND pt.subject_id != '*'
{{- end }}
{{- else }}
            UNION
            -- Candidates from intersection part: {{.Relation}}
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation = '{{.Relation}}'
              AND t.subject_type = p_subject_type
{{- if not $.HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- range .UsersetPatterns }}
            UNION
            -- Candidates from userset: {{.SubjectType}}#{{.SubjectRelation}}
            SELECT DISTINCT m.subject_id
            FROM melange_tuples t
            JOIN melange_tuples m
              ON m.object_type = '{{.SubjectType}}'
              AND m.object_id = split_part(t.subject_id, '#', 1)
              AND m.relation IN ({{.SatisfyingRelationsList}})
              AND m.subject_type = p_subject_type
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.SourceRelationList}})
              AND t.subject_type = '{{.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if not $.HasWildcard }}
              AND m.subject_id != '*'
{{- end }}
{{- end }}
{{- range .ParentRelations }}
            UNION
            -- Candidates via TTU: {{.LinkingRelation}} -> {{.Relation}}
            SELECT DISTINCT pt.subject_id
            FROM melange_tuples link
            JOIN melange_tuples pt
              ON pt.object_type = link.subject_type
              AND pt.object_id = link.subject_id
              AND pt.subject_type = p_subject_type
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
              AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
{{- if not $.HasWildcard }}
              AND pt.subject_id != '*'
{{- end }}
{{- end }}
            UNION
            -- Candidates from global subject pool
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.subject_type = p_subject_type
{{- if not .HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
        ),
        filtered_candidates AS (
            SELECT DISTINCT c.subject_id
            FROM subject_candidates c
            WHERE check_permission(p_subject_type, c.subject_id, '{{.Relation}}', '{{.ObjectType}}', p_object_id) = 1
        )
{{- if .HasWildcard }},
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM filtered_candidates fc WHERE fc.subject_id = '*') AS has_wildcard
        )
        SELECT fc.subject_id
        FROM filtered_candidates fc
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard)
           OR (fc.subject_id = '*')
           OR (
               fc.subject_id != '*'
               AND check_permission_no_wildcard(
                   p_subject_type,
                   fc.subject_id,
                   '{{.Relation}}',
                   '{{.ObjectType}}',
                   p_object_id
               ) = 1
           );
{{- else }}
        SELECT fc.subject_id FROM filtered_candidates fc;
{{- end }}
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;
