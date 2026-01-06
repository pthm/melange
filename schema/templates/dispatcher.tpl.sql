{{- /*
  Template for generating the check_permission dispatcher function.
  Routes to specialized functions for all known type/relation pairs.
  Unknown type/relation pairs return 0 (deny by default).

  Phase 5: All relations now have specialized functions - no generic fallback needed.
*/ -}}
{{- if .HasSpecializedFunctions -}}
-- Generated internal dispatcher for {{.FunctionName}}_internal
-- Routes to specialized functions with p_visited for cycle detection in TTU patterns
-- Enforces depth limit of 25 to prevent stack overflow from deep permission chains
-- Phase 5: All relations use specialized functions - no generic fallback
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
DECLARE
    v_userset_check INTEGER := 0;
BEGIN
    -- Depth limit check: prevent excessively deep permission resolution chains
    -- This catches both recursive TTU patterns and long userset chains
    IF array_length(p_visited, 1) >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    -- Userset subject handling: when subject is a userset like "group:fga#member"
    IF position('#' in p_subject_id) > 0 THEN
        -- Case 1: Self-referential userset check
        -- "document:1#writer" checking "viewer" on "document:1"
        -- Check if the subject's relation satisfies the requested relation via closure
        -- Note: This is a structural validity check, not a permission grant.
        -- Exclusions don't apply here because we're confirming the userset is valid,
        -- not granting access. Exclusions on usersets apply when the userset is used
        -- as a subject in other permission checks (e.g., checking if group:1#member
        -- has write access on some document).
        IF p_subject_type = p_object_type AND
           substring(p_subject_id from 1 for position('#' in p_subject_id) - 1) = p_object_id THEN
            SELECT 1 INTO v_userset_check
            FROM melange_relation_closure c
            WHERE c.object_type = p_object_type
              AND c.relation = p_relation
              AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
            LIMIT 1;
            IF v_userset_check = 1 THEN
                RETURN 1;
            END IF;
        END IF;

        -- Case 2: Computed userset matching
        -- Subject is "group:fga#member", tuple is "(folder:1, viewer, group:fga#member_c4)"
        -- Check if the subject's relation satisfies the tuple's userset relation via closure
        -- Also validate against model constraints (subject_relation must allow userset subjects)
        SELECT 1 INTO v_userset_check
        FROM melange_tuples t
        JOIN melange_relation_closure c
            ON c.object_type = p_object_type
            AND c.relation = p_relation
            AND c.satisfying_relation = t.relation
        -- Validate model allows userset subjects for this relation
        JOIN melange_model m
            ON m.object_type = p_object_type
            AND m.relation = c.satisfying_relation
            AND m.subject_type = t.subject_type
            AND m.subject_relation IS NOT NULL
            AND m.parent_relation IS NULL
        -- Check if the requested userset relation implies the tuple's userset relation
        -- via the subject type's closure table
        -- e.g., closure(group, member_c4, member) means "member satisfies member_c4"
        JOIN melange_relation_closure subj_c
            ON subj_c.object_type = t.subject_type
            AND subj_c.relation = substring(t.subject_id from position('#' in t.subject_id) + 1)
            AND subj_c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
        WHERE t.object_type = p_object_type
          AND t.object_id = p_object_id
          AND t.subject_type = p_subject_type
          AND t.subject_id != '*'
          AND position('#' in t.subject_id) > 0
          -- Match the ID part (before #) of the subject
          AND substring(t.subject_id from 1 for position('#' in t.subject_id) - 1) =
              substring(p_subject_id from 1 for position('#' in p_subject_id) - 1)
        LIMIT 1;
        IF v_userset_check = 1 THEN
            -- Check exclusions before granting access
            IF NOT check_all_exclusions_with_visited(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited) THEN
                RETURN 1;
            END IF;
        END IF;
    END IF;

    RETURN (SELECT CASE
{{- range .Cases}}
        WHEN p_object_type = '{{.ObjectType}}' AND p_relation = '{{.Relation}}' THEN {{.CheckFunctionName}}(p_subject_type, p_subject_id, p_object_id, p_visited)
{{- end}}
        -- Unknown type/relation: deny by default (no generic fallback)
        ELSE 0
    END);
END;
$$ LANGUAGE plpgsql STABLE;

-- Generated dispatcher for {{.FunctionName}}
-- Routes to specialized functions for all known type/relation pairs
CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT {{.FunctionName}}_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, ARRAY[]::TEXT[]);
$$ LANGUAGE sql STABLE;
{{- else -}}
{{- if .IsNoWildcard }}
-- Generated dispatcher for {{.FunctionName}} (no specialized functions)
-- Falls back to generic implementation for no-wildcard variant
-- The no-wildcard dispatcher uses generic because specialized functions include wildcard matching
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
    SELECT {{.GenericFunctionName}}_internal(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id, p_visited);
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT {{.GenericFunctionName}}(p_subject_type, p_subject_id, p_relation, p_object_type, p_object_id);
$$ LANGUAGE sql STABLE;
{{- else }}
-- Generated dispatcher for {{.FunctionName}} (no relations defined)
-- Phase 5: Returns 0 (deny) for all requests - no generic fallback
CREATE OR REPLACE FUNCTION {{.FunctionName}}_internal (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT,
p_visited TEXT[] DEFAULT ARRAY[]::TEXT[]
) RETURNS INTEGER AS $$
    SELECT 0;
$$ LANGUAGE sql STABLE;

CREATE OR REPLACE FUNCTION {{.FunctionName}} (
p_subject_type TEXT,
p_subject_id TEXT,
p_relation TEXT,
p_object_type TEXT,
p_object_id TEXT
) RETURNS INTEGER AS $$
    SELECT 0;
$$ LANGUAGE sql STABLE;
{{- end -}}
{{- end -}}
