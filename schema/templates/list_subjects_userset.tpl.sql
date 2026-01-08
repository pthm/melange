{{- /*
  Template for list_subjects function with userset patterns.
  Handles relations like: viewer: [group#member] or viewer: [user, group#member]

  This template handles two distinct cases:
  1. Userset filter (p_subject_type like 'group#member'):
     - Find userset tuples where subject matches the filter
     - Return normalized userset references (e.g., 'fga#member')

  2. Regular subject type (p_subject_type like 'user'):
     - Direct tuple lookup
     - Expand usersets to find individual subjects via group membership

  For complex userset patterns, we use check_permission_internal for membership verification.
  Also handles exclusions if present (combined userset + exclusion support).
*/ -}}
-- Generated list_subjects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;     -- Parsed type part (e.g., 'group' from 'group#member')
    v_filter_relation TEXT; -- Parsed relation part (e.g., 'member' from 'group#member')
BEGIN
    -- Check if p_subject_type is a userset filter (contains '#')
    IF position('#' in p_subject_type) > 0 THEN
        -- Parse userset filter
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);

        -- Userset filter: find userset tuples that match and return normalized references
        -- Note: No AllowedSubjectTypes guard here because userset filters query for userset tuples
        -- (e.g., group#member) which have different allowed types than direct subjects.
        -- The check_permission call at the end validates access.
        RETURN QUERY
        SELECT DISTINCT
            substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
        FROM melange_tuples t
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.relation IN ({{.AllSatisfyingRelations}})
          AND t.subject_type = v_filter_type
          AND position('#' in t.subject_id) > 0
          -- Check if the tuple's subject relation satisfies the filter relation via closure
          AND (
              substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
              OR EXISTS (
                  SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
          -- Verify permission (handles exclusions, etc.)
          -- Note: No wildcard post-filtering needed for userset filter case because:
          -- 1. Userset references (group#member) are structurally different from individual subjects
          -- 2. Wildcard access (type:*) applies to individual subjects, not userset references
          -- 3. Userset references can't be wildcards (group:*#member is not valid syntax)
          AND check_permission(v_filter_type, t.subject_id, '{{.Relation}}', '{{.ObjectType}}', p_object_id) = 1
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}
        UNION
        -- Compose with intersection closure relation: {{.}}
        SELECT * FROM list_{{$.ObjectType}}_{{.}}_subjects(p_object_id, v_filter_type || '#' || v_filter_relation)
{{- end }}
{{- end }}
        UNION
        -- Self-referential userset: when object_type matches filter_type and filter_relation
        -- satisfies the requested relation, the userset reference object_id#filter_relation has access
        -- e.g., for group:1.member with filter group#member, return 1#member (= group:1#member)
        -- NOTE: Exclusions don't apply to self-referential userset checks (structural validity)
        SELECT p_object_id || '#' || v_filter_relation AS subject_id
        WHERE v_filter_type = '{{.ObjectType}}'
          AND EXISTS (
              -- Verify the filter relation satisfies the requested relation via closure
              SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
              WHERE c.object_type = '{{.ObjectType}}'
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          );
    ELSE
        -- Regular subject type: find direct subjects and expand usersets
        RETURN QUERY
        WITH base_results AS (
            -- Path 1: Direct tuple lookup with simple closure relations
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = p_subject_type
              AND p_subject_type IN ({{.AllowedSubjectTypes}})  -- Type guard
{{- if not .HasWildcard }}
              AND t.subject_id != '*'  -- Exclude wildcards when model doesn't allow them
{{- end }}
{{- if .SimpleExcludedRelations }}
              -- Simple exclusions: NOT EXISTS tuple lookup
{{- range .SimpleExcludedRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples excl
                  WHERE excl.object_type = '{{$.ObjectType}}'
                    AND excl.object_id = p_object_id
                    AND excl.relation = '{{.}}'
                    AND excl.subject_type = t.subject_type
                    AND (excl.subject_id = t.subject_id OR excl.subject_id = '*')
              )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
              -- Complex exclusions: check_permission_internal call
{{- range .ComplexExcludedRelations }}
              AND check_permission_internal(t.subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
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
                    AND check_permission_internal(t.subject_type, t.subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
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
                        AND check_permission_internal(t.subject_type, t.subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
                  )
{{- else }}
                  (check_permission_internal(t.subject_type, t.subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
                   AND check_permission_internal(t.subject_type, t.subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
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
              AND p_subject_type IN ({{$.AllowedSubjectTypes}})
{{- if not $.HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
              AND check_permission_internal(t.subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}
            UNION
            -- Compose with intersection closure relation: {{.}}
            SELECT * FROM list_{{$.ObjectType}}_{{.}}_subjects(p_object_id, p_subject_type)
{{- end }}
{{- end }}
{{- range .UsersetPatterns }}
            UNION
            -- Path: Via {{.SubjectType}}#{{.SubjectRelation}} - expand group membership to return individual subjects
{{- if .IsComplex }}
            -- Complex userset: use LATERAL join with userset's list_subjects function
            -- This handles userset-to-userset chains where there are no direct subject tuples
            SELECT DISTINCT s.subject_id
            FROM melange_tuples t
            CROSS JOIN LATERAL list_{{.SubjectType}}_{{.SubjectRelation}}_subjects(split_part(t.subject_id, '#', 1), p_subject_type) s
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.SourceRelationList}})
              AND t.subject_type = '{{.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if .IsClosurePattern }}
              -- Closure pattern: verify permission via source relation (applies exclusions)
              AND check_permission_internal(p_subject_type, s.subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- else }}
            -- Simple userset: JOIN with membership tuples
            SELECT DISTINCT s.subject_id
            FROM melange_tuples t
            JOIN melange_tuples s
              ON s.object_type = '{{.SubjectType}}'
              AND s.object_id = split_part(t.subject_id, '#', 1)
              AND s.relation IN ({{.SatisfyingRelationsList}})
              AND s.subject_type = p_subject_type
              AND p_subject_type IN ({{$.AllowedSubjectTypes}})  -- Type guard for userset expansion
{{- if not $.HasWildcard }}
              AND s.subject_id != '*'
{{- end }}
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.SourceRelationList}})
              AND t.subject_type = '{{.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if .IsClosurePattern }}
              -- Closure pattern: verify permission via source relation (applies exclusions)
              AND check_permission_internal(s.subject_type, s.subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if $.SimpleExcludedRelations }}
              -- Apply simple exclusions to userset expansion path
{{- range $.SimpleExcludedRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples excl
                  WHERE excl.object_type = '{{$.ObjectType}}'
                    AND excl.object_id = p_object_id
                    AND excl.relation = '{{.}}'
                    AND excl.subject_type = p_subject_type
                    AND (excl.subject_id = s.subject_id OR excl.subject_id = '*')
              )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
              -- Apply complex exclusions to userset expansion path
{{- range $.ComplexExcludedRelations }}
              AND check_permission_internal(p_subject_type, s.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if $.ExcludedParentRelations }}
              -- Apply TTU exclusions to userset expansion path
{{- range $.ExcludedParentRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples link
                  WHERE link.object_type = '{{$.ObjectType}}'
                    AND link.object_id = p_object_id
                    AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                    AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                    AND check_permission_internal(p_subject_type, s.subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- end }}
{{- end }}
{{- if $.ExcludedIntersectionGroups }}
              -- Apply intersection exclusions to userset expansion path
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
                        AND link.object_id = p_object_id
                        AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                        AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                        AND check_permission_internal(p_subject_type, s.subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
                  )
{{- else }}
                  (check_permission_internal(p_subject_type, s.subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
                   AND check_permission_internal(p_subject_type, s.subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
                  )
{{- end }}
{{- end }}
              )
{{- end }}
{{- end }}
{{- end }}
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
{{- if .HasWildcard }}
        -- Wildcard handling: when wildcard exists, filter non-wildcard subjects
        -- to only those with explicit (non-wildcard-derived) access
        SELECT br.subject_id
        FROM base_results br
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard)
           OR (br.subject_id = '*')
           OR (
               br.subject_id != '*'
               AND check_permission_no_wildcard(
                   p_subject_type,
                   br.subject_id,
                   '{{.Relation}}',
                   '{{.ObjectType}}',
                   p_object_id
               ) = 1
           );
{{- else }}
        SELECT br.subject_id FROM base_results br;
{{- end }}
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;
