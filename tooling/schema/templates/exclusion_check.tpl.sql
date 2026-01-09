{{- /*
  Partial template for exclusion check EXISTS clause.
  Returns an EXISTS expression that checks if subject is excluded.
*/ -}}
EXISTS (
SELECT 1 FROM melange_tuples
WHERE object_type = '{{.ObjectType}}'
AND object_id = p_object_id
AND relation = '{{.ExcludedRelation}}'
AND subject_type = p_subject_type
AND {{.SubjectIDCheck }}
LIMIT 1
)
