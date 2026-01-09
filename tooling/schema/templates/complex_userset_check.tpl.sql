{{- /*
  Partial template for complex userset expansion EXISTS clause.
  Used when the userset closure contains relations with complex features
  (userset, TTU, exclusion, intersection) that can't be verified via simple tuple lookup.

  Instead of JOINing membership tuples directly, this template finds grant tuples
  and calls check_permission_internal to verify that the subject has the required
  membership relation on the userset object.

  For [group#member] where member has exclusions, this does:
  1. Find tuples: (document:1, viewer, group:x#member)
  2. Verify: check_permission_internal(user:alice, 'member', 'group', 'x') = 1

  The cycle key is passed to check_permission_internal to prevent infinite loops
  when userset patterns reference each other. The key format is 'object_type:object_id:relation'.
*/ -}}
EXISTS (
SELECT 1
FROM melange_tuples grant_tuple
WHERE grant_tuple.object_type = '{{.ObjectType}}'
AND grant_tuple.object_id = p_object_id
AND grant_tuple.relation = '{{.Relation}}'
AND grant_tuple.subject_type = '{{.SubjectType}}'
AND position ('#' in grant_tuple.subject_id) > 0
AND split_part (grant_tuple.subject_id, '#', 2) = '{{.SubjectRelation}}'
AND {{.InternalCheckFunctionName}} (
    p_subject_type,
    p_subject_id,
    '{{.SubjectRelation}}',
    '{{.SubjectType}}',
    split_part (grant_tuple.subject_id, '#', 1),
    p_visited || ARRAY['{{.ObjectType}}:' || p_object_id || ':{{.Relation}}']
) = 1
LIMIT 1
)
