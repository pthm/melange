{{- /*
  Template for list_objects function with recursive TTU patterns.

  This is a comprehensive template that handles all pattern combinations:
  - Direct/Implied: tuple lookup with closure-inlined relations
  - Userset: JOIN with membership tuples or check_permission_internal
  - TTU/Recursive: recursive CTE for parent traversal
  - Exclusion: NOT EXISTS anti-join or check_permission_internal

  For TTU patterns, the recursion direction is from parent to child:
  - Base case: objects where subject has access via direct/implied/userset paths
  - Recursive case: objects whose linking relation points to accessible parent objects

  Self-referential TTU (same object type) uses true recursive CTE.
  Cross-type TTU uses check_permission_internal on the parent object.

  Depth is limited to 25 with M2002 error on overflow.
*/ -}}
-- Generated list_objects function for {{.ObjectType}}.{{.Relation}}
-- Features: {{.FeaturesString}}
CREATE OR REPLACE FUNCTION {{.FunctionName}}(
    p_subject_type TEXT,
    p_subject_id TEXT
) RETURNS TABLE(object_id TEXT) AS $$
DECLARE
    v_max_depth INTEGER;
BEGIN
{{- if .SelfReferentialLinkingRelations }}
    -- Check for excessive recursion depth before running the query
    -- This matches check_permission behavior with M2002 error
    -- Only self-referential TTUs contribute to recursion depth (cross-type are one-hop)
    WITH RECURSIVE depth_check(object_id, depth) AS (
        -- Base case: seed with empty set (we just need depth tracking)
        SELECT NULL::TEXT, 0
        WHERE FALSE

        UNION ALL
        -- Track depth through all self-referential linking relations
        SELECT t.object_id, d.depth + 1
        FROM depth_check d
        JOIN melange_tuples t
          ON t.object_type = '{{.ObjectType}}'
          AND t.relation IN ({{.SelfReferentialLinkingRelations}})
          AND t.subject_type = '{{.ObjectType}}'
        WHERE d.depth < 26  -- Allow one extra to detect overflow
    )
    SELECT MAX(depth) INTO v_max_depth FROM depth_check;
{{- else }}
    -- No self-referential TTU patterns; skip depth check
    v_max_depth := 0;
{{- end }}

    IF v_max_depth >= 25 THEN
        RAISE EXCEPTION 'resolution too complex' USING ERRCODE = 'M2002';
    END IF;

    RETURN QUERY
    WITH RECURSIVE accessible(object_id, depth) AS (
        -- =====================================================================
        -- BASE CASE: Direct/Implied/Userset access (non-recursive paths)
        -- =====================================================================

        -- Path 1: Direct tuple lookup with simple closure relations
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        WHERE t.object_type = '{{.ObjectType}}'
          AND t.relation IN ({{.RelationList}})
          AND t.subject_type = p_subject_type
          AND p_subject_type IN ({{.AllowedSubjectTypes}})
          AND {{.SubjectIDCheck}}
{{- if .SimpleExcludedRelations }}
          -- Simple exclusions for direct path
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
          -- Complex exclusions for direct path
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}

