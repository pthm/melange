{{- /*
  Partial template for direct tuple lookup EXISTS clause.
  Returns an EXISTS expression that checks for direct tuple grants.
*/ -}}
EXISTS(
        SELECT 1 FROM melange_tuples
        WHERE object_type = '{{.ObjectType}}'
          AND object_id = p_object_id
          AND relation IN ({{.RelationList}})
          AND subject_type = p_subject_type
          AND {{.SubjectIDCheck}}
        LIMIT 1
    )
