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
$$ LANGUAGE plpgsql STABLE ;
