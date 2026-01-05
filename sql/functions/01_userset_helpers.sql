-- Melange infrastructure functions
-- These are generic permission checking functions that work with melange_model,
-- melange_relation_closure, and melange_tuples.
-- IMPORTANT: These functions are domain-agnostic. Domain-specific logic belongs in the Go layer.
--
-- This file is idempotent and applied by `melange migrate`.


-- =============================================================================
-- USERSET REFERENCE HELPER FUNCTIONS
--
-- These functions support [type#relation] patterns where a subject gains access
-- via group/team membership rather than direct tuple assignment.
--
-- Core insight: instead of storing "user:alice -> document:1" directly, schemas
-- can express "group:engineering#member -> document:1" meaning "anyone who is
-- a member of engineering can access document:1". This indirection enables
-- role-based access without duplicating tuples for each user.
-- =============================================================================

-- Check if a subject has a specific relation on a specific object.
-- Handles:
--   1. Direct tuples (subject has relation directly)
--   2. Userset references (subject is member of a group that has the relation)
--   3. Implied relations via closure table
--   4. Parent inheritance (relation inherited from parent object)
--
-- This is the on-demand (lazy) version for check_permission - only evaluates
-- the specific membership needed, not all possible grants.
--
-- Parameters:
--   p_subject_type, p_subject_id: The subject to check
--   p_object_type, p_object_id: The object to check access on
--   p_relation: The relation to check
--   p_visited: Array of visited nodes for cycle detection (internal use)
--
-- Returns TRUE if the subject has the relation on the object.
CREATE OR REPLACE FUNCTION subject_has_grant(
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

    -- Prevent infinite loops in cyclic permission graphs
    IF v_visit_key = ANY(p_visited) THEN
        RETURN FALSE;
    END IF;

    -- Guard against pathological schemas that would exhaust stack
    IF COALESCE(array_length(p_visited, 1), 0) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex: depth limit exceeded' USING ERRCODE = 'M2002';
    END IF;

    -- Handle self-referential usersets: "document:1#writer" checking "viewer" on "document:1"
    -- succeeds if writer satisfies viewer in the closure table
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

    -- Intersections require ALL conditions to be met (AND semantics).
    -- Must evaluate them before other rules since partial matches don't grant access.
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

        IF check_intersection_groups(
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

    -- Direct tuple lookup: leverages closure table to check all implied relations
    -- in a single query (owner implies admin implies member, etc.)
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
        AND m.parent_relation IS NULL   -- Not a parent rule
        -- Allow either:
        -- 1. Direct assignment (no subject_relation in model)
        -- 2. Userset reference where the subject_id matches the pattern
        AND (
            m.subject_relation IS NULL
            OR (
                m.subject_relation IS NOT NULL
                AND position('#' in p_subject_id) > 0
                AND substring(p_subject_id from position('#' in p_subject_id) + 1) = m.subject_relation
            )
        )
        AND (t.subject_id != '*' OR m.subject_wildcard = TRUE)
    WHERE t.object_type = p_object_type
      AND t.object_id = p_object_id
      AND t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    LIMIT 1;

    IF v_found THEN
        IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
            RETURN FALSE;
        END IF;
        RETURN TRUE;
    END IF;

    -- Computed userset matching: tuple grants "group#owner", we're checking "group#member".
    -- If owner implies member via closure, the grant should apply.
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
            AND m.subject_relation IS NOT NULL  -- userset reference rule
            AND m.parent_relation IS NULL
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

        IF v_found THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;
    END IF;

    -- Userset expansion: tuple says "group:x#member can view document:1",
    -- so recursively check if subject is a member of group:x
    FOR v_userset IN
        SELECT
            t.subject_type AS group_type,
            -- Parse the group ID: everything before # or the whole string if no #
            CASE
                WHEN position('#' in t.subject_id) > 0
                THEN substring(t.subject_id from 1 for position('#' in t.subject_id) - 1)
                ELSE t.subject_id
            END AS group_id,
            -- Parse the required relation: everything after # or from model if no #
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
          AND (
              position('#' in t.subject_id) = 0
              OR substring(t.subject_id from position('#' in t.subject_id) + 1) = ur.subject_relation_satisfying
          )
    LOOP
        -- Recursively check if subject has the required relation on the group
        IF subject_has_grant(
            p_subject_type, p_subject_id,
            v_userset.group_type, v_userset.group_id,
            v_userset.required_relation,
            p_visited || v_visit_key  -- append current to visited for cycle detection
        ) THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;
    END LOOP;

    -- Parent inheritance: "viewer from org" means follow the org tuple and
    -- check if subject has viewer on that parent organization
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
            AND t.relation = m.subject_type  -- linking relation
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
        -- Recursively check parent (which may itself have usersets)
        IF subject_has_grant(
            p_subject_type, p_subject_id,
            v_parent.parent_type, v_parent.parent_id,
            v_parent.required_relation,
            p_visited || v_visit_key
        ) THEN
            IF check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited || v_visit_key) THEN
                RETURN FALSE;
            END IF;
            RETURN TRUE;
        END IF;
    END LOOP;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;


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
