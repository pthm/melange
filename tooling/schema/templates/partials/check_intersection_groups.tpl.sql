    -- Intersection groups (OR'd together, parts within group AND'd)
{{- range $groupIdx, $group := .IntersectionGroups}}
    IF NOT v_has_access THEN
        IF {{range $partIdx, $part := $group.Parts}}{{if $partIdx}} AND {{end}}{{if $part.IsThis}}EXISTS(
            SELECT 1 FROM melange_tuples t
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation = '{{$.Relation}}'
              AND t.subject_type = p_subject_type
              AND {{if $part.ThisHasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id AND t.subject_id != '*'{{end}}
        ){{else if $part.IsTTU}}EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$part.TTULinkingRelation}}'
              AND {{$.InternalCheckFunctionName}}(
                  p_subject_type, p_subject_id,
                  '{{$part.TTURelation}}',
                  link.subject_type,
                  link.subject_id,
                  p_visited
              ) = 1
        ){{else}}{{$part.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1{{end}}{{end}} THEN
{{- range $partIdx, $part := $group.Parts}}
{{- if $part.HasExclusion}}
            -- Check exclusion for part {{$partIdx}}
            IF {{$.InternalCheckFunctionName}}(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, p_visited) = 1 THEN
                -- Excluded, this group fails
            ELSE
{{- end}}
{{- end}}
            v_has_access := TRUE;
{{- range $partIdx, $part := $group.Parts}}
{{- if $part.HasExclusion}}
            END IF;
{{- end}}
{{- end}}
        END IF;
    END IF;
{{- end}}
