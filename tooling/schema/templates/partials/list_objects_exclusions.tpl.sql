{{- $r := .Root -}}
{{- $obj := .ObjectIDExpr -}}
{{- $subType := .SubjectTypeExpr -}}
{{- $subID := .SubjectIDExpr -}}
{{- if $r.SimpleExcludedRelations }}
      -- Simple exclusions
{{- range $r.SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{ $r.ObjectType }}'
            AND excl.object_id = {{ $obj }}
            AND excl.relation = '{{ . }}'
            AND excl.subject_type = {{ $subType }}
            AND (excl.subject_id = {{ $subID }} OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if $r.ComplexExcludedRelations }}
      -- Complex exclusions
{{- range $r.ComplexExcludedRelations }}
      AND check_permission_internal({{ $subType }}, {{ $subID }}, '{{ . }}', '{{ $r.ObjectType }}', {{ $obj }}, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- if $r.ExcludedParentRelations }}
      -- TTU exclusions
{{- range $r.ExcludedParentRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples link
          WHERE link.object_type = '{{ $r.ObjectType }}'
            AND link.object_id = {{ $obj }}
            AND link.relation = '{{ .LinkingRelation }}'
{{- if .AllowedLinkingTypes }}
            AND link.subject_type IN ({{range $i, $lt := .AllowedLinkingTypes}}{{if $i}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
            AND check_permission_internal({{ $subType }}, {{ $subID }}, '{{ .Relation }}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
      )
{{- end }}
{{- end }}
{{- if $r.ExcludedIntersectionGroups }}
      -- Intersection exclusions
{{- range $r.ExcludedIntersectionGroups }}
      AND NOT (
{{- range $i, $part := .Parts }}
{{- if $i }}
          AND
{{- end }}
{{- if $part.ParentRelation }}
          EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{ $r.ObjectType }}'
                AND link.object_id = {{ $obj }}
                AND link.relation = '{{ $part.ParentRelation.LinkingRelation }}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal({{ $subType }}, {{ $subID }}, '{{ $part.ParentRelation.Relation }}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- else }}
          (check_permission_internal({{ $subType }}, {{ $subID }}, '{{ $part.Relation }}', '{{ $r.ObjectType }}', {{ $obj }}, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
           AND check_permission_internal({{ $subType }}, {{ $subID }}, '{{ $part.ExcludedRelation }}', '{{ $r.ObjectType }}', {{ $obj }}, ARRAY[]::TEXT[]) = 0
{{- end }}
          )
{{- end }}
{{- end }}
      )
{{- end }}
{{- end }}
