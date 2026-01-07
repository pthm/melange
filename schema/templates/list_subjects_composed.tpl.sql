{{- /*
  Template for list_subjects function with indirect anchor (composed access).

  This template handles relations with no direct/implied access paths but which can
  reach subjects through TTU or userset patterns to an anchor relation that has
  direct grants.

  For TTU patterns (e.g., document.viewer: viewer from folder where folder.viewer: [user]):
    - Find subjects who can access parent objects via anchor relation
    - Those subjects also have access to children pointing to those parents
    - Handle multiple target types by generating UNION for each
    - Handle recursive same-type parents with check_permission_internal

  For userset patterns (e.g., document.viewer: [group#member] where group.member: [user]):
    - Find all userset grants on the object
    - Return subjects who are members of those groups (via anchor's list function)

  Uses candidate collection + check_permission filtering to handle complex patterns.
*/ -}}
-- Generated list_subjects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
{{- if .IndirectAnchor }}
-- Indirect anchor: {{.IndirectAnchor.AnchorType}}.{{.IndirectAnchor.AnchorRelation}} via {{(index .IndirectAnchor.Path 0).Type}}
{{- end }}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_object_id TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_is_userset_filter BOOLEAN;
    v_filter_type TEXT;
    v_filter_relation TEXT;
BEGIN
    -- Check if subject_type is a userset filter (e.g., 'group#member')
    v_is_userset_filter := position('#' in p_subject_type) > 0;
    IF v_is_userset_filter THEN
        v_filter_type := split_part(p_subject_type, '#', 1);
        v_filter_relation := split_part(p_subject_type, '#', 2);
    END IF;

{{- if .HasIndirectAnchor }}
{{- $firstStep := index .IndirectAnchor.Path 0 }}

    IF v_is_userset_filter THEN
        -- Userset filter case: find subjects of type v_filter_type#v_filter_relation
        -- that have the requested relation on the object
        RETURN QUERY
        WITH subject_candidates AS (
{{- if eq $firstStep.Type "ttu" }}
{{- /* TTU: Generate UNION for each target type */ -}}
{{- range $i, $targetType := $firstStep.AllTargetTypes }}
{{- if $i }}
            UNION
{{- end }}
            -- From {{$targetType}} parents
            SELECT DISTINCT s.subject_id
            FROM melange_tuples link
            CROSS JOIN LATERAL list_{{$targetType}}_{{$firstStep.TargetRelation}}_subjects(link.subject_id, p_subject_type) s
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$firstStep.LinkingRelation}}'
              AND link.subject_type = '{{$targetType}}'
{{- end }}
{{- /* Handle recursive same-type parents - gather candidates via direct lookup and filter */ -}}
{{- range $recursiveType := $firstStep.RecursiveTypes }}
            UNION
            -- From {{$recursiveType}} parents (recursive, find candidates then verify)
            SELECT DISTINCT s.subject_id
            FROM melange_tuples link
            CROSS JOIN LATERAL list_{{$recursiveType}}_{{$firstStep.TargetRelation}}_subjects(link.subject_id, p_subject_type) s
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$firstStep.LinkingRelation}}'
              AND link.subject_type = '{{$recursiveType}}'
{{- end }}
{{- else if eq $firstStep.Type "userset" }}
            -- Userset: Find subjects via userset grants
            SELECT DISTINCT s.subject_id
            FROM melange_tuples t
            CROSS JOIN LATERAL list_{{$firstStep.SubjectType}}_{{$firstStep.SubjectRelation}}_subjects(split_part(t.subject_id, '#', 1), p_subject_type) s
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = '{{$firstStep.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{$firstStep.SubjectRelation}}'
{{- end }}
        )
        SELECT DISTINCT sc.subject_id
        FROM subject_candidates sc
        -- Verify via check_permission to apply any exclusions
        WHERE check_permission_internal(v_filter_type, sc.subject_id, '{{.Relation}}', '{{.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 1;

    ELSE
        -- Direct subject type case
        IF p_subject_type NOT IN ({{.AllowedSubjectTypes}}) THEN
            RETURN;
        END IF;

        RETURN QUERY
        WITH subject_candidates AS (
{{- if eq $firstStep.Type "ttu" }}
{{- /* TTU: Generate UNION for each target type */ -}}
{{- range $i, $targetType := $firstStep.AllTargetTypes }}
{{- if $i }}
            UNION
{{- end }}
            -- From {{$targetType}} parents
            SELECT DISTINCT s.subject_id
            FROM melange_tuples link
            CROSS JOIN LATERAL list_{{$targetType}}_{{$firstStep.TargetRelation}}_subjects(link.subject_id, p_subject_type) s
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$firstStep.LinkingRelation}}'
              AND link.subject_type = '{{$targetType}}'
{{- end }}
{{- /* Handle recursive same-type parents - gather candidates then verify */ -}}
{{- range $recursiveType := $firstStep.RecursiveTypes }}
            UNION
            -- From {{$recursiveType}} parents (recursive, find candidates then verify)
            SELECT DISTINCT s.subject_id
            FROM melange_tuples link
            CROSS JOIN LATERAL list_{{$recursiveType}}_{{$firstStep.TargetRelation}}_subjects(link.subject_id, p_subject_type) s
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$firstStep.LinkingRelation}}'
              AND link.subject_type = '{{$recursiveType}}'
{{- end }}
{{- else if eq $firstStep.Type "userset" }}
            -- Userset: Find subjects via userset grants
            SELECT DISTINCT s.subject_id
            FROM melange_tuples t
            CROSS JOIN LATERAL list_{{$firstStep.SubjectType}}_{{$firstStep.SubjectRelation}}_subjects(split_part(t.subject_id, '#', 1), p_subject_type) s
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = '{{$firstStep.SubjectType}}'
              AND position('#' in t.subject_id) > 0
              AND split_part(t.subject_id, '#', 2) = '{{$firstStep.SubjectRelation}}'
{{- end }}
        )
        SELECT DISTINCT sc.subject_id
        FROM subject_candidates sc
{{- if .SimpleExcludedRelations }}
        -- Apply simple exclusions
        WHERE TRUE
{{- range .SimpleExcludedRelations }}
          AND NOT EXISTS (
              SELECT 1 FROM melange_tuples excl
              WHERE excl.object_type = '{{$.ObjectType}}'
                AND excl.object_id = p_object_id
                AND excl.relation = '{{.}}'
                AND excl.subject_type = p_subject_type
                AND (excl.subject_id = sc.subject_id OR excl.subject_id = '*')
          )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
{{- if not .SimpleExcludedRelations }}
        WHERE TRUE
{{- end }}
        -- Apply complex exclusions
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, sc.subject_id, '{{.}}', '{{$.ObjectType}}', p_object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if not .SimpleExcludedRelations }}{{- if not .ComplexExcludedRelations }}
        -- No exclusions to apply
{{- end }}{{- end }}
        ;
    END IF;
{{- else }}
    -- Fallback: no indirect anchor (this shouldn't happen)
    RETURN QUERY SELECT NULL::TEXT WHERE FALSE;
{{- end }}
END;
$$ LANGUAGE plpgsql STABLE;
