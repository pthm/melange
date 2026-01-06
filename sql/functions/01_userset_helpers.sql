-- Melange infrastructure functions
-- These are generic permission checking functions that work with melange_model,
-- melange_relation_closure, and melange_tuples.
-- IMPORTANT: These functions are domain-agnostic. Domain-specific logic belongs in the Go layer.
--
-- This file is idempotent and applied by `melange migrate`.
--
-- Phase 5: Main check_permission uses generated specialized functions.
-- subject_has_grant_no_wildcard is kept for ListUsers (check_permission_no_wildcard path).


-- =============================================================================
-- USERSET REFERENCE HELPER FUNCTIONS (NO-WILDCARD VARIANT)
--
-- These functions support [type#relation] patterns where a subject gains access
-- via group/team membership rather than direct tuple assignment.
--
-- This no-wildcard variant is used by ListUsers to enumerate specific subjects
-- rather than matching "everyone" via user:*.
-- =============================================================================

-- Variant that ignores wildcards - used by ListUsers to enumerate specific subjects
-- rather than matching "everyone" via user:*
CREATE OR REPLACE FUNCTION subject_has_grant_no_wildcard(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_visited TEXT [] DEFAULT ARRAY[]::TEXT []
) RETURNS BOOLEAN AS $$
DECLARE
    v_found BOOLEAN := FALSE;
    v_userset RECORD;
    v_parent RECORD;
    v_visit_key TEXT;
    v_has_intersection BOOLEAN;
    v_has_other_rules BOOLEAN;
    v_self_in_intersection BOOLEAN := FALSE;
BEGIN
    v_visit_key := p_object_type || ':' || p_object_id || ':' || p_relation;

    IF v_visit_key = ANY(p_visited) THEN
        RETURN FALSE;
    END IF;

    -- Depth protection: raise exception to signal resolution too complex
    IF COALESCE(array_length(p_visited, 1), 0) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex: depth limit exceeded' USING ERRCODE = 'M2002';
    END IF;

    IF position('#' in p_subject_id) > 0 AND p_subject_type = p_object_type THEN
        IF substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN
            SELECT TRUE INTO v_found
            FROM melange_relation_closure c
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
            LIMIT 1;

            IF v_found THEN
                RETURN TRUE;
            END IF;
        END IF;
    END IF;

    SELECT EXISTS (
        SELECT 1
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = c.object_type
            AND m.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND m.rule_group_mode = 'intersection'
    ) INTO v_has_intersection;

    IF v_has_intersection THEN
        SELECT EXISTS (
            SELECT 1
            FROM melange_relation_closure c
            JOIN melange_model m
                ON m.object_type = c.object_type
                AND m.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND m.rule_group_mode = 'intersection'
              AND m.check_relation = p_relation
        ) INTO v_self_in_intersection;

        IF check_intersection_groups_no_wildcard(
            p_subject_type, p_subject_id,
            p_relation, p_object_type, p_object_id,
            p_visited || v_visit_key
        ) THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;

        SELECT EXISTS (
            SELECT 1
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.relation = p_relation
              AND (m.rule_group_mode IS NULL OR m.rule_group_mode != 'intersection')
              AND (m.subject_type IS NOT NULL OR m.implied_by IS NOT NULL OR m.parent_relation IS NOT NULL)
              AND NOT (
                  v_self_in_intersection
                  AND m.subject_type IS NOT NULL
                  AND m.implied_by IS NULL
                  AND m.parent_relation IS NULL
              )
              AND NOT (
                  m.implied_by IS NOT NULL
                  AND EXISTS (
                      SELECT 1
                      FROM melange_model mi
                      WHERE mi.object_type = p_object_type
                        AND mi.relation = m.implied_by
                        AND mi.rule_group_mode = 'intersection'
                  )
              )
        ) INTO v_has_other_rules;

        IF NOT v_has_other_rules THEN
            RETURN FALSE;
        END IF;
    END IF;

    SELECT TRUE INTO v_found
    FROM melange_tuples t
    JOIN melange_relation_closure c
        ON c.object_type = p_object_type
        AND c.relation = p_relation
        AND c.satisfying_relation = t.relation
    JOIN melange_model m
        ON m.object_type = p_object_type
        AND m.relation = c.satisfying_relation
        AND m.subject_type = t.subject_type
        AND m.parent_relation IS NULL
        AND (
            m.subject_relation IS NULL
            OR (
                m.subject_relation IS NOT NULL
                AND position('#' in p_subject_id) > 0
                AND substring(p_subject_id from position('#' in p_subject_id) + 1) = m.subject_relation
            )
        )
    WHERE t.object_type = p_object_type
      AND t.object_id = p_object_id
      AND t.subject_type = p_subject_type
      AND t.subject_id = p_subject_id
    LIMIT 1;

    IF v_found THEN
        IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
            RETURN FALSE;
        END IF;
        RETURN TRUE;
    END IF;

    IF position('#' in p_subject_id) > 0 THEN
        SELECT TRUE INTO v_found
        FROM melange_tuples t
        JOIN melange_relation_closure c
            ON c.object_type = p_object_type
            AND c.relation = p_relation
            AND c.satisfying_relation = t.relation
        JOIN melange_model m
            ON m.object_type = p_object_type
            AND m.relation = c.satisfying_relation
            AND m.subject_type = t.subject_type
            AND m.subject_relation IS NOT NULL
            AND m.parent_relation IS NULL
        JOIN melange_relation_closure subj_c
            ON subj_c.object_type = t.subject_type
            AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
            AND subj_c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
        WHERE t.object_type = p_object_type
          AND t.object_id = p_object_id
          AND t.subject_type = p_subject_type
          AND t.subject_id != '*'
          AND position('#' in t.subject_id) > 0
          AND substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) =
              substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)
        LIMIT 1;

    IF v_found THEN
        IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
            RETURN FALSE;
        END IF;
        RETURN TRUE;
    END IF;
    END IF;

    FOR v_userset IN
        SELECT
            t.subject_type AS group_type,
            CASE
                WHEN position('#' in t.subject_id) > 0
                THEN substring(t.subject_id from 1 for position('#' in t.subject_id) - 1)
                ELSE t.subject_id
            END AS group_id,
            CASE
                WHEN position('#' in t.subject_id) > 0
                THEN ur.subject_relation_satisfying
                ELSE ur.subject_relation
            END AS required_relation
        FROM melange_userset_rules ur
        JOIN melange_tuples t
            ON t.object_type = p_object_type
            AND t.object_id = p_object_id
            AND t.relation = ur.tuple_relation
            AND t.subject_type = ur.subject_type
        WHERE ur.object_type = p_object_type
          AND ur.relation = p_relation
          AND t.subject_id != '*'
          AND (
              position('#' in t.subject_id) = 0
              OR substring(t.subject_id from position('#' in t.subject_id) + 1) = ur.subject_relation_satisfying
          )
    LOOP
        IF subject_has_grant_no_wildcard(
            p_subject_type, p_subject_id,
            v_userset.group_type, v_userset.group_id,
            v_userset.required_relation,
            p_visited || v_visit_key
        ) THEN
            IF check_all_exclusions_with_visited(
                p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id,
                p_visited || v_visit_key
            ) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;
    END LOOP;

    FOR v_parent IN
        SELECT
            t.subject_type AS parent_type,
            t.subject_id AS parent_id,
            m.parent_relation AS required_relation
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = p_object_type
            AND m.relation = c.satisfying_relation
            AND m.parent_relation IS NOT NULL
        JOIN melange_tuples t
            ON t.object_type = p_object_type
            AND t.object_id = p_object_id
            AND t.relation = m.subject_type
        JOIN melange_model mr
            ON mr.object_type = p_object_type
            AND mr.relation = m.subject_type
            AND mr.subject_type = t.subject_type
            AND mr.parent_relation IS NULL
            AND mr.implied_by IS NULL
            AND (
                mr.subject_relation IS NULL
                OR (
                    position('#' in t.subject_id) > 0
                    AND substring(t.subject_id from position('#' in t.subject_id) + 1) = mr.subject_relation
                )
            )
            AND (t.subject_id != '*' OR mr.subject_wildcard = TRUE)
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
    LOOP
        IF subject_has_grant_no_wildcard(
            p_subject_type, p_subject_id,
            v_parent.parent_type, v_parent.parent_id,
            v_parent.required_relation,
            p_visited || v_visit_key
        ) THEN
            IF check_all_exclusions_with_visited(
                p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id,
                p_visited || v_visit_key
            ) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;
    END LOOP;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;
