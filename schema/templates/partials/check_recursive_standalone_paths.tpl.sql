{{- if or .HasDirect .HasImplied}}

    -- Direct/Implied access path
    IF {{.DirectCheck}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

{{- if .HasUserset}}

    -- Userset access path
    IF NOT v_has_access THEN
        IF {{.UsersetCheck}} THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ImpliedFunctionCalls}}

    -- Implied access path via {{.FunctionName}}
    IF NOT v_has_access THEN
        IF {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ParentRelations}}

    -- Recursive access path via {{.LinkingRelation}} -> {{.ParentRelation}}
    IF NOT v_has_access THEN
        IF EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes}}
              AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end}}
              AND {{$.InternalCheckFunctionName}}(
                  p_subject_type, p_subject_id,
                  '{{.ParentRelation}}',
                  link.subject_type,
                  link.subject_id,
                  p_visited || v_key
              ) = 1
        ) THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}
