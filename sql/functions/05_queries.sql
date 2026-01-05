-- Low-level tuple existence check with LIKE pattern support.
-- Useful for checking specific tuple patterns without permission evaluation.
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


-- Enumerate objects a subject can access (ListObjects API).
-- Strategy: find all objects of target type, then filter via check_permission.
-- Ensures consistency with Check at the cost of performance.
CREATE OR REPLACE FUNCTION list_accessible_objects(
    p_subject_type TEXT,
    p_subject_id TEXT,
    p_relation TEXT,
    p_object_type TEXT
) RETURNS TABLE (object_id TEXT) AS $$
BEGIN
    RETURN QUERY
    WITH candidate_objects AS (
        SELECT DISTINCT t.object_id
        FROM melange_tuples t
        WHERE t.object_type = p_object_type
        UNION
        SELECT split_part(p_subject_id, '#', 1) AS object_id
        WHERE position('#' in p_subject_id) > 0
          AND p_subject_type = p_object_type
    )
    SELECT DISTINCT c.object_id
    FROM candidate_objects c
    WHERE check_permission(
        p_subject_type,
        p_subject_id,
        p_relation,
        p_object_type,
        c.object_id
    ) = 1;
END;
$$ LANGUAGE plpgsql STABLE;


-- Enumerate subjects with access to an object (ListUsers API).
-- Supports userset filters like "group#member" to find userset references.
-- Strategy: gather candidate subjects from all relevant paths, then filter via check_permission.
CREATE OR REPLACE FUNCTION list_accessible_subjects(
    p_object_type TEXT,
    p_object_id TEXT,
    p_relation TEXT,
    p_subject_type TEXT
) RETURNS TABLE (subject_id TEXT) AS $$
DECLARE
    v_filter_type TEXT;     -- Parsed type part (e.g., 'group' from 'group#member')
    v_filter_relation TEXT; -- Parsed relation part (e.g., 'member' from 'group#member')
