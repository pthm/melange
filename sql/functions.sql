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
                ELSE m.subject_relation
            END AS required_relation
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = p_object_type
            AND m.relation = c.satisfying_relation
            AND m.subject_relation IS NOT NULL  -- userset reference rule
            AND m.parent_relation IS NULL       -- not a parent inheritance rule
        JOIN melange_tuples t
            ON t.object_type = p_object_type
            AND t.object_id = p_object_id
            AND t.relation = c.satisfying_relation
            AND t.subject_type = m.subject_type
            -- Tuple subject_id must match the model's expected pattern
            -- Either it has # (userset in tuple) or model defines the relation
            AND (position('#' in t.subject_id) > 0 OR m.subject_relation IS NOT NULL)
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
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

    -- Seed: direct tuples the subject has (only for rules without userset reference)
    -- Expand via closure to get all relations these tuples satisfy
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
        AND m.subject_relation IS NULL  -- Direct assignment only
        AND m.parent_relation IS NULL   -- Not a parent rule
    JOIN melange_relation_closure c
        ON c.object_type = t.object_type
        AND c.satisfying_relation = t.relation
    WHERE t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    ON CONFLICT DO NOTHING;

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
                c.relation
            FROM _sg_grants g
            JOIN melange_model m
                ON m.subject_type = g.object_type      -- group type matches
                AND m.subject_relation = g.relation    -- required relation matches
                AND m.subject_relation IS NOT NULL     -- is a userset rule
                AND m.parent_relation IS NULL          -- not parent inheritance
            JOIN melange_tuples t
                ON t.subject_type = m.subject_type
                -- Match userset tuple format: subject_id is "id#relation"
                AND t.subject_id = g.object_id || '#' || m.subject_relation
                AND t.object_type = m.object_type
                AND t.relation = m.relation
            JOIN melange_relation_closure c
                ON c.object_type = t.object_type
                AND c.satisfying_relation = m.relation
            WHERE NOT EXISTS (
                SELECT 1 FROM _sg_grants existing
                WHERE existing.object_type = t.object_type
                  AND existing.object_id = t.object_id
                  AND existing.relation = c.relation
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
    RETURN EXISTS (
        WITH RECURSIVE exclusion_sources AS (
            -- Base case: the target object itself
            SELECT
                p_object_type AS source_type,
                p_object_id AS source_id,
                p_excluded_relation AS check_rel,
                0 AS depth

            UNION ALL

            -- Recursive case: walk up to parent objects if exclusion has parent inheritance
            SELECT
                t.subject_type,
                t.subject_id,
                m.parent_relation,
                es.depth + 1
            FROM exclusion_sources es
            JOIN melange_tuples t
                ON t.object_type = es.source_type
                AND t.object_id = es.source_id
            JOIN melange_model m
                ON m.object_type = es.source_type
                AND m.relation = es.check_rel
                AND m.parent_relation IS NOT NULL
                AND m.subject_type = t.relation
            WHERE es.depth < 10
        )
        -- Check if subject has any satisfying relation at any exclusion source
        SELECT 1
        FROM exclusion_sources es
        JOIN melange_relation_closure c
            ON c.object_type = es.source_type
            AND c.relation = es.check_rel
        JOIN melange_tuples t
            ON t.object_type = es.source_type
            AND t.object_id = es.source_id
            AND t.relation = c.satisfying_relation
        WHERE t.subject_type = p_subject_type
          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
        LIMIT 1
    );
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
    v_excluded_rel TEXT;
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

    IF v_found = 1 THEN
        -- Check for exclusions on the matching relation
        DECLARE
            v_excluded TEXT;
        BEGIN
            SELECT am.excluded_relation INTO v_excluded
            FROM melange_model am
            WHERE am.object_type = p_object_type
              AND am.relation = p_relation
              AND am.excluded_relation IS NOT NULL
              AND am.parent_relation IS NULL
            LIMIT 1;

            IF v_excluded IS NOT NULL THEN
                -- Use CTE-based exclusion check (handles all cases uniformly)
                IF check_exclusion(p_subject_type, p_subject_id, v_excluded, p_object_type, p_object_id) THEN
                    v_found := 0;
                END IF;
            END IF;
        END;

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
    FOR v_parent_type, v_parent_id, v_parent_rel, v_excluded_rel IN
        SELECT t.subject_type, t.subject_id, am.parent_relation, am.excluded_relation
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
        -- Check if the parent relation is satisfied (recursively uses closure for parent)
        IF check_permission_simple(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
            -- Check for exclusion using CTE-based helper (handles all cases uniformly)
            IF v_excluded_rel IS NOT NULL THEN
                IF check_exclusion(p_subject_type, p_subject_id, v_excluded_rel, p_object_type, p_object_id) THEN
                    CONTINUE; -- Excluded by "but not", try next parent
                END IF;
            END IF;
            RETURN 1;
        END IF;
    END LOOP;

    RETURN 0;
END;
$$ LANGUAGE plpgsql STABLE;


-- Check intersection groups for a relation.
-- For "viewer: writer and editor", this checks if the subject has BOTH
-- writer AND editor on the object.
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
    v_check_rel TEXT;
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
        FOR v_check_rel IN
            SELECT m.check_relation
            FROM melange_model m
            WHERE m.object_type = p_object_type
              AND m.rule_group_id = v_group.rule_group_id
              AND m.rule_group_mode = 'intersection'
              AND m.check_relation IS NOT NULL
        LOOP
            IF v_check_rel = p_relation THEN
                -- Self-reference (This pattern): "[user] and writer" on viewer
                -- Check for direct tuple since there's no subject_type entry
                IF NOT EXISTS (
                    SELECT 1 FROM melange_tuples t
                    WHERE t.subject_type = p_subject_type
                      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
                      AND t.object_type = p_object_type
                      AND t.object_id = p_object_id
                      AND t.relation = v_check_rel
                ) THEN
                    v_group_satisfied := FALSE;
                    EXIT;
                END IF;
            ELSE
                -- Use subject_has_grant to check other relations (supports userset patterns)
                IF NOT subject_has_grant(
                    p_subject_type, p_subject_id,
                    p_object_type, p_object_id,
                    v_check_rel, ARRAY[]::TEXT[]
                ) THEN
                    v_group_satisfied := FALSE;
                    EXIT;  -- No need to check more relations in this group
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
    v_excluded_rel TEXT;
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
            -- Check for exclusions
            SELECT em.excluded_relation INTO v_excluded_rel
            FROM melange_model em
            WHERE em.object_type = p_object_type
              AND em.relation = p_relation
              AND em.excluded_relation IS NOT NULL
            LIMIT 1;

            IF v_excluded_rel IS NOT NULL THEN
                IF check_exclusion(p_subject_type, p_subject_id, v_excluded_rel, p_object_type, p_object_id) THEN
                    RETURN 0;  -- Excluded
                END IF;
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
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = c.object_type
            AND m.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND m.subject_relation IS NOT NULL
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
        -- Check for exclusions
        SELECT em.excluded_relation INTO v_excluded_rel
        FROM melange_model em
        WHERE em.object_type = p_object_type
          AND em.relation = p_relation
          AND em.excluded_relation IS NOT NULL
        LIMIT 1;

        IF v_excluded_rel IS NOT NULL THEN
            -- Check if subject is excluded
            IF check_exclusion(p_subject_type, p_subject_id, v_excluded_rel, p_object_type, p_object_id) THEN
                RETURN 0;  -- Excluded
            END IF;
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


-- List objects a subject can access (simple path - no userset references)
-- Uses a recursive CTE to walk the permission graph in a single query.
--
-- This is a CTE-only implementation that avoids per-row check_permission calls
-- by building the complete access set recursively:
--
-- 1. base_access: All relations the subject satisfies via direct tuples (via closure)
-- 2. inherited_access: Recursively inherit child relations from parent objects
-- 3. candidate_objects: Filter to objects matching p_object_type and p_relation (via closure)
-- 4. Apply exclusions at the final result level
--
-- Key insight: Build a complete set of (object_type, object_id, relation) tuples
-- the subject satisfies, then filter by closure to the target relation.
CREATE OR REPLACE FUNCTION list_accessible_objects_simple(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
DECLARE
    v_has_intersection BOOLEAN;
BEGIN
    -- Check once if intersection rules exist (optimization)
    SELECT EXISTS (
        SELECT 1 FROM melange_model m
        WHERE m.object_type = p_object_type
          AND m.relation = p_relation
          AND m.rule_group_mode = 'intersection'
    ) INTO v_has_intersection;

    RETURN QUERY
    WITH RECURSIVE
    -- Base: all relations the subject satisfies via direct tuples (closure)
    -- For each tuple where subject matches, expand to all relations it satisfies
    base_access AS (
        SELECT
            t.object_type AS obj_type,
            t.object_id AS obj_id,
            c.relation AS rel
        FROM melange_tuples t
        JOIN melange_relation_closure c
            ON c.object_type = t.object_type
            AND c.satisfying_relation = t.relation
        WHERE t.subject_type = p_subject_type
          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    ),
    -- Recursive: inherit child relations from parent relations
    -- For "X from parent" patterns, if subject has parent_relation on parent object,
    -- then subject has relation X on all child objects linked to that parent.
    inherited_access AS (
        SELECT obj_type, obj_id, rel, 0 AS depth FROM base_access

        UNION

        SELECT
            child.object_type AS obj_type,
            child.object_id AS obj_id,
            c.relation AS rel,
            ia.depth + 1
        FROM inherited_access ia
        -- Find parent inheritance rules where:
        -- - The rule is for objects linked to our accessible object type
        -- - The rule requires our accessible relation on the parent
        JOIN melange_model m
            ON m.parent_relation IS NOT NULL
            AND m.parent_relation = ia.rel
        -- Find child objects linked to the accessible parent
        JOIN melange_tuples child
            ON child.subject_type = ia.obj_type
            AND child.subject_id = ia.obj_id
            AND child.relation = m.subject_type  -- linking relation
            AND child.object_type = m.object_type
        -- Expand to all relations this satisfies via closure
        JOIN melange_relation_closure c
            ON c.object_type = child.object_type
            AND c.satisfying_relation = m.relation
        WHERE ia.depth < 10
    ),
    -- Candidate objects that satisfy p_relation (via closure)
    candidate_objects AS (
        SELECT DISTINCT ia.obj_id
        FROM inherited_access ia
        WHERE ia.obj_type = p_object_type
          AND ia.rel = p_relation
    )
    SELECT co.obj_id AS object_id
    FROM candidate_objects co
    WHERE NOT EXISTS (
        SELECT 1
        FROM melange_model em
        WHERE em.object_type = p_object_type
          AND em.relation = p_relation
          AND em.excluded_relation IS NOT NULL
          AND check_exclusion(
              p_subject_type,
              p_subject_id,
              em.excluded_relation,
              p_object_type,
              co.obj_id
          )
    )
    -- Intersection check: filter out candidates that don't satisfy all AND conditions
    AND (
        NOT v_has_intersection
        OR check_intersection_groups(
            p_subject_type,
            p_subject_id,
            p_relation,
            p_object_type,
            co.obj_id
        )
    );
END;
$$ LANGUAGE plpgsql STABLE;


-- List objects a subject can access (with userset reference support)
-- This is the main entry point. It detects whether userset references exist
-- for the target relation and dispatches to either:
-- 1. Fast path (list_accessible_objects_simple) for relations without usersets
-- 2. Full userset-aware enumeration using subject_grants for relations with usersets
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
DECLARE
    v_has_userset BOOLEAN;
BEGIN
    -- Fast path: check if any userset references exist for this relation
    SELECT EXISTS (
        SELECT 1
        FROM melange_relation_closure c
        JOIN melange_model m
            ON m.object_type = c.object_type
            AND m.relation = c.satisfying_relation
        WHERE c.object_type = p_object_type
          AND c.relation = p_relation
          AND m.subject_relation IS NOT NULL
    ) INTO v_has_userset;

    IF NOT v_has_userset THEN
        -- No userset references, use fast path
        RETURN QUERY SELECT lo.object_id
        FROM list_accessible_objects_simple(
            p_subject_type, p_subject_id, p_relation, p_object_type
        ) lo;
        RETURN;
    END IF;

    -- Full userset-aware enumeration using subject_grants
    -- Filter the results to the target object type and relation, applying exclusions
    RETURN QUERY
    SELECT DISTINCT sg.object_id
    FROM subject_grants(p_subject_type, p_subject_id) sg
    WHERE sg.object_type = p_object_type
      AND sg.relation = p_relation
      -- Apply exclusions
      AND NOT EXISTS (
        SELECT 1
        FROM melange_model em
        WHERE em.object_type = p_object_type
          AND em.relation = p_relation
          AND em.excluded_relation IS NOT NULL
          AND check_exclusion(
              p_subject_type,
              p_subject_id,
              em.excluded_relation,
              p_object_type,
              sg.object_id
          )
      )
      -- Intersection check: filter out objects that don't satisfy all AND conditions
      AND (
          NOT EXISTS (
              SELECT 1 FROM melange_model m
              WHERE m.object_type = p_object_type
                AND m.relation = p_relation
                AND m.rule_group_mode = 'intersection'
          )
          OR check_intersection_groups(
              p_subject_type,
              p_subject_id,
              p_relation,
              p_object_type,
              sg.object_id
          )
      );
END;
$$ LANGUAGE plpgsql STABLE;


-- List subjects that have access to an object
-- Uses a recursive CTE to find all permission sources, then collects subjects.
--
-- The CTE:
-- 1. Starts with the target object
-- 2. Recursively finds parent objects that grant permission
-- 3. Collects all subjects with satisfying relations on any permission source
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
BEGIN
    RETURN QUERY
    WITH RECURSIVE
    -- Find all parent objects that grant permission to the target object
    permission_sources AS (
        -- The target object itself
        SELECT
            p_object_type AS source_type,
            p_object_id AS source_id,
            p_relation AS required_relation,
            0 AS depth

        UNION

        -- Parents that grant permission via inheritance.
        -- IMPORTANT: We must check parent inheritance on ALL relations that satisfy
        -- the required_relation via the closure table, not just required_relation itself.
        -- This matches the fix in check_permission and list_accessible_objects.
        SELECT
            t.subject_type,
            t.subject_id,
            m.parent_relation,
            ps.depth + 1
        FROM permission_sources ps
        JOIN melange_relation_closure c
            ON c.object_type = ps.source_type
            AND c.relation = ps.required_relation
        JOIN melange_tuples t
            ON t.object_type = ps.source_type
            AND t.object_id = ps.source_id
        JOIN melange_model m
            ON m.object_type = ps.source_type
            AND m.relation = c.satisfying_relation
            AND m.parent_relation IS NOT NULL
            AND m.subject_type = t.relation  -- linking relation
        WHERE ps.depth < 10
    )
    -- Find all subjects with satisfying relations on any permission source
    SELECT DISTINCT t.subject_id
    FROM permission_sources ps
    JOIN melange_relation_closure c
        ON c.object_type = ps.source_type
        AND c.relation = ps.required_relation
    JOIN melange_tuples t
        ON t.object_type = ps.source_type
        AND t.object_id = ps.source_id
        AND t.relation = c.satisfying_relation
    WHERE t.subject_type = p_subject_type
      AND t.subject_id != '*'  -- Exclude wildcard from results
      -- Exclusion check: ALWAYS check against the ORIGINAL object (p_object_type/p_object_id),
      -- not the permission source. Exclusions are defined on p_relation which is on the
      -- original object. For example, if checking can_review_strict on pull_request,
      -- the exclusion (banned) must be checked on the pull_request, even when the
      -- permission comes from a parent repo.
      AND NOT EXISTS (
          SELECT 1
          FROM melange_model em
          WHERE em.object_type = p_object_type
            AND em.relation = p_relation
            AND em.excluded_relation IS NOT NULL
            AND check_exclusion(
                p_subject_type,
                t.subject_id,
                em.excluded_relation,
                p_object_type,
                p_object_id
            )
      )
      -- Intersection check: filter out subjects that don't satisfy all AND conditions
      AND (
          NOT EXISTS (
              SELECT 1 FROM melange_model m
              WHERE m.object_type = p_object_type
                AND m.relation = p_relation
                AND m.rule_group_mode = 'intersection'
          )
          OR check_intersection_groups(
              p_subject_type,
              t.subject_id,
              p_relation,
              p_object_type,
              p_object_id
          )
      );
END;
$$ LANGUAGE plpgsql STABLE;
