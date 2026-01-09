{{- /*
  Partial template for complex exclusion check using check_permission_internal.
  Used when the excluded relation has userset, TTU, intersection, exclusion,
  or implied closure - patterns that can't be resolved with a simple tuple lookup.
*/ -}}
{{.InternalCheckFunctionName}}(p_subject_type, p_subject_id, '{{.ExcludedRelation}}', '{{.ObjectType}}', p_object_id, p_visited) = 1
