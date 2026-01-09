{{- /*
  Template for generating PL/pgSQL check functions with cycle detection and intersection.
  Used when NeedsPLpgSQL() returns true or complex userset patterns exist.
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
    v_userset_check INTEGER := 0;
BEGIN
    -- Cycle detection
    IF v_key = ANY(p_visited) THEN RETURN 0; END IF;
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    v_has_access := FALSE;

{{template "check_userset_subject.tpl.sql" .}}

    -- Relation has intersection; only render standalone paths if HasStandaloneAccess is true
{{- if .HasStandaloneAccess}}
{{template "check_recursive_standalone_paths.tpl.sql" .}}
{{- end}}
{{template "check_recursive_intersection_groups.tpl.sql" .}}
{{template "check_exclusion_with_access.tpl.sql" .}}

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE ;
