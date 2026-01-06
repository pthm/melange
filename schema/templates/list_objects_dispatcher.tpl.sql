{{- /*
  Template for generating the list_accessible_objects dispatcher function.
  Routes to specialized list_objects functions for known type/relation pairs,
  falling back to the generic list_accessible_objects_generic function for unsupported patterns.

  Unlike check_permission which must deny unknown patterns, list functions can
  safely fall back to generic because they filter results through check_permission.
*/ -}}
-- Generated dispatcher for list_accessible_objects
-- Routes to specialized functions or falls back to generic implementation
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE (object_id TEXT) AS $$
BEGIN
{{- if .HasSpecializedFunctions }}
    -- Route to specialized functions for supported type/relation pairs
{{- range .Cases }}
    IF p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN
        RETURN QUERY SELECT * FROM {{.FunctionName}}(p_subject_type, p_subject_id);
        RETURN;
    END IF;
{{- end }}
{{- end }}

    -- Fall back to generic implementation for unsupported patterns
    RETURN QUERY SELECT * FROM list_accessible_objects_generic(
        p_subject_type, p_subject_id, p_relation, p_object_type
    );
END;
$$ LANGUAGE plpgsql STABLE;
