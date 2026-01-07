{{- /*
  Template for list_objects function with indirect anchor (composed access).

  This template handles relations with no direct/implied access paths but which can
  reach subjects through TTU or userset patterns to an anchor relation that has
  direct grants.

  For TTU patterns (e.g., document.viewer: viewer from folder where folder.viewer: [user]):
    - Find objects whose linking relation points to objects accessible via anchor
    - Compose by calling the anchor relation's list function
    - Handle multiple target types by generating UNION for each
    - Handle recursive same-type parents with check_permission_internal

  For userset patterns (e.g., document.viewer: [group#member] where group.member: [user]):
    - Find objects with userset grants pointing to groups
    - Verify membership via anchor relation's list function or check_permission_internal

  Exclusions are applied after the composed access check.
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
{{- if .IndirectAnchor }}
-- Indirect anchor: {{.IndirectAnchor.AnchorType}}.{{.IndirectAnchor.AnchorRelation}} via {{(index .IndirectAnchor.Path 0).Type}}
{{- end }}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    -- Type guard: only return results if subject type is allowed
    IF p_subject_type NOT IN ({{.AllowedSubjectTypes}}) THEN
        RETURN;
    END IF;

    RETURN QUERY
{{- if .HasIndirectAnchor }}
{{- $firstStep := index .IndirectAnchor.Path 0 }}
{{- if eq $firstStep.Type "ttu" }}
{{- /* TTU: Generate UNION for each target type that has the target relation with direct grants */ -}}
{{- range $i, $targetType := $firstStep.AllTargetTypes }}
{{- if $i }}
    UNION
{{- end }}
    -- TTU composition: find objects whose {{$firstStep.LinkingRelation}} points to accessible {{$targetType}}s
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation = '{{$firstStep.LinkingRelation}}'
      AND t.subject_type = '{{$targetType}}'
      AND t.subject_id IN (
          -- Compose with {{$targetType}}.{{$firstStep.TargetRelation}} list function
          SELECT obj.object_id FROM list_{{$targetType}}_{{$firstStep.TargetRelation}}_objects(p_subject_type, p_subject_id) obj
      )
{{- if $.SimpleExcludedRelations }}
      -- Simple exclusions
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
      -- Complex exclusions
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- /* Handle recursive same-type parents with check_permission_internal */ -}}
{{- range $recursiveType := $firstStep.RecursiveTypes }}
    UNION
    -- Recursive TTU: find objects whose {{$firstStep.LinkingRelation}} points to {{$recursiveType}}s (same type, use check_permission)
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation = '{{$firstStep.LinkingRelation}}'
      AND t.subject_type = '{{$recursiveType}}'
      AND check_permission_internal(p_subject_type, p_subject_id, '{{$firstStep.TargetRelation}}', '{{$recursiveType}}', t.subject_id, ARRAY[]::TEXT[]) = 1
{{- if $.SimpleExcludedRelations }}
      -- Simple exclusions
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
      -- Complex exclusions
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- else if eq $firstStep.Type "userset" }}
    -- Userset composition: find objects with {{$firstStep.SubjectType}}#{{$firstStep.SubjectRelation}} grants
    -- where subject has {{$firstStep.SubjectRelation}} on the {{$firstStep.SubjectType}}
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{.ObjectType}}'
      AND t.relation IN ({{.RelationList}})
      AND t.subject_type = '{{$firstStep.SubjectType}}'
      AND position('#' in t.subject_id) > 0
      AND split_part(t.subject_id, '#', 2) = '{{$firstStep.SubjectRelation}}'
      -- Verify membership via first step's target list function or check_permission_internal
      AND (
          -- Check if the userset's object is in the accessible set
          split_part(t.subject_id, '#', 1) IN (
              SELECT obj.object_id FROM {{.IndirectAnchor.FirstStepTargetFunctionName}}(p_subject_type, p_subject_id) obj
          )
          OR
          -- Or verify directly via check_permission_internal
          check_permission_internal(p_subject_type, p_subject_id, '{{$firstStep.SubjectRelation}}', '{{$firstStep.SubjectType}}', split_part(t.subject_id, '#', 1), ARRAY[]::TEXT[]) = 1
      )
{{- if .SimpleExcludedRelations }}
      -- Simple exclusions
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
      -- Complex exclusions
{{- range .ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}

    UNION

    -- Self-candidate: when subject is a userset on the same object type
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          SELECT 1 FROM melange_relation_closure c
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
{{- else }}
    -- Fallback: no indirect anchor (this shouldn't happen)
    SELECT NULL::TEXT WHERE FALSE;
{{- end }}
END;
$$ LANGUAGE plpgsql STABLE;
