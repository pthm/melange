-- Melange infrastructure functions
-- These are generic permission checking functions that work with melange_model,
-- melange_relation_closure, and melange_tuples.
-- IMPORTANT: These functions are domain-agnostic. Domain-specific logic belongs in the Go layer.
--
-- This file is idempotent and applied by `melange migrate`.


-- =============================================================================
-- USERSET REFERENCE HELPER FUNCTIONS
-- These functions support [type#relation] patterns where a subject gains access
-- via group/team membership rather than direct tuple assignment.
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
    p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS BOOLEAN AS $$
DECLARE
    v_found BOOLEAN := FALSE;
    v_userset RECORD;
    v_parent RECORD;
    v_visit_key TEXT;
BEGIN
    -- Build unique key for cycle detection
    v_visit_key := p_object_type || ':' || p_object_id || ':' || p_relation;

    -- Cycle detection
    IF v_visit_key = ANY(p_visited) THEN
        RETURN FALSE;
    END IF;

    -- Depth protection (array length serves as depth counter)
    -- Note: array_length returns NULL for empty arrays, so use COALESCE
    IF COALESCE(array_length(p_visited, 1), 0) >= 10 THEN
        RETURN FALSE;
    END IF;

    -- 1. Check direct tuple match via closure table
    -- This handles: direct match + all implied-by relations in one query
    -- Also handles userset tuple match where subject_id contains #relation
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
    WHERE t.object_type = p_object_type
      AND t.object_id = p_object_id
      AND t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    LIMIT 1;

    IF v_found THEN
        RETURN TRUE;
    END IF;

    -- 1b. Check for computed usersets: when p_subject_id is a userset (e.g., fga#member)
    -- and there are tuples with a different userset relation that is implied by the requested one
    -- (e.g., tuple has fga#member_c4 and we check fga#member, where member implies member_c4)
    --
    -- The key insight: if the model says member -> member_c1 -> ... -> member_c4, then
    -- a tuple granting to "group#member_c4" should match a check for "group#member"
    -- because anyone with "member" also has "member_c4".
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
            RETURN TRUE;
        END IF;
    END IF;

    -- 2. Check userset references from tuples
    -- Tuples with userset subjects store subject_id as "id#relation" (e.g., "x#member")
    -- For example: tuple (group, x#member, viewer, document, 1) means
    -- "members of group:x have viewer on document:1"
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
                THEN substring(t.subject_id from position('#' in t.subject_id) + 1)
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
    LOOP
        -- Recursively check if subject has the required relation on the group
        IF subject_has_grant(
            p_subject_type, p_subject_id,
            v_userset.group_type, v_userset.group_id,
            v_userset.required_relation,
            p_visited || v_visit_key  -- append current to visited for cycle detection
        ) THEN
            RETURN TRUE;
        END IF;
    END LOOP;

    -- 3. Check parent inheritance
    -- For rules like "can_read from org", check if subject has the
    -- parent_relation on the parent object.
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
            RETURN TRUE;
        END IF;
    END LOOP;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;


-- Compute all (object_type, object_id, relation) tuples that a subject can access.
-- Uses iterative fixpoint computation to handle:
--   1. Direct tuples
--   2. Userset references (nested group membership)
--   3. Parent inheritance
--   4. Implied relations via closure table
--
-- This is the full enumeration version for list_accessible_objects.
-- For check_permission, prefer subject_has_grant() for on-demand evaluation.
--
-- Parameters:
--   p_subject_type, p_subject_id: The subject to enumerate access for
--
-- Returns a table of (object_type, object_id, relation) tuples the subject can access.
-- Note: VOLATILE because it uses temp tables for iterative computation.
CREATE OR REPLACE FUNCTION subject_grants(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE (object_type TEXT, object_id TEXT, relation TEXT) AS $$
DECLARE
    v_iteration INTEGER := 0;
    v_new_count INTEGER;
    v_max_iterations CONSTANT INTEGER := 10;  -- depth limit
BEGIN
    -- Create temp table for iterative fixpoint computation
    -- Using ON COMMIT DROP for automatic cleanup
    CREATE TEMP TABLE IF NOT EXISTS _sg_grants (
        object_type TEXT NOT NULL,
        object_id TEXT NOT NULL,
        relation TEXT NOT NULL,
        iteration INTEGER NOT NULL,  -- track when discovered for debugging
        PRIMARY KEY (object_type, object_id, relation)
    ) ON COMMIT DROP;

    -- Clear any existing data (in case function is called multiple times in same transaction)
    TRUNCATE _sg_grants;

    -- Seed: direct tuples the subject has
    -- Expand via closure to get all relations these tuples satisfy
    -- Handles both direct assignments (m.subject_relation IS NULL) and
    -- userset subjects (p_subject_id contains #relation matching m.subject_relation)
    --
    -- For userset subjects like group:fga#member, we also need to handle computed
    -- usersets where the tuple might store a different relation (e.g., fga#owner)
    -- that implies the requested relation (member) via the subject type's closure.
    INSERT INTO _sg_grants (object_type, object_id, relation, iteration)
    SELECT DISTINCT
        t.object_type,
        t.object_id,
        c.relation,
        0
    FROM melange_tuples t
    JOIN melange_model m
        ON m.object_type = t.object_type
        AND m.relation = t.relation
        AND m.subject_type = t.subject_type
        AND m.parent_relation IS NULL   -- Not a parent rule
        -- Accept either:
        -- 1. Direct assignment (no subject_relation in model)
        -- 2. Userset reference where subject_id matches the pattern
        AND (
            m.subject_relation IS NULL
            OR (
                m.subject_relation IS NOT NULL
                AND position('#' in p_subject_id) > 0
                AND substring(p_subject_id from position('#' in p_subject_id) + 1) = m.subject_relation
            )
        )
    JOIN melange_relation_closure c
        ON c.object_type = t.object_type
        AND c.satisfying_relation = t.relation
    WHERE t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    ON CONFLICT DO NOTHING;

    -- Additional seed for computed usersets: when p_subject_id is a userset (e.g., fga#member)
    -- and there are tuples with a different userset relation that is implied by the requested one
    -- (e.g., tuple has fga#member_c4 and we query fga#member, where member implies member_c4)
    IF position('#' in p_subject_id) > 0 THEN
        INSERT INTO _sg_grants (object_type, object_id, relation, iteration)
        SELECT DISTINCT
            t.object_type,
            t.object_id,
            obj_c.relation,
            0
        FROM melange_tuples t
        JOIN melange_model m
            ON m.object_type = t.object_type
            AND m.relation = t.relation
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
        JOIN melange_relation_closure obj_c
            ON obj_c.object_type = t.object_type
            AND obj_c.satisfying_relation = t.relation
        WHERE t.subject_type = p_subject_type
          AND t.subject_id != '*'
          AND position('#' in t.subject_id) > 0
          -- Match the ID part (before #) of the subject
          AND substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) =
              substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)
        ON CONFLICT DO NOTHING;
    END IF;

    -- Iterative expansion until fixpoint or depth limit
    LOOP
        v_iteration := v_iteration + 1;

        IF v_iteration > v_max_iterations THEN
            RAISE NOTICE 'subject_grants: depth limit (%) reached for subject %:%',
                         v_max_iterations, p_subject_type, p_subject_id;
            EXIT;
        END IF;

        -- Expand via userset references AND parent inheritance in one pass
        WITH userset_expansion AS (
            -- If subject has relation R on group G, and there's a tuple
            -- (G, group_id#R) -> relation -> object with userset rule [G#R],
            -- then subject has that relation on the object.
            --
            -- Tuples with userset subjects are stored as:
            --   subject_type=group, subject_id=x#member, relation=viewer, object_type=document, object_id=1
            -- This means "members of group:x have viewer on document:1"
            --
            -- We need to match:
            --   - g.object_type = group (the group type matches)
            --   - g.object_id = x (the specific group)
            --   - g.relation = member (the required relation on the group)
            --   - t.subject_id = x#member (matches g.object_id || '#' || g.relation)
            SELECT DISTINCT
                t.object_type,
                t.object_id,
                ur.relation
            FROM _sg_grants g
            JOIN melange_userset_rules ur
                ON ur.subject_type = g.object_type   -- group type matches
                AND ur.subject_relation = g.relation -- required relation matches
            JOIN melange_tuples t
                ON t.subject_type = ur.subject_type
                -- Match userset tuple format: subject_id is "id#relation"
                AND t.subject_id = g.object_id || '#' || ur.subject_relation
                AND t.object_type = ur.object_type
                AND t.relation = ur.tuple_relation
            WHERE NOT EXISTS (
                SELECT 1 FROM _sg_grants existing
                WHERE existing.object_type = t.object_type
                  AND existing.object_id = t.object_id
                  AND existing.relation = ur.relation
            )
        ),
        parent_expansion AS (
            -- If subject has parent_relation on parent P, and there's an object O
            -- linked to P via linking_relation with rule "X from P",
            -- then subject has relation X on O.
            SELECT DISTINCT
                child.object_type,
                child.object_id,
                c.relation
            FROM _sg_grants g
            JOIN melange_model m
                ON m.parent_relation = g.relation      -- parent relation matches
                AND m.parent_relation IS NOT NULL      -- is parent inheritance
            JOIN melange_tuples child
                ON child.subject_type = g.object_type  -- parent type
                AND child.subject_id = g.object_id     -- specific parent
                AND child.object_type = m.object_type
                AND child.relation = m.subject_type    -- linking relation
            JOIN melange_relation_closure c
                ON c.object_type = child.object_type
                AND c.satisfying_relation = m.relation
            WHERE NOT EXISTS (
                SELECT 1 FROM _sg_grants existing
                WHERE existing.object_type = child.object_type
                  AND existing.object_id = child.object_id
                  AND existing.relation = c.relation
            )
        ),
        all_new AS (
            SELECT ue.object_type, ue.object_id, ue.relation FROM userset_expansion ue
            UNION
            SELECT pe.object_type, pe.object_id, pe.relation FROM parent_expansion pe
        )
        INSERT INTO _sg_grants (object_type, object_id, relation, iteration)
        SELECT an.object_type, an.object_id, an.relation, v_iteration
        FROM all_new an
        ON CONFLICT DO NOTHING;

        GET DIAGNOSTICS v_new_count = ROW_COUNT;

        -- Fixpoint reached - no new grants discovered
        IF v_new_count = 0 THEN
            EXIT;
        END IF;
    END LOOP;

    -- Return all discovered grants
    RETURN QUERY
    SELECT g.object_type, g.object_id, g.relation
    FROM _sg_grants g;
END;
$$ LANGUAGE plpgsql VOLATILE;


-- Check if a subject has an excluded relation on an object.
-- Uses a recursive CTE to handle all cases uniformly:
--   1. Direct tuples (e.g., author)
--   2. Implied-by relations (via closure table)
--   3. Parent inheritance (via recursive CTE)
--
-- This avoids split-path logic and handles edge cases in a single query.
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


-- Check if a subject is excluded by ANY of the exclusion rules for a relation.
-- For nested exclusions like "(writer but not editor) but not owner",
-- this checks all exclusion rows and returns TRUE if ANY matches.
--
-- Parameters:
--   p_subject_type, p_subject_id: The subject to check
--   p_relation: The relation being checked (to find its exclusions)
--   p_object_type, p_object_id: The object to check exclusions on
--
-- Returns TRUE if the subject should be excluded, FALSE otherwise.
CREATE OR REPLACE FUNCTION check_all_exclusions(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS BOOLEAN AS $$
DECLARE
    v_excluded TEXT;
BEGIN
    -- Iterate through ALL exclusions for this relation
    FOR v_excluded IN
        SELECT em.excluded_relation
        FROM melange_model em
        WHERE em.object_type = p_object_type
          AND em.relation = p_relation
          AND em.excluded_relation IS NOT NULL
    LOOP
        IF check_exclusion(p_subject_type, p_subject_id, v_excluded, p_object_type, p_object_id) THEN
            RETURN TRUE;  -- Subject is excluded
        END IF;
    END LOOP;

    RETURN FALSE;  -- Not excluded
END;
$$ LANGUAGE plpgsql STABLE;


-- Simple permission check (no userset reference support)
-- This is the original check_permission for backward compatibility and as a fast path
-- when no userset references are defined for a relation.
--
-- Checks if a subject has a specific permission on an object by:
-- 1. Direct tuple match using closure table (handles direct + all implied relations in one query)
-- 2. Parent relation inheritance (e.g., org -> repo -> change)
-- 3. Exclusion check (for "but not" relations)
--
-- The closure table eliminates recursive implied-by traversal, reducing multiple
-- model queries to a single JOIN operation.
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
    -- 1. Check direct tuple match using closure table
    -- This handles: direct match + all implied-by relations in one query
    -- The closure table contains all relations that satisfy p_relation (including itself)
    SELECT 1 INTO v_found
    FROM melange_tuples t
    JOIN melange_relation_closure c
        ON c.object_type = p_object_type
        AND c.relation = p_relation
        AND c.satisfying_relation = t.relation
    WHERE t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
      AND t.object_type = p_object_type
      AND t.object_id = p_object_id
    LIMIT 1;

    -- 1b. Check for computed usersets: when p_subject_id is a userset (e.g., fga#member)
    -- and there are tuples with a different userset relation that is implied by the requested one
    -- (e.g., tuple has fga#member_c4 and we check fga#member, where member implies member_c4)
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
        -- Check for ALL exclusions on the matching relation.
        -- For nested exclusions like "(writer but not editor) but not owner",
        -- there will be multiple exclusion rows (one for "editor", one for "owner").
        -- The subject must NOT have ANY of the excluded relations.
        IF check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
            v_found := 0;
        END IF;

        IF v_found = 1 THEN
            RETURN 1;
        END IF;
    END IF;

    -- 2. Check parent relations (inheritance from parent object)
    -- Uses schema-driven parent relations from melange_model.
    -- subject_type stores the LINKING RELATION (e.g., "org") not the parent type.
    -- This allows correct filtering when an object has multiple relations to the same type.
    --
    -- Example: "can_read from org" stores subject_type="org", parent_relation="can_read"
    -- The query finds tuples where t.relation="org" and checks can_read on the parent.
    --
    -- IMPORTANT: We must check parent inheritance on ALL relations that satisfy p_relation
    -- via the closure table, not just p_relation itself. For example:
    --   repository.can_read: reader
    --   repository.reader: [user] or can_read from org
    -- When checking can_read, reader satisfies it via closure, so we must also check
    -- reader's parent inheritance rule (can_read from org).
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
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
    LOOP
        -- Check if the parent relation is satisfied
        -- IMPORTANT: Must use check_permission (not check_permission_simple) here because
        -- the parent object may have userset references that require full evaluation.
        -- E.g., document.viewer: viewer from parent -> folder.viewer: [group#member]
        IF check_permission(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
            -- Check for ALL exclusions (using helper function)
            IF NOT check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
                RETURN 1;
            END IF;
        END IF;
    END LOOP;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;


-- Check intersection groups for a relation.
-- For "viewer: writer and editor", this checks if the subject has BOTH
-- writer AND editor on the object.
--
-- For "viewer: writer and (editor but not owner)", this checks if the subject
-- has writer AND (has editor BUT NOT owner) on the object.
--
-- Returns TRUE if ANY intersection group is fully satisfied (all relations in group match).
-- Returns FALSE if no intersection groups are satisfied.
--
-- Each intersection group has a unique rule_group_id. All rules in a group must
-- be satisfied (AND semantics), but groups themselves are OR'd together.
CREATE OR REPLACE FUNCTION check_intersection_groups(
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
BEGIN
    -- Find all distinct intersection groups for this relation
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

        -- Check if ALL relations in this group are satisfied
        -- Now also fetches check_excluded_relation for per-check exclusions
        FOR v_check IN
            SELECT m.check_relation, m.check_excluded_relation
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.rule_group_id = v_group.rule_group_id
              AND m.rule_group_mode = 'intersection'
              AND m.check_relation IS NOT NULL
        LOOP
            IF v_check.check_relation = p_relation THEN
                -- Self-reference (This pattern): "[user] and writer" on viewer
                -- Check for direct tuple since there's no subject_type entry
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

                -- Check for exclusion on this self-reference check
                IF v_group_satisfied AND v_check.check_excluded_relation IS NOT NULL THEN
                    IF check_exclusion(p_subject_type, p_subject_id,
                                       v_check.check_excluded_relation,
                                       p_object_type, p_object_id) THEN
                        v_group_satisfied := FALSE;
                        EXIT;
                    END IF;
                END IF;
            ELSE
                -- Use subject_has_grant to check other relations (supports userset patterns)
                IF NOT subject_has_grant(
                    p_subject_type, p_subject_id,
                    p_object_type, p_object_id,
                    v_check.check_relation, ARRAY[]::TEXT[]
                ) THEN
                    v_group_satisfied := FALSE;
                    EXIT;  -- No need to check more relations in this group
                END IF;

                -- Check for exclusion on this check_relation
                -- For "writer and (editor but not owner)", when check_relation=editor,
                -- check_excluded_relation=owner. If subject has owner, they're excluded.
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

        -- If this group is fully satisfied, return true
        IF v_group_satisfied THEN
            RETURN TRUE;
        END IF;
    END LOOP;

    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE;


-- Generic permission check with userset reference and intersection support
-- This is the main entry point for permission checking. It detects the type of
-- relation rules and uses the appropriate evaluation strategy:
-- 1. Intersection groups: uses check_intersection_groups (AND semantics)
-- 2. Userset references: uses subject_has_grant (lazy userset evaluation)
-- 3. Simple relations: uses check_permission_simple (fast path)
--
-- Parameters:
--   p_subject_type, p_subject_id: The subject requesting access
--   p_relation: The relation to check
--   p_object_type, p_object_id: The object to check access on
--
-- Returns 1 if access is granted, 0 if denied.
CREATE OR REPLACE FUNCTION check_permission(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
DECLARE
    v_has_intersection BOOLEAN;
    v_has_other_rules BOOLEAN;
    v_has_userset BOOLEAN;
BEGIN
    -- Check if any intersection rules exist for this relation
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
        -- Check intersection groups
        IF check_intersection_groups(
            p_subject_type, p_subject_id,
            p_relation, p_object_type, p_object_id
        ) THEN
            -- Check for ALL exclusions (supports nested exclusions)
            IF check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
                RETURN 0;  -- Excluded
            END IF;

            RETURN 1;  -- Access granted via intersection
        END IF;

        -- Intersection not satisfied. Check if there are OTHER (non-intersection) rules
        -- to fall through to (e.g., "writer or (editor and owner)" has both).
        -- If there are no other rules, return 0 immediately.
        SELECT EXISTS (
            SELECT 1
            FROM melange_relation_closure c
            JOIN melange_model m
                ON m.object_type = c.object_type
                AND m.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND (m.rule_group_mode IS NULL OR m.rule_group_mode != 'intersection')
              AND (m.subject_type IS NOT NULL OR m.implied_by IS NOT NULL OR m.parent_relation IS NOT NULL)
        ) INTO v_has_other_rules;

        IF NOT v_has_other_rules THEN
            RETURN 0;  -- Intersection-only relation, no fallback
        END IF;
    END IF;

    -- Fast path: check if any userset references exist for this relation
    -- If not, we can use the simpler (faster) check_permission_simple
    SELECT EXISTS (
        SELECT 1
        FROM melange_userset_rules ur
        WHERE ur.object_type = p_object_type
          AND ur.relation = p_relation
    ) INTO v_has_userset;

    IF NOT v_has_userset THEN
        -- No userset references, use fast path
        RETURN check_permission_simple(
            p_subject_type, p_subject_id,
            p_relation, p_object_type, p_object_id
        );
    END IF;

    -- Full userset-aware check using subject_has_grant
    -- First check if access is granted
    IF subject_has_grant(
        p_subject_type, p_subject_id,
        p_object_type, p_object_id,
        p_relation, ARRAY[]::TEXT[]
    ) THEN
        -- Check for ALL exclusions (supports nested exclusions)
        IF check_all_exclusions(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id) THEN
            RETURN 0;  -- Excluded
        END IF;

        RETURN 1;  -- Access granted
    END IF;

    RETURN 0;  -- No access
END;
$$ LANGUAGE plpgsql STABLE;


-- Generic tuple existence check
-- Supports LIKE patterns for relation matching (e.g., 'collaborator_%')
-- Includes wildcard matching: subject_id = '*' represents type:*
CREATE OR REPLACE FUNCTION has_tuple(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation_pattern TEXT,
    p_object_type TEXT,
    p_object_id TEXT
) RETURNS INTEGER AS $$
BEGIN
    RETURN (
        SELECT CASE WHEN EXISTS (
            SELECT 1 FROM melange_tuples t
            WHERE t.subject_type = p_subject_type
              AND (t.subject_id = p_subject_id OR t.subject_id = '*')
              AND t.relation LIKE p_relation_pattern
              AND t.object_type = p_object_type
              AND t.object_id = p_object_id
        ) THEN 1 ELSE 0 END
    );
END;
$$ LANGUAGE plpgsql STABLE;


-- List objects a subject can access
-- Simplified implementation: finds candidate objects and filters using check_permission.
-- This ensures consistency with permission checks at the cost of performance.
--
-- Strategy:
-- 1. Find ALL objects of the target type (comprehensive candidate set)
-- 2. Filter candidates using check_permission to ensure correctness
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    -- Find ALL distinct objects of the target type from tuples
    -- This is comprehensive - any object that exists in the tuples table
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = p_object_type
      -- Filter using check_permission for correctness
      AND check_permission(
          p_subject_type,
          p_subject_id,
          p_relation,
          p_object_type,
          t.object_id
      ) = 1;
END;
$$ LANGUAGE plpgsql STABLE;


-- List subjects that have access to an object
-- Simplified implementation: finds candidate subjects and filters using check_permission.
-- This ensures consistency with permission checks at the cost of performance.
--
-- Supports userset filters: p_subject_type can be "group#member" to filter for
-- userset references (tuples with subject_id containing #relation).
--
-- Strategy:
-- 1. Find ALL subjects of the target type (comprehensive candidate set)
-- 2. For userset filters, also include userset reference candidates
-- 3. Filter candidates using check_permission to ensure correctness
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;     -- Parsed type part (e.g., 'group' from 'group#member')
    v_filter_relation TEXT; -- Parsed relation part (e.g., 'member' from 'group#member')
BEGIN
    -- Parse userset filter: "group#member" -> type=group, relation=member
    IF position('#' in p_subject_type) > 0 THEN
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);
    ELSE
        v_filter_type := p_subject_type;
        v_filter_relation := NULL;
    END IF;

    -- For userset filters (e.g., "group#member"), we need a different approach
    IF v_filter_relation IS NOT NULL THEN
        -- Return userset references tied to this object and relation closure.
        -- For filter group#member, include tuples like group:foo#member or
        -- group:foo#member_c4 when member implies member_c4.
        RETURN QUERY
        WITH RECURSIVE seed_relations AS (
            SELECT p_relation AS relation
            UNION
            SELECT DISTINCT m.check_relation
            FROM melange_relation_closure c
            JOIN melange_model m
                ON m.object_type = c.object_type
                AND m.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND m.rule_group_mode = 'intersection'
              AND m.check_relation IS NOT NULL
        ),
        userset_nodes AS (
            SELECT p_object_type AS object_type, p_object_id AS object_id, s.relation AS relation, 0 AS depth
            FROM seed_relations s
            UNION
            SELECT t.subject_type AS object_type, split.id AS object_id, split.rel AS relation, n.depth + 1 AS depth
            FROM userset_nodes n
            JOIN melange_relation_closure c
                ON c.object_type = n.object_type
                AND c.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = c.satisfying_relation
            CROSS JOIN LATERAL (
                SELECT
                    substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) AS id,
                    substring(t.subject_id from position('#' in t.subject_id) + 1) AS rel
            ) AS split
            WHERE n.depth < 10
              AND position('#' in t.subject_id) > 0
        )
        SELECT DISTINCT
            substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
        FROM userset_nodes n
        JOIN melange_relation_closure c
            ON c.object_type = n.object_type
            AND c.relation = n.relation
        JOIN melange_tuples t
            ON t.object_type = n.object_type
            AND t.object_id = n.object_id
            AND t.relation = c.satisfying_relation
        WHERE t.subject_type = v_filter_type
          AND position('#' in t.subject_id) > 0
          AND (
              substring(t.subject_id from position('#' in t.subject_id) + 1) = v_filter_relation
              OR EXISTS (
                  SELECT 1 FROM melange_relation_closure subj_c
                  WHERE subj_c.object_type = v_filter_type
                    AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
                    AND subj_c.satisfying_relation = v_filter_relation
              )
          )
          AND check_permission(
              v_filter_type,
              substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation,
              p_relation,
              p_object_type,
              p_object_id
          ) = 1;
    ELSE
        -- Regular subject filter: only subjects tied to this object and relation closure,
        -- including userset expansion via related group objects.
        RETURN QUERY
        WITH RECURSIVE seed_relations AS (
            SELECT p_relation AS relation
            UNION
            SELECT DISTINCT m.check_relation
            FROM melange_relation_closure c
            JOIN melange_model m
                ON m.object_type = c.object_type
                AND m.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND m.rule_group_mode = 'intersection'
              AND m.check_relation IS NOT NULL
        ),
        userset_nodes AS (
            SELECT p_object_type AS object_type, p_object_id AS object_id, s.relation AS relation, 0 AS depth
            FROM seed_relations s
            UNION
            SELECT t.subject_type AS object_type, split.id AS object_id, split.rel AS relation, n.depth + 1 AS depth
            FROM userset_nodes n
            JOIN melange_relation_closure c
                ON c.object_type = n.object_type
                AND c.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = c.satisfying_relation
            CROSS JOIN LATERAL (
                SELECT
                    substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) AS id,
                    substring(t.subject_id from position('#' in t.subject_id) + 1) AS rel
            ) AS split
            WHERE n.depth < 10
              AND position('#' in t.subject_id) > 0
        )
        SELECT DISTINCT t.subject_id
        FROM userset_nodes n
        JOIN melange_relation_closure c
            ON c.object_type = n.object_type
            AND c.relation = n.relation
        JOIN melange_tuples t
            ON t.object_type = n.object_type
            AND t.object_id = n.object_id
            AND t.relation = c.satisfying_relation
        WHERE t.subject_type = v_filter_type
          AND (position('#' in t.subject_id) = 0 OR t.subject_id = '*')
          AND check_permission(
              v_filter_type,
              t.subject_id,
              p_relation,
              p_object_type,
              p_object_id
          ) = 1;
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;
