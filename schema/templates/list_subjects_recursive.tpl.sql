{{- /*
  Template for list_subjects function with recursive TTU patterns.

  This is a comprehensive template that handles all pattern combinations:
  - Direct/Implied: tuple lookup with closure-inlined relations
  - Userset: JOIN with membership tuples or check_permission_internal
  - TTU/Recursive: traverse parent links to find subjects from parent objects
  - Exclusion: NOT EXISTS anti-join or check_permission_internal

  For TTU patterns, list_subjects traverses from child to parent:
  - For each parent link, find subjects with the parent relation on the parent object
  - Self-referential TTU requires recursive traversal up the parent chain

  Depth is limited to 25 with M2002 error on overflow (via check_permission_internal).
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
        RETURN QUERY
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
                  SELECT 1 FROM melange_relation_closure subj_c
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
          AND check_permission(v_filter_type, t.subject_id, '{{.Relation}}', '{{.ObjectType}}', p_object_id) = 1
{{- range .ParentRelations }}
        UNION
        -- TTU path: userset subjects via {{.LinkingRelation}} -> {{.Relation}}
        SELECT DISTINCT
            substring(pt.subject_id from 1 for position('#' in pt.subject_id) - 1) || '#' || v_filter_relation AS subject_id
        FROM melange_tuples link
        JOIN melange_tuples pt
          ON pt.object_type = link.subject_type
          AND pt.object_id = link.subject_id
          AND pt.relation IN (
              SELECT c.satisfying_relation
              FROM melange_relation_closure c
              WHERE c.object_type = link.subject_type
                AND c.relation = '{{.Relation}}'
          )
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
                  SELECT 1 FROM melange_relation_closure subj_c
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(pt.subject_id from position('#' in pt.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
          AND check_permission(v_filter_type, pt.subject_id, '{{$.Relation}}', '{{$.ObjectType}}', p_object_id) = 1
        UNION
        -- TTU intermediate object: return the parent object itself as a userset reference
        -- e.g., for document.viewer: viewer from parent, querying folder#viewer returns folder:X#viewer
        -- This handles the case where the userset filter type matches the TTU parent type
        SELECT DISTINCT link.subject_id || '#' || v_filter_relation AS subject_id
        FROM melange_tuples link
        WHERE link.object_type = '{{$.ObjectType}}'
          AND link.object_id = p_object_id
          AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
          AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
          -- Filter type must match the parent type
          AND link.subject_type = v_filter_type
          -- Filter relation must satisfy the parent relation via closure
          AND EXISTS (
              SELECT 1 FROM melange_relation_closure c
              WHERE c.object_type = link.subject_type
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          )
        UNION
        -- TTU nested intermediate objects: recursively resolve multi-hop TTU chains
        -- e.g., document -> folder -> organization, querying organization#viewer
        -- Uses LATERAL to call list_accessible_subjects on each parent
        SELECT nested.subject_id
        FROM melange_tuples link,
             LATERAL list_accessible_subjects(link.subject_type, link.subject_id, '{{.Relation}}', p_subject_type) nested
        WHERE link.object_type = '{{$.ObjectType}}'
          AND link.object_id = p_object_id
          AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
          AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
{{- end }}
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}
        UNION
        -- Compose with intersection closure relation: {{.}}
        SELECT * FROM list_{{$.ObjectType}}_{{.}}_subjects(p_object_id, v_filter_type || '#' || v_filter_relation)
{{- end }}
{{- end }}
        UNION
        -- Self-referential userset
        SELECT p_object_id || '#' || v_filter_relation AS subject_id
        WHERE v_filter_type = '{{.ObjectType}}'
          AND EXISTS (
              SELECT 1 FROM melange_relation_closure c
              WHERE c.object_type = '{{.ObjectType}}'
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          );
    ELSE
        -- Regular subject type: find direct subjects and expand usersets
        RETURN QUERY
        WITH subject_pool AS (
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.subject_type = p_subject_type
              AND p_subject_type IN ({{.AllowedSubjectTypes}})
{{- if not .HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
        ),
        base_results AS (
            -- Path 1: Direct tuple lookup with simple closure relations
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = p_subject_type
              AND p_subject_type IN ({{.AllowedSubjectTypes}})
{{- if not .HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
{{- if .SimpleExcludedRelations }}
              -- Simple exclusions
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
              -- Complex exclusions
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
                    AND link.subject_type IN ({{.AllowedLinkingTypes}})
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
{{- range .ComplexClosureRelations }}
            UNION
            -- Complex closure relation: {{.}}
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
{{- range .IntersectionClosureRelations }}
            UNION
            -- Compose with intersection closure relation: {{.}}
            SELECT * FROM list_{{$.ObjectType}}_{{.}}_subjects(p_object_id, p_subject_type)
{{- end }}
{{- range .UsersetPatterns }}
            UNION
            -- Userset path: Via {{.SubjectType}}#{{.SubjectRelation}}
{{- if .IsComplex }}
            SELECT DISTINCT m.subject_id
            FROM melange_tuples t
            JOIN melange_tuples m
              ON m.object_type = '{{.SubjectType}}'
              AND m.object_id = split_part(t.subject_id, '#', 1)
              AND m.subject_type = p_subject_type
              AND p_subject_type IN ({{$.AllowedSubjectTypes}})
{{- if not $.HasWildcard }}
              AND m.subject_id != '*'
{{- end }}
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.SourceRelationList}})
              AND t.subject_type = '{{.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
              AND check_permission_internal(m.subject_type, m.subject_id, '{{.SubjectRelation}}', '{{.SubjectType}}', split_part(t.subject_id, '#', 1), ARRAY[]::TEXT[]) = 1
{{- if .IsClosurePattern }}
              AND check_permission_internal(m.subject_type, m.subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- else }}
            SELECT DISTINCT m.subject_id
            FROM melange_tuples t
            JOIN melange_tuples m
              ON m.object_type = '{{.SubjectType}}'
              AND m.object_id = split_part(t.subject_id, '#', 1)
              AND m.relation IN ({{.SatisfyingRelationsList}})
              AND m.subject_type = p_subject_type
              AND p_subject_type IN ({{$.AllowedSubjectTypes}})
{{- if not $.HasWildcard }}
              AND m.subject_id != '*'
{{- end }}
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.SourceRelationList}})
              AND t.subject_type = '{{.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if .IsClosurePattern }}
              AND check_permission_internal(m.subject_type, m.subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if $.SimpleExcludedRelations }}
{{- range $.SimpleExcludedRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples excl
                  WHERE excl.object_type = '{{$.ObjectType}}'
                    AND excl.object_id = p_object_id
                    AND excl.relation = '{{.}}'
                    AND excl.subject_type = m.subject_type
                    AND (excl.subject_id = m.subject_id OR excl.subject_id = '*')
              )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
{{- range $.ComplexExcludedRelations }}
              AND check_permission_internal(m.subject_type, m.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if $.ExcludedParentRelations }}
{{- range $.ExcludedParentRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples link
                  WHERE link.object_type = '{{$.ObjectType}}'
                    AND link.object_id = p_object_id
                    AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                    AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
                    AND check_permission_internal(m.subject_type, m.subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- end }}
{{- end }}
{{- if $.ExcludedIntersectionGroups }}
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
                        AND check_permission_internal(m.subject_type, m.subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
                  )
{{- else }}
                  (check_permission_internal(m.subject_type, m.subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
                   AND check_permission_internal(m.subject_type, m.subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
                  )
{{- end }}
{{- end }}
              )
{{- end }}
{{- end }}
{{- end }}
{{- range .ParentRelations }}
            UNION
            -- TTU path: subjects via {{.LinkingRelation}} -> {{.Relation}}
            -- Find subjects with {{.Relation}} on parent objects
            SELECT DISTINCT sp.subject_id
            FROM subject_pool sp
            JOIN melange_tuples link
              ON link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
              AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
              -- Verify subject has the parent relation on the linked object
            WHERE check_permission_internal(p_subject_type, sp.subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
{{- if $.SimpleExcludedRelations }}
{{- range $.SimpleExcludedRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples excl
                  WHERE excl.object_type = '{{$.ObjectType}}'
                    AND excl.object_id = p_object_id
                    AND excl.relation = '{{.}}'
                    AND excl.subject_type = p_subject_type
                    AND (excl.subject_id = sp.subject_id OR excl.subject_id = '*')
              )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
{{- range $.ComplexExcludedRelations }}
              AND check_permission_internal(p_subject_type, sp.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if $.ExcludedParentRelations }}
{{- range $.ExcludedParentRelations }}
              AND NOT EXISTS (
                  SELECT 1 FROM melange_tuples excl_link
                  WHERE excl_link.object_type = '{{$.ObjectType}}'
                    AND excl_link.object_id = p_object_id
                    AND excl_link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
                    AND excl_link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
                    AND check_permission_internal(p_subject_type, sp.subject_id, '{{.Relation}}', excl_link.subject_type, excl_link.subject_id, ARRAY[]::TEXT[]) = 1
              )
{{- end }}
{{- end }}
{{- end }}
        ),
        has_wildcard AS (
            SELECT EXISTS (SELECT 1 FROM base_results br WHERE br.subject_id = '*') AS has_wildcard
        )
{{- if .HasWildcard }}
        -- Wildcard handling
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
