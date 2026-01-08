{{- /*
  Template for generating the list_accessible_objects dispatcher function.
  Routes to specialized list_objects functions for known type/relation pairs.

  All relations have specialized functions generated at migration time.
  Unknown type/relation pairs indicate either a model error or a query
  against an undefined relation - both should return an error.
*/ -}}
-- Generated dispatcher for list_accessible_objects
-- Routes to specialized functions for all type/relation pairs
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE (object_id TEXT) AS $$
BEGIN
{{- if .HasSpecializedFunctions }}
    -- Route to specialized functions for all type/relation pairs
{{- range .Cases }}
    IF p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN
        RETURN QUERY SELECT * FROM {{.FunctionName}}(p_subject_type, p_subject_id);
        RETURN;
    END IF;
{{- end }}
{{- end }}

    -- Unknown type/relation pair - return empty result (relation not defined in model)
    -- This matches check_permission behavior for unknown relations (returns 0/denied)
    RETURN;
END;
$$ LANGUAGE plpgsql STABLE;
