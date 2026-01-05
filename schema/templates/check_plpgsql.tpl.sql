{{- /*
  Template for generating PL/pgSQL check functions with cycle detection.
  Used when NeedsPLpgSQL() returns true (HasRecursive).
*/ -}}
-- Generated check function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName }} (
p_subject_type TEXT,
p_subject_id TEXT,
p_object_id TEXT,
p_visited TEXT [] DEFAULT ARRAY []::TEXT []
) RETURNS INTEGER AS $$
DECLARE
    v_has_access BOOLEAN := FALSE;
    v_key TEXT := '{{.ObjectType}}:' || p_object_id || ':{{.Relation}}';
BEGIN
    -- Cycle detection
    IF v_key = ANY(p_visited) THEN RETURN 0; END IF;
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    v_has_access := FALSE;

{{- if .HasIntersection}}
    -- Relation has intersection; only render standalone paths if HasStandaloneAccess is true
{{- if .HasStandaloneAccess}}
{{- if or .HasDirect .HasImplied}}

    -- Direct/Implied access path (standalone)
    IF {{.DirectCheck}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

{{- if .HasUserset}}

    -- Userset access path (standalone)
    IF NOT v_has_access THEN
        IF {{.UsersetCheck}} THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ImpliedFunctionCalls}}

    -- Implied access path via {{.FunctionName}} (standalone)
    IF NOT v_has_access THEN
        IF {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ParentRelations}}

    -- Recursive access path via {{.LinkingRelation}} -> {{.ParentRelation}} (standalone)
    IF NOT v_has_access THEN
        IF EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
              AND check_permission_internal(
                  p_subject_type, p_subject_id,
                  '{{.ParentRelation}}',
                  link.subject_type,
                  link.subject_id,
                  p_visited || v_key
              ) = 1
        ) THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}
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
              AND {{if $.HasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id AND t.subject_id != '*'{{end}}
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
                  p_visited || v_key
              ) = 1
        ){{else}}{{$part.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited || v_key) = 1{{end}}{{end}} THEN
{{- range $partIdx, $part := $group.Parts}}
{{- if $part.HasExclusion}}
            -- Check exclusion for part {{$partIdx}}
            IF check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, p_visited || v_key) = 1 THEN
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

{{- else}}
    -- No intersection: all access paths are standalone
{{- if or .HasDirect .HasImplied}}

    -- Direct/Implied access path
    IF {{.DirectCheck}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

{{- if .HasUserset}}

    -- Userset access path
    IF NOT v_has_access THEN
        IF {{.UsersetCheck}} THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ImpliedFunctionCalls}}

    -- Implied access path via {{.FunctionName}}
    IF NOT v_has_access THEN
        IF {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ParentRelations}}

    -- Recursive access path via {{.LinkingRelation}} -> {{.ParentRelation}}
    IF NOT v_has_access THEN
        IF EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
              AND check_permission_internal(
                  p_subject_type, p_subject_id,
                  '{{.ParentRelation}}',
                  link.subject_type,
                  link.subject_id,
                  p_visited || v_key
              ) = 1
        ) THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}
{{- end}}

{{- if .HasExclusion}}

    -- Exclusion check
    IF v_has_access THEN
        IF {{.ExclusionCheck}} THEN
            RETURN 0;
        END IF;
        RETURN 1;
    END IF;
{{- else}}

    IF v_has_access THEN RETURN 1; END IF;
{{- end}}

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE ;
