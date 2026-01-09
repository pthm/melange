{{- /*
  Template for generating PL/pgSQL check functions with cycle detection and intersection.
  Used when NeedsPLpgSQL() returns true or complex userset patterns exist.
*/ -}}
-- Generated check function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName }} (
p_subject_type TEXT,
p_subject_id TEXT,
p_object_id TEXT,
p_visited TEXT [] DEFAULT ARRAY []::TEXT []
) RETURNS INTEGER AS $$
DECLARE
    v_has_access BOOLEAN := FALSE;
    v_key TEXT := '{{.ObjectType}}:' || p_object_id || ':{{.Relation}}';
    v_userset_check INTEGER := 0;
BEGIN
    -- Cycle detection
    IF v_key = ANY(p_visited) THEN RETURN 0; END IF;
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    v_has_access := FALSE;

    -- Userset subject handling
    IF position('#' in p_subject_id) > 0 THEN
        -- Case 1: Self-referential userset check
        IF p_subject_type = '{{.ObjectType}}' AND
           substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN
            SELECT 1 INTO v_userset_check
            FROM (VALUES {{.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
            WHERE c.object_type = '{{.ObjectType}}'
              AND c.relation = '{{.Relation}}'
              AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
            LIMIT 1;
            IF v_userset_check = 1 THEN
                RETURN 1;
            END IF;
        END IF;

        -- Case 2: Computed userset matching
        SELECT 1 INTO v_userset_check
        FROM melange_tuples t
        JOIN (VALUES {{.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
            ON c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = t.relation
        JOIN (VALUES {{.UsersetValues}}) AS m(object_type, relation, subject_type, subject_relation)
            ON m.object_type = '{{.ObjectType}}'
            AND m.relation = c.satisfying_relation
            AND m.subject_type = t.subject_type
        JOIN (VALUES {{.ClosureValues}}) AS subj_c(object_type, relation, satisfying_relation)
            ON subj_c.object_type = t.subject_type
            AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
            AND subj_c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.object_id = p_object_id
          AND t.subject_type = p_subject_type
          AND t.subject_id != '*'
          AND position('#' in t.subject_id) > 0
          AND substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) =
              substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)
        LIMIT 1;
        IF v_userset_check = 1 THEN
{{- if .HasExclusion}}
            IF {{.ExclusionCheck}} THEN
                RETURN 0;
            END IF;
{{- end}}
            RETURN 1;
        END IF;
    END IF;

    -- Relation has intersection; only render standalone paths if HasStandaloneAccess is true
{{- if .HasStandaloneAccess}}
{{- if or .HasDirect .HasImplied}}

    -- Direct/Implied access path (standalone)
    IF {{.DirectCheck}} THEN
        v_has_access := TRUE;
    END IF;
{{- end}}

{{- if .HasUserset}}

    -- Userset access path (standalone)
    IF NOT v_has_access THEN
        IF {{.UsersetCheck}} THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ImpliedFunctionCalls}}

    -- Implied access path via {{.FunctionName}} (standalone)
    IF NOT v_has_access THEN
        IF {{.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited) = 1 THEN
            v_has_access := TRUE;
        END IF;
    END IF;
{{- end}}

{{- range .ParentRelations}}

    -- Recursive access path via {{.LinkingRelation}} -> {{.ParentRelation}} (standalone)
    IF NOT v_has_access THEN
        IF EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{.LinkingRelation}}'
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
{{- end}}

    -- Intersection groups (OR'd together, parts within group AND'd)
{{- range $groupIdx, $group := .IntersectionGroups}}
    IF NOT v_has_access THEN
        IF {{range $partIdx, $part := $group.Parts}}{{if $partIdx}} AND {{end}}{{if $part.IsThis}}EXISTS(
            SELECT 1 FROM melange_tuples t
            WHERE t.object_type = '{{$.ObjectType}}'
              AND t.object_id = p_object_id
              AND t.relation = '{{$.Relation}}'
              AND t.subject_type = p_subject_type
              AND {{if $part.ThisHasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id AND t.subject_id != '*'{{end}}
        ){{else if $part.IsTTU}}EXISTS(
            SELECT 1 FROM melange_tuples link
            WHERE link.object_type = '{{$.ObjectType}}'
              AND link.object_id = p_object_id
              AND link.relation = '{{$part.TTULinkingRelation}}'
              AND {{$.InternalCheckFunctionName}}(
                  p_subject_type, p_subject_id,
                  '{{$part.TTURelation}}',
                  link.subject_type,
                  link.subject_id,
                  p_visited || v_key
              ) = 1
        ){{else}}{{$part.FunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited || v_key) = 1{{end}}{{end}} THEN
{{- range $partIdx, $part := $group.Parts}}
{{- if $part.HasExclusion}}
            -- Check exclusion for part {{$partIdx}}
            IF {{$.InternalCheckFunctionName}}(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', p_object_id, p_visited || v_key) = 1 THEN
                -- Excluded, this group fails
            ELSE
{{- end}}
{{- end}}
            v_has_access := TRUE;
{{- range $partIdx, $part := $group.Parts}}
{{- if $part.HasExclusion}}
            END IF;
{{- end}}
{{- end}}
        END IF;
    END IF;
{{- end}}

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

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE ;
