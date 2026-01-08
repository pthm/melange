{{- /*
  Template for list_objects function with intersection patterns.

  This is a comprehensive template that handles intersection patterns combined with
  all other features (direct, implied, userset, TTU, exclusion).

  Intersection strategy for list:
  - Each intersection group produces a set of objects via INTERSECT
  - Multiple groups are UNION'd together
  - Standalone access paths (direct/implied/userset/TTU outside intersections) are UNION'd

  For example, "viewer: writer or (editor and owner)" generates:
  1. UNION: objects where subject has writer (standalone implied)
  2. UNION: (objects with editor) INTERSECT (objects with owner)
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
{{- if or .ParentRelations .SelfReferentialLinkingRelations }}
DECLARE
    v_max_depth INTEGER;
BEGIN
{{- if .SelfReferentialLinkingRelations }}
    -- Check for excessive recursion depth before running the query
    WITH RECURSIVE depth_check(object_id, depth) AS (
        SELECT NULL::TEXT, 0
        WHERE FALSE
        UNION ALL
        SELECT t.object_id, d.depth + 1
        FROM depth_check d
        JOIN melange_tuples t
          ON t.object_type = '{{.ObjectType}}'
          AND t.relation IN ({{.SelfReferentialLinkingRelations}})
          AND t.subject_type = '{{.ObjectType}}'
        WHERE d.depth < 26
    )
    SELECT MAX(depth) INTO v_max_depth FROM depth_check;
{{- else }}
    v_max_depth := 0;
{{- end }}

    IF v_max_depth >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    RETURN QUERY
{{- else }}
BEGIN
    RETURN QUERY
{{- end }}
{{- if .HasStandaloneAccess }}
    -- =====================================================================
    -- STANDALONE ACCESS PATHS (outside of intersection groups)
    -- =====================================================================
{{- if .RelationList }}

    -- Direct/Implied standalone access via closure relations
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{.ObjectType}}'
      AND t.relation IN ({{.RelationList}})
      AND t.subject_type = p_subject_type
      AND p_subject_type IN ({{.AllowedSubjectTypes}})
      AND {{.SubjectIDCheck}}
{{- if .SimpleExcludedRelations }}
{{- range .SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{$.ObjectType}}'
            AND excl.object_id = t.object_id
            AND excl.relation = '{{.}}'
            AND excl.subject_type = p_subject_type
            AND (excl.subject_id = p_subject_id OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if .ComplexExcludedRelations }}
{{- range .ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- range .ComplexClosureRelations }}

    UNION
    -- Complex closure relation: {{.}}
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation = '{{.}}'
      AND t.subject_type = p_subject_type
      AND p_subject_type IN ({{$.AllowedSubjectTypes}})
      AND (t.subject_id = p_subject_id OR t.subject_id = '*')
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- range .IntersectionClosureRelations }}

    UNION
    -- Compose with intersection closure relation: {{.}}
    SELECT * FROM list_{{$.ObjectType}}_{{.}}_objects(p_subject_type, p_subject_id)
{{- end }}
{{- range .UsersetPatterns }}

    UNION
    -- Userset path: Via {{.SubjectType}}#{{.SubjectRelation}} membership
{{- if .IsComplex }}
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation IN ({{.SourceRelationList}})
      AND t.subject_type = '{{.SubjectType}}'
      AND position('#' in t.subject_id) > 0
      AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SubjectRelation}}', '{{.SubjectType}}', split_part(t.subject_id, '#', 1), ARRAY[]::TEXT[]) = 1
{{- if .IsClosurePattern }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- else }}
    SELECT DISTINCT t.object_id
    FROM melange_tuples t
    JOIN melange_tuples m
      ON m.object_type = '{{.SubjectType}}'
      AND m.object_id = split_part(t.subject_id, '#', 1)
      AND m.relation IN ({{.SatisfyingRelationsList}})
      AND m.subject_type = p_subject_type
      AND p_subject_type IN ({{$.AllowedSubjectTypes}})
{{- if .HasWildcard }}
      AND (m.subject_id = p_subject_id OR m.subject_id = '*')
{{- else }}
      AND m.subject_id = p_subject_id
{{- end }}
    WHERE t.object_type = '{{$.ObjectType}}'
      AND t.relation IN ({{.SourceRelationList}})
      AND t.subject_type = '{{.SubjectType}}'
      AND position('#' in t.subject_id) > 0
      AND split_part(t.subject_id, '#', 2) = '{{.SubjectRelation}}'
{{- if .IsClosurePattern }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.SourceRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
{{- if $.SimpleExcludedRelations }}
{{- range $.SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{$.ObjectType}}'
            AND excl.object_id = t.object_id
            AND excl.relation = '{{.}}'
            AND excl.subject_type = p_subject_type
            AND (excl.subject_id = p_subject_id OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- range .ParentRelations }}
{{- if .HasCrossTypeLinks }}

    UNION
    -- Cross-type TTU: {{.LinkingRelation}} -> {{.Relation}}
    SELECT DISTINCT child.object_id
    FROM melange_tuples child
    WHERE child.object_type = '{{$.ObjectType}}'
      AND child.relation = '{{.LinkingRelation}}'
      AND child.subject_type IN ({{.CrossTypeLinkingTypes}})
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.Relation}}', child.subject_type, child.subject_id, ARRAY[]::TEXT[]) = 1
{{- if $.SimpleExcludedRelations }}
{{- range $.SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{$.ObjectType}}'
            AND excl.object_id = child.object_id
            AND excl.relation = '{{.}}'
            AND excl.subject_type = p_subject_type
            AND (excl.subject_id = p_subject_id OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', child.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

    -- =====================================================================
    -- INTERSECTION GROUPS
    -- Each group is an INTERSECT of its parts, groups are UNION'd
    -- =====================================================================
{{- range $groupIdx, $group := .IntersectionGroups }}
{{- if $.HasStandaloneAccess }}

    UNION
{{- else if $groupIdx }}

    UNION
{{- end }}
    -- Intersection group {{$groupIdx}}
    SELECT ig_{{$groupIdx}}.object_id FROM (
{{- range $partIdx, $part := $group.Parts }}
{{- if $partIdx }}
        INTERSECT
{{- end }}
{{- if $part.IsThis }}
        -- Part {{$partIdx}}: This (direct tuple for relation)
        SELECT DISTINCT t.object_id
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.relation = '{{$.Relation}}'
          AND t.subject_type = p_subject_type
          AND p_subject_type IN ({{$.AllowedSubjectTypes}})
          AND {{if $part.HasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id AND t.subject_id != '*'{{end}}
{{- if $part.ExcludedRelation }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- else if $part.ParentRelation }}
        -- Part {{$partIdx}}: TTU {{$part.ParentRelation.LinkingRelation}} -> {{$part.ParentRelation.Relation}}
        SELECT DISTINCT child.object_id
        FROM melange_tuples child
        WHERE child.object_type = '{{$.ObjectType}}'
          AND child.relation = '{{$part.ParentRelation.LinkingRelation}}'
          AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', child.subject_type, child.subject_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', child.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- else }}
        -- Part {{$partIdx}}: Relation {{$part.Relation}}
        SELECT DISTINCT t.object_id
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
    ) AS ig_{{$groupIdx}}
{{- /* Apply relation-level exclusions to the intersection result */ -}}
{{- if $.SimpleExcludedRelations }}
    WHERE TRUE
{{- range $.SimpleExcludedRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples excl
          WHERE excl.object_type = '{{$.ObjectType}}'
            AND excl.object_id = ig_{{$groupIdx}}.object_id
            AND excl.relation = '{{.}}'
            AND excl.subject_type = p_subject_type
            AND (excl.subject_id = p_subject_id OR excl.subject_id = '*')
      )
{{- end }}
{{- end }}
{{- if $.ComplexExcludedRelations }}
{{- if not $.SimpleExcludedRelations }}
    WHERE TRUE
{{- end }}
{{- range $.ComplexExcludedRelations }}
      AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', ig_{{$groupIdx}}.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- if .SelfReferentialLinkingRelations }}

    UNION ALL
    -- Self-referential TTU: recursive expansion from accessible parents
    -- Note: WITH RECURSIVE must be wrapped in a subquery when used after UNION
    SELECT rec.object_id FROM (
        WITH RECURSIVE accessible_rec(object_id, depth) AS (
        -- Seed: all objects from above queries
        SELECT DISTINCT seed.object_id, 0
        FROM (
{{- if .HasStandaloneAccess }}
{{- if .RelationList }}
            SELECT t.object_id FROM melange_tuples t
            WHERE t.object_type = '{{.ObjectType}}'
              AND t.relation IN ({{.RelationList}})
              AND t.subject_type = p_subject_type
              AND p_subject_type IN ({{.AllowedSubjectTypes}})
              AND {{.SubjectIDCheck}}
{{- end }}
{{- range $groupIdx, $group := .IntersectionGroups }}
{{- if or $.RelationList $groupIdx }}
            UNION
{{- end }}
            SELECT object_id FROM (
{{- range $partIdx, $part := $group.Parts }}
{{- if $partIdx }}
                INTERSECT
{{- end }}
{{- if $part.IsThis }}
                SELECT t.object_id FROM melange_tuples t
                WHERE t.object_type = '{{$.ObjectType}}' AND t.relation = '{{$.Relation}}'
                  AND t.subject_type = p_subject_type AND {{if $part.HasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id{{end}}
{{- else if $part.ParentRelation }}
                SELECT child.object_id FROM melange_tuples child
                WHERE child.object_type = '{{$.ObjectType}}' AND child.relation = '{{$part.ParentRelation.LinkingRelation}}'
                  AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', child.subject_type, child.subject_id, ARRAY[]::TEXT[]) = 1
{{- else }}
                SELECT t.object_id FROM melange_tuples t
                WHERE t.object_type = '{{$.ObjectType}}'
                  AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
            ) AS ig_{{$groupIdx}}
{{- end }}
{{- else }}
{{- range $groupIdx, $group := .IntersectionGroups }}
{{- if $groupIdx }}
            UNION
{{- end }}
            SELECT object_id FROM (
{{- range $partIdx, $part := $group.Parts }}
{{- if $partIdx }}
                INTERSECT
{{- end }}
{{- if $part.IsThis }}
                SELECT t.object_id FROM melange_tuples t
                WHERE t.object_type = '{{$.ObjectType}}' AND t.relation = '{{$.Relation}}'
                  AND t.subject_type = p_subject_type AND {{if $part.HasWildcard}}(t.subject_id = p_subject_id OR t.subject_id = '*'){{else}}t.subject_id = p_subject_id{{end}}
{{- else if $part.ParentRelation }}
                SELECT child.object_id FROM melange_tuples child
                WHERE child.object_type = '{{$.ObjectType}}' AND child.relation = '{{$part.ParentRelation.LinkingRelation}}'
                  AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', child.subject_type, child.subject_id, ARRAY[]::TEXT[]) = 1
{{- else }}
                SELECT t.object_id FROM melange_tuples t
                WHERE t.object_type = '{{$.ObjectType}}'
                  AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- end }}
{{- end }}
            ) AS ig_{{$groupIdx}}
{{- end }}
{{- end }}
        ) AS seed

        UNION ALL

        SELECT DISTINCT child.object_id, a.depth + 1
        FROM accessible_rec a
        JOIN melange_tuples child
          ON child.object_type = '{{.ObjectType}}'
          AND child.relation IN ({{.SelfReferentialLinkingRelations}})
          AND child.subject_type = '{{.ObjectType}}'
          AND child.subject_id = a.object_id
        WHERE a.depth < 25
        )
        SELECT DISTINCT object_id FROM accessible_rec
    ) AS rec
{{- end }}

    UNION

    -- Self-candidate: when subject is a userset on the same object type
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          SELECT 1 FROM melange_relation_closure c
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
END;
$$ LANGUAGE plpgsql STABLE;
