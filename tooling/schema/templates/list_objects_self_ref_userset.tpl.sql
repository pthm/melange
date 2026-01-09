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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
    )
    SELECT DISTINCT me.object_id
    FROM member_expansion me
    WHERE TRUE
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "me.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}

{{template "list_objects_self_candidate.tpl.sql" .}}
END;
$$ LANGUAGE plpgsql STABLE;