BEGIN
    -- Parse userset filter if present
    IF position('#' in p_subject_type) > 0 THEN
        v_filter_type := substring(p_subject_type from 1 for position('#' in p_subject_type) - 1);
        v_filter_relation := substring(p_subject_type from position('#' in p_subject_type) + 1);
    ELSE
        v_filter_type := p_subject_type;
        v_filter_relation := NULL;
    END IF;

    -- Userset filter: find all "type#relation" subjects that have access
    IF v_filter_relation IS NOT NULL THEN
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
        userset_rule_tuples AS (
            SELECT DISTINCT object_type, relation, tuple_relation, subject_type, subject_relation, subject_relation_satisfying
            FROM melange_userset_rules
        ),
        userset_nodes AS (
            SELECT p_object_type AS object_type, p_object_id AS object_id, s.relation AS relation, 0 AS depth
            FROM seed_relations s
            UNION
            SELECT t.subject_type AS object_type, split.id AS object_id, split.rel AS relation, n.depth + 1 AS depth
            FROM userset_nodes n
            JOIN userset_rule_tuples ur
                ON ur.object_type = n.object_type
                AND ur.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = ur.tuple_relation
                AND t.subject_type = ur.subject_type
            CROSS JOIN LATERAL (
                SELECT
                    substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) AS id,
                    substring(t.subject_id from position('#' in t.subject_id) + 1) AS rel
            ) AS split
            WHERE n.depth < 25
              AND position('#' in t.subject_id) > 0
              AND split.rel = ur.subject_relation_satisfying
        ),
        userset_candidates AS (
            SELECT DISTINCT
                substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM userset_nodes n
            JOIN userset_rule_tuples ur
                ON ur.object_type = n.object_type
                AND ur.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = ur.tuple_relation
                AND t.subject_type = ur.subject_type
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
        ),
        subject_pool AS (
            SELECT DISTINCT
                substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_tuples t
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
        ),
        type_object_candidates AS (
            SELECT DISTINCT object_id
            FROM melange_tuples
            WHERE object_type = v_filter_type
            UNION
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.subject_type = v_filter_type
              AND position('#' in t.subject_id) = 0
              AND t.subject_id != '*'
        ),
        direct_candidates AS (
            SELECT DISTINCT
                substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) || '#' || v_filter_relation AS subject_id
            FROM melange_relation_closure c
            JOIN melange_tuples t
                ON t.object_type = p_object_type
                AND t.object_id = p_object_id
                AND t.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND t.subject_type = v_filter_type
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
        ),
        parent_candidates AS (
            SELECT DISTINCT
                t.subject_id || '#' || v_filter_relation AS subject_id
            FROM melange_relation_closure c
            JOIN melange_model m
                ON m.object_type = c.object_type
                AND m.relation = c.satisfying_relation
                AND m.parent_relation IS NOT NULL
            JOIN melange_tuples t
                ON t.object_type = p_object_type
                AND t.object_id = p_object_id
                AND t.relation = m.subject_type
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND t.subject_type = v_filter_type
              AND (
                  m.parent_relation = v_filter_relation
                  OR EXISTS (
                      SELECT 1 FROM melange_relation_closure subj_c
                      WHERE subj_c.object_type = v_filter_type
                        AND subj_c.relation = m.parent_relation
                        AND subj_c.satisfying_relation = v_filter_relation
                  )
              )
        ),
        self_candidate AS (
            SELECT p_object_id || '#' || v_filter_relation AS subject_id
            WHERE v_filter_type = p_object_type
        ),
        all_candidates AS (
            SELECT sp.subject_id AS candidate_id FROM subject_pool sp
            UNION
            SELECT toc.object_id || '#' || v_filter_relation AS candidate_id FROM type_object_candidates toc
            UNION
            SELECT dc.subject_id AS candidate_id FROM direct_candidates dc
            UNION
            SELECT pc.subject_id AS candidate_id FROM parent_candidates pc
            UNION
            SELECT uc.subject_id AS candidate_id FROM userset_candidates uc
            UNION
            SELECT sc.subject_id AS candidate_id FROM self_candidate sc
        )
        SELECT DISTINCT c.candidate_id AS subject_id
        FROM all_candidates c
        WHERE check_permission(
            v_filter_type,
            c.candidate_id,
            p_relation,
            p_object_type,
            p_object_id
        ) = 1;
    ELSE
        -- Regular subject filter: enumerate direct subjects with access
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
        userset_rule_tuples AS (
            SELECT DISTINCT object_type, relation, tuple_relation, subject_type, subject_relation, subject_relation_satisfying
            FROM melange_userset_rules
        ),
        userset_nodes AS (
            SELECT p_object_type AS object_type, p_object_id AS object_id, s.relation AS relation, 0 AS depth
            FROM seed_relations s
            UNION
            SELECT t.subject_type AS object_type, split.id AS object_id, split.rel AS relation, n.depth + 1 AS depth
            FROM userset_nodes n
            JOIN userset_rule_tuples ur
                ON ur.object_type = n.object_type
                AND ur.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = ur.tuple_relation
                AND t.subject_type = ur.subject_type
            CROSS JOIN LATERAL (
                SELECT
                    substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) AS id,
                    substring(t.subject_id from position('#' in t.subject_id) + 1) AS rel
            ) AS split
            WHERE n.depth < 25
              AND position('#' in t.subject_id) > 0
              AND split.rel = ur.subject_relation_satisfying
        ),
        userset_candidates AS (
            SELECT DISTINCT t.subject_id
            FROM userset_nodes n
            JOIN userset_rule_tuples ur
                ON ur.object_type = n.object_type
                AND ur.relation = n.relation
            JOIN melange_tuples t
                ON t.object_type = n.object_type
                AND t.object_id = n.object_id
                AND t.relation = ur.tuple_relation
                AND t.subject_type = ur.subject_type
            WHERE t.subject_type = v_filter_type
              AND (position('#' in t.subject_id) = 0 OR t.subject_id = '*')
        ),
        subject_pool AS (
            SELECT DISTINCT t.subject_id
            FROM melange_tuples t
            WHERE t.subject_type = v_filter_type
              AND (position('#' in t.subject_id) = 0 OR t.subject_id = '*')
        ),
        direct_candidates AS (
            SELECT DISTINCT t.subject_id
            FROM melange_relation_closure c
            JOIN melange_tuples t
                ON t.object_type = p_object_type
                AND t.object_id = p_object_id
                AND t.relation = c.satisfying_relation
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND t.subject_type = v_filter_type
              AND (position('#' in t.subject_id) = 0 OR t.subject_id = '*')
        ),
        all_candidates AS (
            SELECT sp.subject_id AS candidate_id FROM subject_pool sp
            UNION
            SELECT dc.subject_id AS candidate_id FROM direct_candidates dc
            UNION
            SELECT uc.subject_id AS candidate_id FROM userset_candidates uc
        ),
        filtered_candidates AS (
            SELECT DISTINCT c.candidate_id AS subject_id
            FROM all_candidates c
            WHERE check_permission(
                v_filter_type,
                c.candidate_id,
                p_relation,
                p_object_type,
                p_object_id
            ) = 1
        ),
        has_wildcard AS (
            SELECT EXISTS (
                SELECT 1 FROM filtered_candidates fc
                WHERE fc.subject_id = '*'
            ) AS has_wildcard
        )
        SELECT fc.subject_id
        FROM filtered_candidates fc
        CROSS JOIN has_wildcard hw
        WHERE (NOT hw.has_wildcard AND fc.subject_id != '*')
           OR (
               hw.has_wildcard
               AND (
                   fc.subject_id = '*'
                   OR (
                       fc.subject_id != '*'
                       AND check_permission_no_wildcard(
                           v_filter_type,
                           fc.subject_id,
                           p_relation,
                           p_object_type,
                           p_object_id
                       ) = 1
                   )
               )
           );
    END IF;
END;
$$ LANGUAGE plpgsql STABLE;
