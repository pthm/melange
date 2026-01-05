-- Exclusion check: delegates to subject_has_grant since exclusions use the same
-- permission resolution logic as grants (direct tuples, usersets, parent inheritance)
CREATE OR REPLACE FUNCTION check_exclusion(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_excluded_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS BOOLEAN AS $$
BEGIN
    RETURN subject_has_grant(
        p_subject_type,
        p_subject_id,
        p_object_type,
        p_object_id,
        p_excluded_relation,
        ARRAY[]::TEXT[]
    );
END;
$$ LANGUAGE plpgsql STABLE;


-- Variant that preserves visited state across recursive checks to prevent cycles
CREATE OR REPLACE FUNCTION check_exclusion_with_visited(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_excluded_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT[]
) RETURNS BOOLEAN AS $$
BEGIN
    RETURN subject_has_grant(
        p_subject_type,
        p_subject_id,
        p_object_type,
        p_object_id,
        p_excluded_relation,
        p_visited
    );
END;
$$ LANGUAGE plpgsql STABLE;


-- Aggregates ALL exclusion rules for a relation: simple exclusions, parent-based
-- exclusions, and intersection exclusions. Returns TRUE if subject is blocked
-- by ANY of them. Nested exclusions like "(writer but not editor) but not owner"
-- generate multiple exclusion rows that are all checked.
CREATE OR REPLACE FUNCTION check_all_exclusions(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS BOOLEAN AS $$
DECLARE
    v_excluded TEXT;
    v_parent_excl RECORD;
    v_parent RECORD;
BEGIN
    -- Simple exclusions: "but not blocked"
    FOR v_excluded IN
        SELECT em.excluded_relation
        FROM melange_relation_closure c
        JOIN melange_model em
            ON em.object_type = c.object_type
            AND em.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND em.excluded_relation IS NOT NULL
    LOOP
        IF check_exclusion(p_subject_type, p_subject_id, v_excluded, p_object_type, p_object_id) THEN
            RETURN TRUE;  -- Subject is excluded
        END IF;
    END LOOP;

    -- Parent-based exclusions: "but not viewer from parent"
    FOR v_parent_excl IN
        SELECT em.excluded_parent_relation, em.excluded_parent_type
        FROM melange_relation_closure c
        JOIN melange_model em
            ON em.object_type = c.object_type
            AND em.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND em.excluded_parent_relation IS NOT NULL
    LOOP
        FOR v_parent IN
            SELECT t.subject_type AS parent_type, t.subject_id AS parent_id
            FROM melange_tuples t
            WHERE t.object_type = p_object_type
              AND t.object_id = p_object_id
              AND t.relation = v_parent_excl.excluded_parent_type
        LOOP
            IF subject_has_grant(
                p_subject_type, p_subject_id,
                v_parent.parent_type, v_parent.parent_id,
                v_parent_excl.excluded_parent_relation, ARRAY[]::TEXT[]
            ) THEN
                RETURN TRUE;
            END IF;
        END LOOP;
    END LOOP;

    -- Intersection exclusions: "but not (editor and owner)"
    IF check_exclusion_intersection_groups(
        p_subject_type, p_subject_id,
        p_relation, p_object_type, p_object_id
    ) THEN
        RETURN TRUE;
    END IF;

    RETURN FALSE;  -- Not excluded
END;
$$ LANGUAGE plpgsql STABLE;


-- Variant that preserves visited state for cycle prevention in deep recursion
CREATE OR REPLACE FUNCTION check_all_exclusions_with_visited(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT[]
) RETURNS BOOLEAN AS $$
DECLARE
    v_excluded TEXT;
    v_parent_excl RECORD;
    v_parent RECORD;
BEGIN
    FOR v_excluded IN
        SELECT em.excluded_relation
        FROM melange_relation_closure c
        JOIN melange_model em
            ON em.object_type = c.object_type
            AND em.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND em.excluded_relation IS NOT NULL
    LOOP
        IF check_exclusion_with_visited(p_subject_type, p_subject_id, v_excluded, p_object_type, p_object_id, p_visited) THEN
            RETURN TRUE;
        END IF;
    END LOOP;

    FOR v_parent_excl IN
        SELECT em.excluded_parent_relation, em.excluded_parent_type
        FROM melange_relation_closure c
        JOIN melange_model em
            ON em.object_type = c.object_type
            AND em.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND em.excluded_parent_relation IS NOT NULL
    LOOP
        FOR v_parent IN
            SELECT t.subject_type AS parent_type, t.subject_id AS parent_id
            FROM melange_tuples t
            WHERE t.object_type = p_object_type
              AND t.object_id = p_object_id
              AND t.relation = v_parent_excl.excluded_parent_type
        LOOP
            IF subject_has_grant(
                p_subject_type, p_subject_id,
                v_parent.parent_type, v_parent.parent_id,
                v_parent_excl.excluded_parent_relation, p_visited
            ) THEN
                RETURN TRUE;
            END IF;
        END LOOP;
    END LOOP;

    IF check_exclusion_intersection_groups_with_visited(
        p_subject_type, p_subject_id,
        p_relation, p_object_type, p_object_id,
        p_visited
    ) THEN
        RETURN TRUE;
    END IF;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;


-- Intersection exclusions: "but not (editor and owner)" excludes when subject
-- has ALL specified relations. Groups are OR'd: any fully-matched group excludes.
CREATE OR REPLACE FUNCTION check_exclusion_intersection_groups(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
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
          AND m.rule_group_mode = 'exclude_intersection'
          AND m.rule_group_id IS NOT NULL
    LOOP
        v_group_satisfied := TRUE;

        FOR v_check IN
            SELECT m.check_relation, m.check_excluded_relation, m.check_parent_relation, m.check_parent_type
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.rule_group_id = v_group.rule_group_id
              AND m.rule_group_mode = 'exclude_intersection'
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
                    IF subject_has_grant(
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
                          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
                          AND t.object_type = p_object_type
                          AND t.object_id = p_object_id
                          AND t.relation = v_check.check_relation
                    ) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                ELSE
                    IF NOT subject_has_grant(
                        p_subject_type, p_subject_id,
                        p_object_type, p_object_id,
                        v_check.check_relation, p_visited
                    ) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;

                IF v_group_satisfied AND v_check.check_excluded_relation IS NOT NULL THEN
                    IF check_exclusion(p_subject_type, p_subject_id,
                                       v_check.check_excluded_relation,
                                       p_object_type, p_object_id) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;
            ELSE
                IF check_permission(
                    p_subject_type, p_subject_id,
                    v_check.check_relation, p_object_type, p_object_id
                ) = 0 THEN
                    v_group_satisfied := FALSE;
                    EXIT;
                END IF;

                IF v_group_satisfied AND v_check.check_excluded_relation IS NOT NULL THEN
                    IF check_exclusion(p_subject_type, p_subject_id,
                                       v_check.check_excluded_relation,
                                       p_object_type, p_object_id) THEN
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


-- Variant that preserves visited state for cycle prevention
CREATE OR REPLACE FUNCTION check_exclusion_intersection_groups_with_visited(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT,
    p_visited TEXT[]
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
          AND m.rule_group_mode = 'exclude_intersection'
          AND m.rule_group_id IS NOT NULL
    LOOP
        v_group_satisfied := TRUE;

        FOR v_check IN
            SELECT m.check_relation, m.check_excluded_relation, m.check_parent_relation, m.check_parent_type
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.rule_group_id = v_group.rule_group_id
              AND m.rule_group_mode = 'exclude_intersection'
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
                    IF check_permission(
                        p_subject_type, p_subject_id,
                        v_check.check_parent_relation, v_parent.parent_type, v_parent.parent_id
                    ) = 1 THEN
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
                          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
                          AND t.object_type = p_object_type
                          AND t.object_id = p_object_id
                          AND t.relation = v_check.check_relation
                    ) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                ELSE
                    IF NOT subject_has_grant(
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
                IF check_permission(
                    p_subject_type, p_subject_id,
                    v_check.check_relation, p_object_type, p_object_id
                ) = 0 THEN
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
