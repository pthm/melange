-- Fast-path permission check for relations without userset references.
-- Avoids recursive subject_has_grant overhead when schema only uses direct tuples
-- and parent inheritance (no [group#member] patterns).
CREATE OR REPLACE FUNCTION check_permission_simple(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
DECLARE
    v_found INTEGER := 0;
    v_parent_type TEXT;
    v_parent_id TEXT;
    v_parent_rel TEXT;
BEGIN
    -- Self-referential userset: "document:1#writer" checking "viewer" on "document:1"
    IF position('#' in p_subject_id) > 0 AND p_subject_type = p_object_type THEN
        IF substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN
            SELECT 1 INTO v_found
            FROM melange_relation_closure c
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
            LIMIT 1;
        END IF;
    END IF;

    -- Direct tuple lookup with closure expansion
    IF v_found != 1 THEN
        SELECT 1 INTO v_found
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
            AND m.subject_relation IS NULL
            AND (t.subject_id != '*' OR m.subject_wildcard = TRUE)
        WHERE t.subject_type = p_subject_type
          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
          AND t.object_type = p_object_type
          AND t.object_id = p_object_id
        LIMIT 1;
    END IF;

    -- Computed userset matching for userset subjects
    IF v_found IS NULL AND position('#' in p_subject_id) > 0 THEN
        SELECT 1 INTO v_found
        FROM melange_tuples t
        JOIN melange_relation_closure c
            ON c.object_type = p_object_type
            AND c.relation = p_relation
            AND c.satisfying_relation = t.relation
        -- Check if the requested userset relation implies the tuple's userset relation
        -- via the subject type's closure table
        -- e.g., closure(group, member_c4, member) means "member satisfies member_c4"
        JOIN melange_relation_closure subj_c
            ON subj_c.object_type = t.subject_type  -- closure on subject type (e.g., group)
            AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)  -- tuple's relation (e.g., member_c4)
            AND subj_c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)  -- requested relation (e.g., member)
        WHERE t.object_type = p_object_type
          AND t.object_id = p_object_id
          AND t.subject_type = p_subject_type
          AND t.subject_id != '*'
          AND position('#' in t.subject_id) > 0
          -- Match the ID part (before #) of the subject
          AND substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) =
              substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)
        LIMIT 1;
    END IF;

    IF v_found = 1 THEN
        -- Apply exclusions before granting access
        IF check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
            v_found := 0;
        END IF;

        IF v_found = 1 THEN
            RETURN 1;
        END IF;
    END IF;

    -- Parent inheritance: follow linking tuples and check relation on parent object.
    -- subject_type stores the LINKING RELATION (e.g., "org"), not the parent type.
    FOR v_parent_type, v_parent_id, v_parent_rel IN
        SELECT t.subject_type, t.subject_id, am.parent_relation
        FROM melange_relation_closure c
        JOIN melange_model am
          ON am.object_type = p_object_type
         AND am.relation = c.satisfying_relation
         AND am.parent_relation IS NOT NULL
        JOIN melange_tuples t
          ON t.object_type = p_object_type
         AND t.object_id = p_object_id
         AND t.relation = am.subject_type  -- KEY: match linking relation, not parent type
        JOIN melange_model mr
          ON mr.object_type = p_object_type
         AND mr.relation = am.subject_type
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
        -- Use full check_permission for parent since it may have userset patterns
        IF check_permission(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
            IF NOT check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
                RETURN 1;
            END IF;
        END IF;
    END LOOP;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;




-- Intersection check: "viewer: writer and editor" requires BOTH relations.
-- Multiple intersection groups are OR'd; within a group, all must match (AND).
-- Supports nested exclusions: "writer and (editor but not owner)".
CREATE OR REPLACE FUNCTION check_intersection_groups(
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
    -- Detect "This" pattern: direct subject assignments like [user]
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
                -- Tuple-to-userset within intersection: find parent and check
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
                -- Self-reference: "[user] and writer" on viewer checks for direct tuple
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
                        v_check.check_relation, ARRAY[]::TEXT[]
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
                IF NOT subject_has_grant(
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


-- Generic permission checking implementation with visited tracking.
-- This internal version accepts p_visited for cycle detection when called
-- from specialized functions via check_permission_internal.
--
-- Routes to optimal strategy based on relation rules:
--   - Intersection groups first (AND semantics require all conditions)
--   - Userset references via subject_has_grant (recursive group membership)
--   - Simple relations via check_permission_simple (fast path)
--
-- Returns 1 if access granted, 0 if denied.
CREATE OR REPLACE FUNCTION check_permission_generic_internal(
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
    v_has_userset BOOLEAN;
    v_self BOOLEAN := FALSE;
    v_self_in_intersection BOOLEAN := FALSE;
BEGIN
    -- Fast path for self-referential usersets
    IF position('#' in p_subject_id) > 0 AND p_subject_type = p_object_type THEN
        IF substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN
            SELECT TRUE INTO v_self
            FROM melange_relation_closure c
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
            LIMIT 1;
        END IF;
    END IF;

    IF v_self THEN
        RETURN 1;
    END IF;

    -- Intersections must be checked first: they require ALL conditions
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
        -- Check for "This" pattern in intersection (e.g., "[user] and writer")
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

        IF check_intersection_groups(
            p_subject_type, p_subject_id,
            p_relation, p_object_type, p_object_id,
            p_visited
        ) THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited) THEN
                RETURN 0;  -- Excluded
            END IF;

            RETURN 1;
        END IF;

        -- Intersection failed; check for union fallback (e.g., "writer or (editor and owner)")
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

    -- Route to fast path if no userset patterns
    SELECT EXISTS (
        SELECT 1
        FROM melange_userset_rules ur
        WHERE ur.object_type = p_object_type
          AND ur.relation = p_relation
    ) INTO v_has_userset;

    IF NOT v_has_userset THEN
        RETURN check_permission_simple(
            p_subject_type, p_subject_id,
            p_relation, p_object_type, p_object_id
        );
    END IF;

    -- Full userset-aware check
    IF subject_has_grant(
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


-- Generic permission checking implementation.
-- Routes to optimal strategy based on relation rules:
--   - Intersection groups first (AND semantics require all conditions)
--   - Userset references via subject_has_grant (recursive group membership)
--   - Simple relations via check_permission_simple (fast path)
--
-- Returns 1 if access granted, 0 if denied.
--
-- Note: The main check_permission() entry point is generated at migration time
-- and dispatches to specialized per-relation functions. This generic version
-- is used as a fallback when no specialized function exists.
CREATE OR REPLACE FUNCTION check_permission_generic(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT check_permission_generic_internal(
        p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[]
    );
$$ LANGUAGE sql STABLE;


-- Generic permission check ignoring wildcards with visited tracking.
-- This internal version accepts p_visited for cycle detection.
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
-- See check_permission_generic for details on the dispatch pattern.
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
-- to specialized per-relation functions. This default version just calls
-- the generic implementation.
CREATE OR REPLACE FUNCTION check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT check_permission_generic(
        p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id
    );
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
