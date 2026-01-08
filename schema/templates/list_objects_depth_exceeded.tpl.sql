{{- /*
  Template for list_objects function when userset chain depth exceeds 25 levels.

  These relations have userset patterns that form chains exceeding the 25-level
  depth limit (e.g., a1 -> a2 -> a3 -> ... -> a27). Rather than computing at
  runtime and hitting the depth limit, we fail immediately at function call.

  This is more efficient than the generic handler and provides clearer error semantics:
  - No runtime depth tracking overhead
  - Immediate failure for known-bad patterns
  - Clear error message indicating the relation's depth
  - Zero model table lookups
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
-- DEPTH EXCEEDED: Userset chain depth {{.MaxUsersetDepth}} exceeds 25 level limit
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    -- This relation has userset chain depth {{.MaxUsersetDepth}} which exceeds the 25 level limit.
    -- Raise M2002 immediately without any computation.
    RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
END;
$$ LANGUAGE plpgsql STABLE;
