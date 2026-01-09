{{- /*
  Partial template for direct tuple lookup EXISTS clause.
  Returns an EXISTS expression that checks for direct tuple grants.
  SubjectTypeFilter ensures type restrictions from the model are enforced.
*/ -}}
EXISTS (
SELECT 1 FROM melange_tuples
WHERE object_type = '{{.ObjectType}}'
AND object_id = p_object_id
AND relation IN ({{.RelationList }})
AND subject_type IN ({{.SubjectTypeFilter }})
AND subject_type = p_subject_type
AND {{.SubjectIDCheck }}
LIMIT 1
)
