-- Melange relation closure table
-- Stores the precomputed transitive closure of implied-by relations.
--
-- This optimization eliminates recursive function calls for role hierarchy
-- resolution. Instead of traversing the implied-by graph at runtime,
-- check_permission can use a single JOIN against this table.
--
-- Each row means: having 'satisfying_relation' satisfies 'relation' for 'object_type'.
--
-- Example for schema: owner -> admin -> member
--   | object_type | relation | satisfying_relation |
--   |-------------|----------|---------------------|
--   | repository  | owner    | owner               |
--   | repository  | admin    | admin               |
--   | repository  | admin    | owner               |
--   | repository  | member   | member              |
--   | repository  | member   | admin               |
--   | repository  | member   | owner               |
--
-- This file is idempotent and applied by `melange migrate`.

CREATE TABLE IF NOT EXISTS melange_relation_closure (
    id BIGSERIAL PRIMARY KEY,
    object_type VARCHAR NOT NULL,
    relation VARCHAR NOT NULL,
    satisfying_relation VARCHAR NOT NULL,
    via_path VARCHAR[],  -- debugging: path from relation to satisfying_relation

    UNIQUE (object_type, relation, satisfying_relation)
);

-- Primary lookup: find all relations that satisfy a target relation
-- Used by check_permission: JOIN ... ON c.object_type = ? AND c.relation = ?
CREATE INDEX IF NOT EXISTS idx_melange_closure_lookup
    ON melange_relation_closure (object_type, relation);

-- Reverse lookup: find which relations a given relation satisfies
-- Useful for understanding permission inheritance
CREATE INDEX IF NOT EXISTS idx_melange_closure_reverse
    ON melange_relation_closure (object_type, satisfying_relation);
