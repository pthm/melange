{{- /*
  Template for generating check functions without recursion and with intersection.
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
DECLARE
    v_userset_check INTEGER := 0;
    v_has_access BOOLEAN := FALSE;
BEGIN
{{template "check_userset_subject.tpl.sql" .}}
{{- if .HasStandaloneAccess}}
    -- Non-intersection access paths
    IF {{.AccessChecks}}{{range .ImpliedFunctionCalls}} OR {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1{{end}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

{{template "check_intersection_groups.tpl.sql" .}}
{{template "check_exclusion_with_access.tpl.sql" .}}

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE ;
