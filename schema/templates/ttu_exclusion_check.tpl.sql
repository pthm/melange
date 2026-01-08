{{- /*
  Partial template for TTU exclusion check using check_permission_internal.
  Used for "but not X from Y" patterns where the excluded permission is
  inherited from a linked object.
*/ -}}
EXISTS(
    SELECT 1 FROM melange_tuples link
    WHERE link.object_type = '{{.ObjectType}}'
      AND link.object_id = p_object_id
      AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes}}
      AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end}}
      AND {{.InternalCheckFunctionName}}(
          p_subject_type, p_subject_id,
          '{{.ExcludedRelation}}',
          link.subject_type,
          link.subject_id,
          p_visited
      ) = 1
)
