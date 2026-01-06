{{- /*
  Template for list_objects function with direct/implied patterns.
  Uses single SELECT with closure-inlined relations.

  This template is used for relations that only have Direct and/or Implied features.
  Complex features (Userset, Recursive, Exclusion, Intersection) require different templates.

  Includes self-candidate logic for userset subjects: when the subject is a userset
  like "document:1#viewer" and the object_type is "document", the object "document:1"
  should be considered as a candidate.

  Type restriction enforcement:
  - Type guard applies to direct tuple lookup via WHERE clause
  - Self-candidate path is validated by closure check instead (subject type = object type is valid
    when the userset relation satisfies the requested relation via closure)
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    -- Direct tuple lookup with closure-inlined relations
    -- Type guard: only return results if subject type is in allowed subject types
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{.ObjectType}}'
      AND t.relation IN ({{.RelationList}})
      AND t.subject_type = p_subject_type
      AND p_subject_type IN ({{.AllowedSubjectTypes}})  -- Type guard in WHERE clause
      AND {{.SubjectIDCheck}}
    UNION
    -- Self-candidate: when subject is a userset on the same object type
    -- e.g., subject_id = 'document:1#viewer' querying object_type = 'document'
    -- The object 'document:1' should be considered as a candidate
    -- No type guard here - validity comes from the closure check below
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          -- Verify the userset relation satisfies the requested relation via closure
          SELECT 1 FROM melange_relation_closure c
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
END;
$$ LANGUAGE plpgsql STABLE;
