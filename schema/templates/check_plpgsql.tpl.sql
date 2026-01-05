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

{{- range .ParentRelations}}

    -- Recursive access path via {{.LinkingRelation}}
    IF NOT v_has_access THEN
        IF EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
              AND {{.ParentFunctionName}}(
                  p_subject_type, p_subject_id,
                  link.subject_id,
                  p_visited || v_key
              ) = 1
        ) THEN
            v_has_access := TRUE;
        END IF;
    END IF;
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
