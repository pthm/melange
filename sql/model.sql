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
    excluded_relation VARCHAR,
    -- New columns for userset references and intersection support
    subject_relation VARCHAR,      -- For userset references [type#relation]: stores the relation part
    rule_group_id BIGINT,          -- Groups rules that form an intersection
    rule_group_mode VARCHAR,       -- 'intersection' for AND semantics, 'union' or NULL for OR
    check_relation VARCHAR,        -- For intersection rules: which relation to check
    check_excluded_relation VARCHAR, -- For intersection rules: exclusion on the check_relation (e.g., "editor but not owner")
    CONSTRAINT chk_rule_group_mode CHECK (rule_group_mode IS NULL OR rule_group_mode IN ('union', 'intersection'))
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

-- Userset reference lookup: find rules with userset references
-- Used for resolving [type#relation] patterns in check_permission
CREATE INDEX IF NOT EXISTS idx_melange_model_userset ON melange_model (object_type, relation, subject_type, subject_relation)
    WHERE subject_relation IS NOT NULL;

-- Intersection group lookup: find intersection rules
-- Used to detect if a relation has intersection rules (for fast path optimization)
CREATE INDEX IF NOT EXISTS idx_melange_model_intersection ON melange_model (object_type, relation)
    WHERE rule_group_mode = 'intersection';

-- Userset rule expansion table
-- Stores precomputed userset rules with relation closure applied.
-- Each row indicates that a tuple with tuple_relation can satisfy relation
-- for object_type when the tuple subject is subject_type#subject_relation.
CREATE TABLE IF NOT EXISTS melange_userset_rules (
    id BIGSERIAL PRIMARY KEY,
    object_type VARCHAR NOT NULL,
    relation VARCHAR NOT NULL,
    tuple_relation VARCHAR NOT NULL,
    subject_type VARCHAR NOT NULL,
    subject_relation VARCHAR NOT NULL,
    UNIQUE (object_type, relation, tuple_relation, subject_type, subject_relation)
);

-- Primary lookup: find userset rules for a specific object type and relation
CREATE INDEX IF NOT EXISTS idx_melange_userset_rules_lookup
    ON melange_userset_rules (object_type, relation);

-- Tuple lookup: match tuples by relation and subject type
CREATE INDEX IF NOT EXISTS idx_melange_userset_rules_tuple
    ON melange_userset_rules (object_type, tuple_relation, subject_type);

-- Subject lookup: match group relation requirements
CREATE INDEX IF NOT EXISTS idx_melange_userset_rules_subject
    ON melange_userset_rules (subject_type, subject_relation);
