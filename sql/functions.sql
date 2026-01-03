-- Melange infrastructure functions
-- These are generic permission checking functions that work with melange_model,
-- melange_relation_closure, and melange_tuples.
-- IMPORTANT: These functions are domain-agnostic. Domain-specific logic belongs in the Go layer.
--
-- This file is idempotent and applied by `melange migrate`.

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
        -- Look up if any implied relation has an exclusion
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
                -- Check if excluded relation is satisfied
                IF check_permission(p_subject_type, p_subject_id, v_excluded, p_object_type, p_object_id) = 1 THEN
                    -- Excluded, fall through to parent check
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
    FOR v_parent_type, v_parent_id, v_parent_rel, v_excluded_rel IN
        SELECT t.subject_type, t.subject_id, am.parent_relation, am.excluded_relation
        FROM melange_tuples t
        JOIN melange_model am
          ON am.object_type = p_object_type
         AND am.relation = p_relation
         AND am.parent_relation IS NOT NULL
         AND t.relation = am.subject_type  -- KEY: match linking relation, not parent type
        WHERE t.object_type = p_object_type
          AND t.object_id = p_object_id
    LOOP
        -- Check if the parent relation is satisfied (recursively uses closure for parent)
        IF check_permission(p_subject_type, p_subject_id, v_parent_rel, v_parent_type, v_parent_id) = 1 THEN
            -- Check for exclusion using recursive check_permission
            -- This handles both direct tuples and computed relations like is_collaborator
            IF v_excluded_rel IS NOT NULL THEN
                IF check_permission(p_subject_type, p_subject_id, v_excluded_rel, p_object_type, p_object_id) = 1 THEN
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


-- List objects a subject can access (reverse lookup)
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE(object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = p_object_type
      AND check_permission(p_subject_type, p_subject_id, p_relation, p_object_type, t.object_id) = 1;
END;
$$ LANGUAGE plpgsql STABLE;


-- List subjects that have access to an object (inverse of list_accessible_objects)
-- Answers "who has this permission on this object?"
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE(subject_id TEXT) AS $$
BEGIN
    RETURN QUERY
    SELECT DISTINCT t.subject_id
    FROM melange_tuples t
    WHERE t.subject_type = p_subject_type
      AND check_permission(p_subject_type, t.subject_id, p_relation, p_object_type, p_object_id) = 1;
END;
$$ LANGUAGE plpgsql STABLE;
