{{- /*
  Template for generating pure SQL check functions.
  Used when NeedsPLpgSQL() returns false.
*/ -}}
-- Generated check function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_object_id TEXT,
    p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
{{- if .HasExclusion}}
    SELECT CASE
        WHEN ({{.AccessChecks}})
        THEN
            CASE WHEN {{.ExclusionCheck}} THEN 0 ELSE 1 END
        ELSE 0
    END
{{- else}}
    SELECT CASE WHEN ({{.AccessChecks}}) THEN 1 ELSE 0 END
{{- end}};
$$ LANGUAGE sql STABLE;
