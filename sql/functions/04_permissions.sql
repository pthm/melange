-- Melange permission checking functions
-- Phase 5: Main check_permission uses generated specialized functions.
-- No-wildcard variant still uses generic implementation for ListUsers.

-- Intersection checks without wildcard matching - used by ListUsers
CREATE OR REPLACE FUNCTION check_intersection_groups_no_wildcard(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT [] DEFAULT ARRAY[]::TEXT []
) RETURNS BOOLEAN AS $$
DECLARE
    v_group RECORD;
    v_group_satisfied BOOLEAN;
    v_check RECORD;
    v_has_direct BOOLEAN;
    v_parent RECORD;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM melange_model m
        WHERE m.object_type = p_object_type
          AND m.relation = p_relation
          AND m.subject_type IS NOT NULL
          AND m.subject_relation IS NULL
          AND m.parent_relation IS NULL
    ) INTO v_has_direct;

    FOR v_group IN
        SELECT DISTINCT m.rule_group_id
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = c.object_type
            AND m.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND m.rule_group_mode = 'intersection'
          AND m.rule_group_id IS NOT NULL
    LOOP
        v_group_satisfied := TRUE;

        FOR v_check IN
            SELECT m.check_relation, m.check_excluded_relation, m.check_parent_relation, m.check_parent_type
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.rule_group_id = v_group.rule_group_id
              AND m.rule_group_mode = 'intersection'
              AND (m.check_relation IS NOT NULL OR m.check_parent_relation IS NOT NULL)
        LOOP
            IF v_check.check_parent_relation IS NOT NULL THEN
                v_group_satisfied := FALSE;
                FOR v_parent IN
                    SELECT t.subject_type AS parent_type, t.subject_id AS parent_id
                    FROM melange_tuples t
                    WHERE t.object_type = p_object_type
                      AND t.object_id = p_object_id
                      AND t.relation = v_check.check_parent_type
                LOOP
                    IF subject_has_grant_no_wildcard(
                        p_subject_type, p_subject_id,
                        v_parent.parent_type, v_parent.parent_id,
                        v_check.check_parent_relation, p_visited
                    ) THEN
                        v_group_satisfied := TRUE;
                        EXIT;
                    END IF;
                END LOOP;

                IF NOT v_group_satisfied THEN
                    EXIT;
                END IF;

                CONTINUE;
            END IF;

            IF v_check.check_relation = p_relation THEN
                IF position('#' in p_subject_id) > 0
                    AND p_subject_type = p_object_type
                    AND substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id
                    AND EXISTS (
                        SELECT 1
                        FROM melange_relation_closure c
                        WHERE c.object_type = p_object_type
                          AND c.relation = p_relation
                          AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
                    ) THEN
                    v_group_satisfied := TRUE;
                ELSEIF v_has_direct THEN
                    IF NOT EXISTS (
                        SELECT 1 FROM melange_tuples t
                        WHERE t.subject_type = p_subject_type
                          AND t.subject_id = p_subject_id
                          AND t.object_type = p_object_type
                          AND t.object_id = p_object_id
                          AND t.relation = v_check.check_relation
                    ) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                ELSE
                    IF NOT subject_has_grant_no_wildcard(
                        p_subject_type, p_subject_id,
                        p_object_type, p_object_id,
                        v_check.check_relation, p_visited
                    ) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;

                IF v_group_satisfied AND v_check.check_excluded_relation IS NOT NULL THEN
                    IF check_exclusion_with_visited(p_subject_type, p_subject_id,
                                                   v_check.check_excluded_relation,
                                                   p_object_type, p_object_id, p_visited) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;
            ELSE
                IF NOT subject_has_grant_no_wildcard(
                    p_subject_type, p_subject_id,
                    p_object_type, p_object_id,
                    v_check.check_relation, p_visited
                ) THEN
                    v_group_satisfied := FALSE;
                    EXIT;
                END IF;

                IF v_group_satisfied AND v_check.check_excluded_relation IS NOT NULL THEN
                    IF check_exclusion_with_visited(p_subject_type, p_subject_id,
                                                   v_check.check_excluded_relation,
                                                   p_object_type, p_object_id, p_visited) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;
            END IF;
        END LOOP;

        IF v_group_satisfied THEN
            RETURN TRUE;
        END IF;
    END LOOP;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;


-- Generic permission check ignoring wildcards with visited tracking.
-- This internal version accepts p_visited for cycle detection.
-- Used by ListUsers to enumerate specific subjects rather than matching wildcards.
CREATE OR REPLACE FUNCTION check_permission_no_wildcard_generic_internal(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
DECLARE
    v_has_intersection BOOLEAN;
    v_has_other_rules BOOLEAN;
    v_self_in_intersection BOOLEAN := FALSE;
BEGIN
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
            p_visited
        ) THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited) THEN
                RETURN 0;
            END IF;

            RETURN 1;
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
            RETURN 0;
        END IF;
    END IF;

    IF subject_has_grant_no_wildcard(
        p_subject_type, p_subject_id,
        p_object_type, p_object_id,
        p_relation, p_visited
    ) THEN
        IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited) THEN
            RETURN 0;
        END IF;

        RETURN 1;
    END IF;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;


-- Generic permission check ignoring wildcards - used by ListUsers to enumerate specific subjects.
CREATE OR REPLACE FUNCTION check_permission_no_wildcard_generic(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT check_permission_no_wildcard_generic_internal(
        p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[]
    );
$$ LANGUAGE sql STABLE;


-- Default dispatcher for check_permission.
-- This is replaced at migration time with a generated version that routes
-- to specialized per-relation functions. This default version denies all
-- requests until migration runs.
-- Phase 5: No generic fallback - all relations use specialized functions.
CREATE OR REPLACE FUNCTION check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT 0;
$$ LANGUAGE sql STABLE;


-- Default dispatcher for check_permission_no_wildcard.
-- This is replaced at migration time with a generated version.
CREATE OR REPLACE FUNCTION check_permission_no_wildcard(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT check_permission_no_wildcard_generic(
        p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id
    );
$$ LANGUAGE sql STABLE;
