-- Melange infrastructure functions
-- These are generic permission checking functions that work with melange_model,
-- melange_relation_closure, and melange_tuples.
-- IMPORTANT: These functions are domain-agnostic. Domain-specific logic belongs in the Go layer.
--
-- This file is idempotent and applied by `melange migrate`.


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


-- Generic permission check (optimized with closure table)
-- Checks if a subject has a specific permission on an object by:
-- 1. Direct tuple match using closure table (handles direct + all implied relations in one query)
-- 2. Parent relation inheritance (e.g., org -> repo -> change)
-- 3. Exclusion check (for "but not" relations)
--
-- The closure table eliminates recursive implied-by traversal, reducing multiple
-- model queries to a single JOIN operation.
CREATE OR REPLACE FUNCTION check_permission(
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
        IF check_permission(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
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
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
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
      );
END;
$$ LANGUAGE plpgsql STABLE;
