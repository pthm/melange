{{- /*
  Partial template for userset expansion EXISTS clause.
  Returns an EXISTS expression that joins grant tuples with membership tuples.
*/ -}}
EXISTS(
        SELECT 1
        FROM melange_tuples grant_tuple
        JOIN melange_tuples membership
          ON membership.object_type = '{{.SubjectType}}'
          AND membership.object_id = split_part(grant_tuple.subject_id, '#', 1)
          AND membership.relation = '{{.SubjectRelation}}'
          AND membership.subject_type = p_subject_type
          AND membership.subject_id = p_subject_id
        WHERE grant_tuple.object_type = '{{.ObjectType}}'
          AND grant_tuple.object_id = p_object_id
          AND grant_tuple.relation = '{{.Relation}}'
          AND grant_tuple.subject_type = '{{.SubjectType}}'
          AND position('#' in grant_tuple.subject_id) > 0
        LIMIT 1
    )
