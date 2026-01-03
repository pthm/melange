-- Melange authorization model table
-- Stores parsed FGA schema definitions for permission checking
--
-- Each row represents one rule in the authorization model:
-- - Direct subject types: object X, relation Y can have subject of type Z
-- - Implied relations: object X, relation Y is implied by relation Z
-- - Parent inheritance: object X, relation Y inherits from parent relation Z
-- - Exclusions: object X, relation Y excludes subjects with relation Z
--
-- This file is idempotent and applied by `melange migrate`.

CREATE TABLE IF NOT EXISTS melange_model (
    id BIGSERIAL PRIMARY KEY,
    object_type VARCHAR NOT NULL,
    relation VARCHAR NOT NULL,
    subject_type VARCHAR,
    implied_by VARCHAR,
    parent_relation VARCHAR,
    excluded_relation VARCHAR
);

-- Primary lookup: find rules for a specific object type and relation
-- Used by check_permission when evaluating implied relations and parent inheritance
CREATE INDEX IF NOT EXISTS idx_melange_model_object_relation ON melange_model (object_type, relation);

-- Implied relations lookup: find relations that imply the target relation
-- Query: WHERE object_type = ? AND relation = ? AND implied_by IS NOT NULL AND parent_relation IS NULL
CREATE INDEX IF NOT EXISTS idx_melange_model_implied ON melange_model (object_type, relation)
    WHERE implied_by IS NOT NULL AND parent_relation IS NULL;

-- Composite index for the full implied-by lookup pattern including the implied_by column
-- Covers: SELECT implied_by, excluded_relation FROM melange_model WHERE object_type = ? AND relation = ? AND implied_by IS NOT NULL
CREATE INDEX IF NOT EXISTS idx_melange_model_implied_lookup ON melange_model (object_type, relation, implied_by)
    WHERE implied_by IS NOT NULL;

-- Parent relations lookup: find parent inheritance rules
-- Query: WHERE object_type = ? AND relation = ? AND parent_relation IS NOT NULL AND subject_type = ?
CREATE INDEX IF NOT EXISTS idx_melange_model_parent ON melange_model (object_type, relation, subject_type)
    WHERE parent_relation IS NOT NULL;

-- Composite index for the full parent lookup pattern including parent_relation
-- Covers: SELECT parent_relation, subject_type, excluded_relation FROM melange_model WHERE object_type = ? AND relation = ? AND parent_relation IS NOT NULL
CREATE INDEX IF NOT EXISTS idx_melange_model_parent_lookup ON melange_model (object_type, relation, parent_relation, subject_type)
    WHERE parent_relation IS NOT NULL;
