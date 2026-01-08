{{- /*
  Template for list_subjects function with self-referential userset patterns.

  This handles relations like: group.member: [user, group#member]
  where groups can contain other groups as members.

  Example:
    - user:alice is member of group:engineering
    - group:engineering#member is member of group:all-staff
    - Query: "Who are members of group:all-staff?"
    - Result: user:alice (through recursive expansion of group:engineering#member)

  Two cases:
  1. Userset filter (p_subject_type = 'group#member'):
     - Find userset tuples and recursively expand to find all nested usersets
     - Returns group:X#member references

  2. Regular subject type (p_subject_type = 'user'):
     - Find direct subjects via tuple lookup
     - Recursively expand userset subjects to find ultimate individual subjects

  Uses recursive CTEs with depth limit of 25.
*/ -}}
-- Generated list_subjects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}} (self-referential userset)
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

        -- Userset filter case: find userset tuples and recursively expand
        -- Returns normalized references like 'group:1#member'
        RETURN QUERY
        WITH RECURSIVE userset_expansion(userset_object_id, depth) AS (
            -- Base case: direct userset tuples on the requested object
            SELECT DISTINCT split_part(t.subject_id, '#', 1), 0
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

            UNION ALL

            -- Recursive case: expand nested userset membership
            -- For group:X#member, find groups that are members of group:X
            SELECT DISTINCT split_part(t.subject_id, '#', 1), ue.depth + 1
            FROM userset_expansion ue
            JOIN melange_tuples t
              ON t.object_type = v_filter_type
              AND t.object_id = ue.userset_object_id
              AND t.relation = v_filter_relation
              AND t.subject_type = v_filter_type
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = v_filter_relation
            WHERE ue.depth < 25
        )
        SELECT DISTINCT ue.userset_object_id || '#' || v_filter_relation AS subject_id
        FROM userset_expansion ue
{{- if .IntersectionClosureRelations }}
{{- range .IntersectionClosureRelations }}

        UNION

        -- Compose with intersection closure relation: {{.}}
        SELECT * FROM list_{{$.ObjectType}}_{{.}}_subjects(p_object_id, v_filter_type || '#' || v_filter_relation)
{{- end }}
{{- end }}

        UNION

        -- Self-referential: when filter type matches object type
        -- Return the object itself as a userset reference
        SELECT p_object_id || '#' || v_filter_relation AS subject_id
        WHERE v_filter_type = '{{.ObjectType}}'
          AND EXISTS (
              SELECT 1 FROM melange_relation_closure c
              WHERE c.object_type = '{{.ObjectType}}'
                AND c.relation = '{{.Relation}}'
                AND c.satisfying_relation = v_filter_relation
          );
    ELSE
        -- Regular subject type: find individual subjects via recursive userset expansion
        RETURN QUERY
        WITH RECURSIVE
        -- First expand all userset objects that have access (groups containing members)
        userset_objects(userset_object_id, depth) AS (
            -- Direct userset references on the object
            SELECT DISTINCT split_part(t.subject_id, '#', 1), 0
            FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = '{{.ObjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.Relation}}'

            UNION ALL

            -- Recursively find groups that are members of already-found groups
            SELECT DISTINCT split_part(t.subject_id, '#', 1), uo.depth + 1
            FROM userset_objects uo
            JOIN melange_tuples t
              ON t.object_type = '{{.ObjectType}}'
              AND t.object_id = uo.userset_object_id
              AND t.relation = '{{.Relation}}'
              AND t.subject_type = '{{.ObjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{.Relation}}'
            WHERE uo.depth < 25
        ),
        base_results AS (
            -- Path 1: Direct tuple lookup on the object itself
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

            UNION

            -- Path 2: Expand userset subjects from all reachable userset objects
            -- Find individual subjects (e.g., users) who are members of all discovered groups
            SELECT DISTINCT t.subject_id
            FROM userset_objects uo
            JOIN melange_tuples t
              ON t.object_type = '{{.ObjectType}}'
              AND t.object_id = uo.userset_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = p_subject_type
              AND p_subject_type IN ({{.AllowedSubjectTypes}})
{{- if not .HasWildcard }}
              AND t.subject_id != '*'
{{- end }}
{{- if .SimpleExcludedRelations }}
              -- Apply exclusions to userset-expanded subjects
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
              -- Apply complex exclusions to userset-expanded subjects
{{- range .ComplexExcludedRelations }}
              AND check_permission_internal(p_subject_type, t.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- range .UsersetPatterns }}
{{- if not .IsSelfReferential }}

            UNION

            -- Non-self-referential userset expansion: {{.SubjectType}}#{{.SubjectRelation}}
{{- if .IsComplex }}
            -- Complex userset: use LATERAL with list function
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
              AND p_subject_type IN ({{$.AllowedSubjectTypes}})
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
              AND check_permission_internal(s.subject_type, s.subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if $.SimpleExcludedRelations }}
              -- Apply simple exclusions
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
              -- Apply complex exclusions
{{- range $.ComplexExcludedRelations }}
              AND check_permission_internal(p_subject_type, s.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
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
