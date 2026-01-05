{{- /*
  Template for generating the check_permission dispatcher function.
  Routes to specialized functions or falls back to generic implementation.
*/ -}}
{{- if .HasSpecializedFunctions -}}
-- Generated dispatcher for {{.FunctionName}}
-- Routes to specialized functions or falls back to generic implementation
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT CASE
{{- range .Cases}}
        WHEN p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN {{.CheckFunctionName}}(p_subject_type, p_subject_id, p_object_id, ARRAY[]::TEXT[])
{{- end}}
        ELSE {{.GenericFunctionName}}(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id)
    END;
$$ LANGUAGE sql STABLE;
{{- else -}}
-- Generated dispatcher (no specialized functions)
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT {{.GenericFunctionName}}(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id);
$$ LANGUAGE sql STABLE;
{{- end}}
