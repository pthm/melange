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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}

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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "t.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" $ "ObjectIDExpr" "child.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
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
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "child.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}
{{- end }}
    )
    SELECT DISTINCT acc.object_id
    FROM accessible acc
    WHERE TRUE
{{template "list_objects_exclusions.tpl.sql" (dict "Root" . "ObjectIDExpr" "acc.object_id" "SubjectTypeExpr" "p_subject_type" "SubjectIDExpr" "p_subject_id")}}

{{template "list_objects_self_candidate.tpl.sql" .}}
END;
$$ LANGUAGE plpgsql STABLE;
