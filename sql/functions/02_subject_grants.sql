-- Enumerate all objects a subject can access.
--
-- Unlike subject_has_grant (which lazily checks one permission), this function
-- eagerly computes the complete set of accessible objects. Used by ListObjects
-- queries where we need the full result set, not just a yes/no answer.
--
-- Uses fixpoint iteration: start with direct tuples, then repeatedly expand
-- via usersets and parent inheritance until no new grants are discovered.
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
    v_max_iterations CONSTANT INTEGER := 25;  -- depth limit (matches OpenFGA default)
BEGIN
    -- Accumulator for discovered grants; temp table enables iterative expansion
    CREATE TEMP TABLE IF NOT EXISTS _sg_grants (
        object_type TEXT NOT NULL,
        object_id TEXT NOT NULL,
        relation TEXT NOT NULL,
        iteration INTEGER NOT NULL,  -- track when discovered for debugging
        PRIMARY KEY (object_type, object_id, relation)
    ) ON COMMIT DROP;

    TRUNCATE _sg_grants;

    -- Seed with tuples directly referencing this subject, expanding via closure
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
        AND (t.subject_id != '*' OR m.subject_wildcard = TRUE)
    JOIN melange_relation_closure c
        ON c.object_type = t.object_type
        AND c.satisfying_relation = t.relation
    WHERE t.subject_type = p_subject_type
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
    ON CONFLICT DO NOTHING;

    -- Handle computed usersets where tuple relation differs but is implied
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

    -- Expand iteratively until no new grants are discovered (fixpoint)
    LOOP
        v_iteration := v_iteration + 1;

        IF v_iteration > v_max_iterations THEN
            RAISE NOTICE 'subject_grants: depth limit (%) reached for subject %:%',
                         v_max_iterations, p_subject_type, p_subject_id;
            EXIT;
        END IF;

        -- Expand via userset references AND parent inheritance in one pass
        WITH userset_expansion AS (
            -- "I'm a member of group:x" + "group:x#member can view doc:1" => "I can view doc:1"
            SELECT DISTINCT
                t.object_type,
                t.object_id,
                ur.relation
            FROM _sg_grants g
            JOIN melange_userset_rules ur
                ON ur.subject_type = g.object_type             -- group type matches
                AND ur.subject_relation_satisfying = g.relation -- required relation matches
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
            -- "I have viewer on org:x" + "repo:1 inherits viewer from org" => "I can view repo:1"
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
