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