{{- range .ComplexClosureRelations }}
        UNION
        -- Complex closure relation: {{.}}
        SELECT DISTINCT t.object_id, 0
        FROM melange_tuples t
        WHERE t.object_type = '{{$.ObjectType}}'
          AND t.relation = '{{.}}'
          AND t.subject_type = p_subject_type
          AND p_subject_type IN ({{$.AllowedSubjectTypes}})
          AND (t.subject_id = p_subject_id OR t.subject_id = '*')
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 1
{{- if $.SimpleExcludedRelations }}
          -- Apply simple exclusions to complex closure relation path
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
          -- Apply complex exclusions to complex closure relation path
{{- range $.ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}

{{- range .IntersectionClosureRelations }}
        UNION
        -- Compose with intersection closure relation: {{.}}
        SELECT DISTINCT o.object_id, 0
        FROM list_{{$.ObjectType}}_{{.}}_objects(p_subject_type, p_subject_id) o
{{- end }}

{{- range .UsersetPatterns }}
        UNION
        -- Userset path: Via {{.SubjectType}}#{{.SubjectRelation}} membership
{{- if .IsComplex }}
        -- Complex userset: use check_permission_internal for membership
        SELECT DISTINCT t.object_id, 0
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
        -- Simple userset: JOIN with membership tuples
        SELECT DISTINCT t.object_id, 0
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
          -- Apply simple exclusions to userset path
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
          -- Apply complex exclusions to userset path
{{- range $.ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', t.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- range .ParentRelations }}
{{- if .HasCrossTypeLinks }}

        UNION
        -- Cross-type TTU: {{.LinkingRelation}} -> {{.Relation}} on non-self types
        -- Find objects whose {{.LinkingRelation}} points to a parent where subject has {{.Relation}}
        -- This is non-recursive (uses check_permission_internal, not CTE reference)
        SELECT DISTINCT child.object_id, 0  -- depth 0 since this is a one-hop lookup
        FROM melange_tuples child
        WHERE child.object_type = '{{$.ObjectType}}'
          AND child.relation = '{{.LinkingRelation}}'
          AND child.subject_type IN ({{.CrossTypeLinkingTypes}})
          -- Verify subject has the required relation on the parent
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.Relation}}', child.subject_type, child.subject_id, ARRAY[]::TEXT[]) = 1
{{- if $.SimpleExcludedRelations }}
          -- Apply simple exclusions to cross-type TTU path
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
          -- Apply complex exclusions to cross-type TTU path
{{- range $.ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', child.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
{{- end }}

        -- =====================================================================
        -- RECURSIVE CASE: TTU paths (objects inheriting from accessible parents)
        -- This MUST be the last term - PostgreSQL only allows one recursive reference
        -- =====================================================================
{{- if .SelfReferentialLinkingRelations }}

        UNION ALL
        -- Self-referential TTU: follow linking relations to accessible parents
        -- Combined all self-referential TTU patterns into single recursive term
        SELECT DISTINCT child.object_id, a.depth + 1
        FROM accessible a
        JOIN melange_tuples child
          ON child.object_type = '{{.ObjectType}}'
          AND child.relation IN ({{.SelfReferentialLinkingRelations}})
          AND child.subject_type = '{{.ObjectType}}'
          AND child.subject_id = a.object_id
        WHERE a.depth < 25
{{- if .SimpleExcludedRelations }}
          -- Apply simple exclusions to recursive path
{{- range .SimpleExcludedRelations }}
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
{{- if .ComplexExcludedRelations }}
          -- Apply complex exclusions to recursive path
{{- range .ComplexExcludedRelations }}
          AND check_permission_internal(p_subject_type, p_subject_id, '{{.}}', '{{$.ObjectType}}', child.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
{{- end }}
{{- end }}
    )
    SELECT DISTINCT acc.object_id
    FROM accessible acc
{{- if .ExcludedParentRelations }}
    -- TTU exclusions: check_permission_internal for each linked parent
    WHERE TRUE
{{- range .ExcludedParentRelations }}
      AND NOT EXISTS (
          SELECT 1 FROM melange_tuples link
          WHERE link.object_type = '{{$.ObjectType}}'
            AND link.object_id = acc.object_id
            AND link.relation = '{{.LinkingRelation}}'
{{- if .AllowedLinkingTypes }}
            AND link.subject_type IN ({{.AllowedLinkingTypes}})
{{- end }}
            AND check_permission_internal(p_subject_type, p_subject_id, '{{.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
      )
{{- end }}
{{- end }}
{{- if .ExcludedIntersectionGroups }}
    -- Intersection exclusions
{{- if not .ExcludedParentRelations }}
    WHERE TRUE
{{- end }}
{{- range .ExcludedIntersectionGroups }}
      AND NOT (
{{- range $i, $part := .Parts }}
{{- if $i }}
          AND
{{- end }}
{{- if $part.ParentRelation }}
          EXISTS (
              SELECT 1 FROM melange_tuples link
              WHERE link.object_type = '{{$.ObjectType}}'
                AND link.object_id = acc.object_id
                AND link.relation = '{{$part.ParentRelation.LinkingRelation}}'
{{- if $part.ParentRelation.AllowedLinkingTypes }}
                AND link.subject_type IN ({{range $j, $lt := $part.ParentRelation.AllowedLinkingTypes}}{{if $j}}, {{end}}'{{$lt}}'{{end}})
{{- end }}
                AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ParentRelation.Relation}}', link.subject_type, link.subject_id, ARRAY[]::TEXT[]) = 1
          )
{{- else }}
          (check_permission_internal(p_subject_type, p_subject_id, '{{$part.Relation}}', '{{$.ObjectType}}', acc.object_id, ARRAY[]::TEXT[]) = 1
{{- if $part.ExcludedRelation }}
           AND check_permission_internal(p_subject_type, p_subject_id, '{{$part.ExcludedRelation}}', '{{$.ObjectType}}', acc.object_id, ARRAY[]::TEXT[]) = 0
{{- end }}
          )
{{- end }}
{{- end }}
      )
{{- end }}
{{- end }}

    UNION

    -- Self-candidate: when subject is a userset on the same object type
    SELECT split_part(p_subject_id, '#', 1) AS object_id
    WHERE position('#' in p_subject_id) > 0
      AND p_subject_type = '{{.ObjectType}}'
      AND EXISTS (
          SELECT 1 FROM (VALUES {{$.ClosureValues}}) AS c(object_type, relation, satisfying_relation)
          WHERE c.object_type = '{{.ObjectType}}'
            AND c.relation = '{{.Relation}}'
            AND c.satisfying_relation = substring(p_subject_id from position('#' in p_subject_id) + 1)
      );
END;
$$ LANGUAGE plpgsql STABLE;
