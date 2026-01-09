{{- /*
  Template for generating the check_permission dispatcher function.
  Routes to specialized functions for all known type/relation pairs.
  Unknown type/relation pairs return 0 (deny by default).

  Phase 5: All relations now have specialized functions - no generic fallback needed.
*/ -}}
{{- if .HasSpecializedFunctions -}}
-- Generated internal dispatcher for {{.FunctionName}}_internal
-- Routes to specialized functions with p_visited for cycle detection in TTU patterns
-- Enforces depth limit of 25 to prevent stack overflow from deep permission chains
-- Phase 5: All relations use specialized functions - no generic fallback
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
BEGIN
    -- Depth limit check: prevent excessively deep permission resolution chains
    -- This catches both recursive TTU patterns and long userset chains
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    RETURN (SELECT CASE
{{- range .Cases}}
        WHEN p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN {{.CheckFunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited)
{{- end}}
        -- Unknown type/relation: deny by default (no generic fallback)
        ELSE 0
    END);
END;
$$ LANGUAGE plpgsql STABLE;

-- Generated dispatcher for {{.FunctionName}}
-- Routes to specialized functions for all known type/relation pairs
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
-- Generated dispatcher for {{.FunctionName}} (no relations defined)
-- Phase 5: Returns 0 (deny) for all requests - no generic fallback
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
    SELECT 0;
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT 0;
$$ LANGUAGE sql STABLE;
{{- end -}}
