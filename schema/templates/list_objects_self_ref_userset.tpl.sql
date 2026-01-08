{{- /*
  Template for list_objects function with self-referential userset patterns.

  This handles relations like: group.member: [user, group#member]
  where groups can contain other groups as members.

  Example:
    - user:alice is member of group:engineering
    - group:engineering#member is member of group:all-staff
    - Query: "What groups is user:alice a member of?"
    - Result: [group:engineering, group:all-staff] (through recursive expansion)

  Uses a recursive CTE to expand nested group membership with depth limit of 25.

  Also handles:
  - Other non-self-referential userset patterns (via JOIN)
  - Exclusions (simple via NOT EXISTS, complex via check_permission_internal)
  - Direct subject types (e.g., [user] in [user, group#member])
  - Wildcards
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}} (self-referential userset)
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    WITH RECURSIVE member_expansion(object_id, depth) AS (
        -- =====================================================================
        -- BASE CASE: Direct membership and non-recursive userset patterns
        -- =====================================================================

        -- Path 1: Direct tuple lookup (e.g., user:alice member group:engineering)
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.relation IN ({{.RelationList}})
          AND t.subject_type = p_subject_type
          AND p_subject_type IN ({{.AllowedSubjectTypes}})
          AND {{.SubjectIDCheck}}
{{- if .SimpleExcludedRelations }}
          -- Simple exclusions for direct path
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
          -- Complex exclusions for direct path
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- range .ComplexClosureRelations }}

        UNION

        -- Complex closure relation: {{.}}
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.relation = '{{.}}'
          AND t.subject_type = p_subject_type
          AND p_subject_type IN ({{$.AllowedSubjectTypes}})
          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- range .IntersectionClosureRelations }}

        UNION

        -- Compose with intersection closure relation: {{.}}
        SELECT DISTINCT o.object_id, 0
        FROM list_{{$.ObjectType}}_{{.}}_objects(p_subject_type, p_subject_id) o
{{- end }}
{{- range .UsersetPatterns }}
{{- if not .IsSelfReferential }}

        UNION

        -- Non-self-referential userset: Via {{.SubjectType}}#{{.SubjectRelation}} membership
{{- if .IsComplex }}
        -- Complex userset: use check_permission_internal for membership
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.relation IN ({{.SourceRelationList}})
          AND t.subject_type = '{{.SubjectType}}'
          AND position('#' in t.subject_id) > 0
          AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.SubjectRelation}}', '{{.SubjectType}}', split_part(t.subject_id, '#', 1), ARRAY[]::TEXT[]) = 1
{{- if .IsClosurePattern }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- else }}
        -- Simple userset: JOIN with membership tuples
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        JOIN melange_tuples m
          ON m.object_type = '{{.SubjectType}}'
          AND m.object_id = split_part(t.subject_id, '#', 1)
          AND m.relation IN ({{.SatisfyingRelationsList}})
          AND m.subject_type = p_subject_type
          AND p_subject_type IN ({{$.AllowedSubjectTypes}})
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
{{- end }}
{{- end }}

        -- =====================================================================
        -- RECURSIVE CASE: Self-referential userset expansion
        -- For patterns like [group#member] on group.member
        -- Find objects containing groups I'm already a member of
        -- =====================================================================

        UNION ALL

        SELECT DISTINCT t.object_id, me.depth + 1
        FROM member_expansion me
        JOIN melange_tuples t
          ON t.object_type = '{{.ObjectType}}'
          AND t.relation IN ({{.RelationList}})
          AND t.subject_type = '{{.ObjectType}}'
          AND position('#' in t.subject_id) > 0
          AND split_part(t.subject_id, '#', 1) = me.object_id
          AND split_part(t.subject_id, '#', 2) = '{{.Relation}}'
        WHERE me.depth < 25
{{- if .SimpleExcludedRelations }}
          -- Apply simple exclusions to recursive userset path
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
          -- Apply complex exclusions to recursive userset path
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
    )
    SELECT DISTINCT me.object_id
    FROM member_expansion me
{{- if .ExcludedParentRelations }}
    -- TTU exclusions
    WHERE TRUE
{{- range .ExcludedParentRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples link
          WHERE link.object_type = '{{$.ObjectType}}'
            AND link.object_id = me.object_id
            AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
            AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
            AND check_permission_internal(p_subject_type, p_subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
      )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
    -- Intersection exclusions
{{- if not .ExcludedParentRelations }}
    WHERE TRUE
{{- end }}
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
                AND link.object_id = me.object_id
                AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- else }}
          (check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', me.object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
           AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', me.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
          )
{{- end }}
{{- end }}
      )
{{- end }}
{{- end }}

    UNION

    -- Self-candidate: when subject is a userset on the same object type
    -- e.g., group:1#member checking what groups group:1#member is a member of
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          SELECT 1 FROM melange_relation_closure c
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
END;
$$ LANGUAGE plpgsql STABLE;
