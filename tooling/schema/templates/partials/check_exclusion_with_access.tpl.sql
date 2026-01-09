{{- if .HasExclusion}}

    -- Exclusion check
    IF v_has_access THEN
        IF {{.ExclusionCheck}} THEN
            RETURN 0;
        END IF;
        RETURN 1;
    END IF;
{{- else}}

    IF v_has_access THEN RETURN 1; END IF;
{{- end}}
