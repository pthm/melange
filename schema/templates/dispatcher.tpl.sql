{{- /*
  Template for generating the check_permission dispatcher function.
  Routes to specialized functions or falls back to generic implementation.
  Userset subjects (containing '#') always use generic for proper handling.
*/ -}}
{{- if .HasSpecializedFunctions -}}
-- Generated internal dispatcher for {{.FunctionName}}_internal
-- Routes to specialized functions with p_visited for cycle detection in TTU patterns
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
    SELECT CASE
        -- Userset subjects (e.g., "group:eng#member") require generic implementation for reflexive checks
        -- Note: only subject_id contains '#' for usersets, not subject_type
        WHEN position('#' in p_subject_id) > 0 THEN {{.GenericFunctionName}}_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited)
{{- range .Cases}}
        WHEN p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN {{.CheckFunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited)
{{- end}}
        ELSE {{.GenericFunctionName}}_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited)
    END;
$$ LANGUAGE sql STABLE;

-- Generated dispatcher for {{.FunctionName}}
-- Routes to specialized functions or falls back to generic implementation
-- Note: userset subjects (type#relation) always use generic for proper handling
CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT {{.FunctionName}}_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[]);
$$ LANGUAGE sql STABLE;
{{- else -}}
-- Generated dispatcher (no specialized functions)
CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT {{.GenericFunctionName}}(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id);
$$ LANGUAGE sql STABLE;
{{- end -}}
