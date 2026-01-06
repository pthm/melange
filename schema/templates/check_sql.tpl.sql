{{- /*
  Template for generating check functions without cycle detection.
  Uses PL/pgSQL to defer validation until execution time, since
  melange_tuples is a user-defined view created after migration.
*/ -}}
-- Generated check function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName }} (
p_subject_type TEXT,
p_subject_id TEXT,
p_object_id TEXT,
p_visited TEXT [] DEFAULT ARRAY []::TEXT []
) RETURNS INTEGER AS $$
{{- if .HasIntersection}}
DECLARE
    v_has_access BOOLEAN := FALSE;
BEGIN
{{- if .HasStandaloneAccess}}
    -- Non-intersection access paths
    IF {{.AccessChecks}}{{range .ImpliedFunctionCalls}} OR {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1{{end}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

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
              AND check_permission_internal(
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
            IF check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, p_visited) = 1 THEN
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

{{- if .HasExclusion}}
    -- Top-level exclusion check
    IF v_has_access THEN
        IF {{.ExclusionCheck}} THEN
            RETURN 0;
        END IF;
        RETURN 1;
    END IF;
{{- else}}
    IF v_has_access THEN
        RETURN 1;
    END IF;
{{- end}}

    RETURN 0;
END;
{{- else}}
BEGIN
{{- if .HasExclusion}}
    IF {{.AccessChecks}}{{range .ImpliedFunctionCalls}} OR {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1{{end}} THEN
        IF {{.ExclusionCheck}} THEN
            RETURN 0;
        ELSE
            RETURN 1;
        END IF;
    ELSE
        RETURN 0;
    END IF;
{{- else}}
    IF {{.AccessChecks}}{{range .ImpliedFunctionCalls}} OR {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1{{end}} THEN
        RETURN 1;
    ELSE
        RETURN 0;
    END IF;
{{- end}}
END;
{{- end}}
$$ LANGUAGE plpgsql STABLE ;
