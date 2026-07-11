package sqlgen

import (
	"strings"
	"testing"
)

func TestBuildCrossTypeTTUBlocksUsesSubjectFirstParentList(t *testing.T) {
	parentAnalysis := RelationAnalysis{
		ObjectType: "namespace",
		Relation:   "admin",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyUserset,
	}

	plan := ListPlan{
		ObjectType: "channel",
		Relation:   "can_read",
		Analysis: RelationAnalysis{
			ObjectType: "channel",
			Relation:   "can_read",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"namespace.admin": &parentAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "admin",
		LinkingRelation:       "namespace",
		CrossTypeLinkingTypes: "'namespace'",
		HasCrossTypeLinks:     true,
	}})

	if len(blocks) != 2 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 2 (subject-first + userset-subject parity)", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_namespace_admin_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertContains(t, sql, "INNER JOIN melange_tuples AS child ON")
	assertContains(t, sql, "child.object_type = 'channel'")
	assertContains(t, sql, "child.relation = 'namespace'")
	assertContains(t, sql, "child.subject_type = 'namespace'")
	assertContains(t, sql, "child.subject_id = parent_obj.object_id")
	assertNotContains(t, sql, "check_permission_internal")

	// Parity companion: per-candidate check, guarded to userset-typed subjects.
	parity := blocks[1].Query.SQL()
	assertContains(t, parity, "position('#' in p_subject_id) > 0")
	assertContains(t, parity, "check_permission_internal")
}

func TestBuildCrossTypeTTUBlocksFallsBackForRecursiveParentList(t *testing.T) {
	// folder.viewer reaches back into document.viewer (mutual cross-type TTU),
	// so composing list functions would recurse infinitely — must fall back.
	parentAnalysis := RelationAnalysis{
		ObjectType: "folder",
		Relation:   "viewer",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
		ParentRelations: []ParentRelationInfo{{
			Relation:            "viewer",
			LinkingRelation:     "parent",
			AllowedLinkingTypes: []string{"document"},
		}},
	}

	plan := ListPlan{
		ObjectType: "document",
		Relation:   "viewer",
		Analysis: RelationAnalysis{
			ObjectType: "document",
			Relation:   "viewer",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"folder.viewer": &parentAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "viewer",
		LinkingRelation:       "parent",
		CrossTypeLinkingTypes: "'folder'",
		HasCrossTypeLinks:     true,
	}})

	if len(blocks) != 1 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 1", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM melange_tuples AS child")
	assertContains(t, sql, "child.subject_type IN ('folder')")
	assertContains(t, sql, "check_permission_internal")
	assertNotContains(t, sql, "list_folder_viewer_obj")
}

func TestBuildCrossTypeTTUBlocksVerifiesClosureSourceRelationWhenConstrained(t *testing.T) {
	parentAnalysis := RelationAnalysis{
		ObjectType: "namespace",
		Relation:   "admin",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyDirect,
	}
	sourceAnalysis := RelationAnalysis{
		ObjectType: "channel",
		Relation:   "reader",
		Features: RelationFeatures{
			HasExclusion: true,
		},
	}

	plan := ListPlan{
		ObjectType: "channel",
		Relation:   "can_read",
		Analysis: RelationAnalysis{
			ObjectType: "channel",
			Relation:   "can_read",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"namespace.admin": &parentAnalysis,
			"channel.reader":  &sourceAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "admin",
		LinkingRelation:       "namespace",
		CrossTypeLinkingTypes: "'namespace'",
		HasCrossTypeLinks:     true,
		IsClosurePattern:      true,
		SourceRelation:        "reader",
	}})

	if len(blocks) != 2 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 2 (subject-first + userset-subject parity)", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_namespace_admin_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertContains(t, sql, "check_permission_internal(p_subject_type, p_subject_id, 'reader', 'channel', child.object_id, ARRAY[]::TEXT[]) = 1")
}

func TestBuildCrossTypeTTUBlocksSkipsClosureSourceCheckWhenUnconstrained(t *testing.T) {
	parentAnalysis := RelationAnalysis{
		ObjectType: "namespace",
		Relation:   "admin",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyDirect,
	}
	sourceAnalysis := RelationAnalysis{
		ObjectType: "channel",
		Relation:   "reader",
	}

	plan := ListPlan{
		ObjectType: "channel",
		Relation:   "can_read",
		Analysis: RelationAnalysis{
			ObjectType: "channel",
			Relation:   "can_read",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"namespace.admin": &parentAnalysis,
			"channel.reader":  &sourceAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "admin",
		LinkingRelation:       "namespace",
		CrossTypeLinkingTypes: "'namespace'",
		HasCrossTypeLinks:     true,
		IsClosurePattern:      true,
		SourceRelation:        "reader",
	}})

	if len(blocks) != 2 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 2 (subject-first + userset-subject parity)", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_namespace_admin_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertNotContains(t, sql, "check_permission_internal")
}

func TestBuildCrossTypeTTUBlocksFallbackVerifiesClosureSourceRelation(t *testing.T) {
	// folder.viewer reaches back into document.can_read, so composition is
	// unsafe and the fallback must carry the closure-source verification.
	parentAnalysis := RelationAnalysis{
		ObjectType: "folder",
		Relation:   "viewer",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
		ParentRelations: []ParentRelationInfo{{
			Relation:            "can_read",
			LinkingRelation:     "parent",
			AllowedLinkingTypes: []string{"document"},
		}},
	}
	sourceAnalysis := RelationAnalysis{
		ObjectType: "document",
		Relation:   "reader",
		Features: RelationFeatures{
			HasIntersection: true,
		},
	}

	plan := ListPlan{
		ObjectType: "document",
		Relation:   "can_read",
		Analysis: RelationAnalysis{
			ObjectType: "document",
			Relation:   "can_read",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"folder.viewer":   &parentAnalysis,
			"document.reader": &sourceAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "viewer",
		LinkingRelation:       "parent",
		CrossTypeLinkingTypes: "'folder'",
		HasCrossTypeLinks:     true,
		IsClosurePattern:      true,
		SourceRelation:        "reader",
	}})

	if len(blocks) != 1 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 1", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM melange_tuples AS child")
	assertContains(t, sql, "child.subject_type IN ('folder')")
	assertContains(t, sql, "check_permission_internal(p_subject_type, p_subject_id, 'reader', 'document', child.object_id, ARRAY[]::TEXT[]) = 1")
	assertNotContains(t, sql, "list_folder_viewer_obj")
}

func TestBuildCrossTypeTTUBlocksComposesAcyclicRecursiveParent(t *testing.T) {
	// workspace.manage is Recursive but only reaches upward (organization),
	// never back into element — composing with its list function is safe.
	parentAnalysis := RelationAnalysis{
		ObjectType: "workspace",
		Relation:   "manage",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
		ParentRelations: []ParentRelationInfo{{
			Relation:            "manage",
			LinkingRelation:     "organization",
			AllowedLinkingTypes: []string{"organization"},
		}},
	}

	plan := ListPlan{
		ObjectType: "element",
		Relation:   "view",
		Analysis: RelationAnalysis{
			ObjectType: "element",
			Relation:   "view",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"workspace.manage": &parentAnalysis,
		},
	}

	blocks := buildCrossTypeTTUBlocks(plan, []ListParentRelationData{{
		Relation:              "manage",
		LinkingRelation:       "workspace",
		CrossTypeLinkingTypes: "'workspace'",
		HasCrossTypeLinks:     true,
	}})

	if len(blocks) != 2 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 2 (subject-first + userset-subject parity)", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_workspace_manage_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertNotContains(t, sql, "check_permission_internal")

	parity := blocks[1].Query.SQL()
	assertContains(t, parity, "position('#' in p_subject_id) > 0")
	assertContains(t, parity, "check_permission_internal")
}

func TestBuildRecursiveComplexUsersetBlockComposesWhenSafe(t *testing.T) {
	// workspace.view is complex (has its own TTU) but never reaches back into
	// element — membership resolves via semi-join on its list function.
	targetAnalysis := RelationAnalysis{
		ObjectType: "workspace",
		Relation:   "view",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
		ParentRelations: []ParentRelationInfo{{
			Relation:            "manage",
			LinkingRelation:     "organization",
			AllowedLinkingTypes: []string{"organization"},
		}},
	}

	plan := ListPlan{
		ObjectType:     "element",
		Relation:       "view",
		DatabaseSchema: "",
		Analysis: RelationAnalysis{
			ObjectType: "element",
			Relation:   "view",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"workspace.view": &targetAnalysis,
		},
	}

	pattern := listUsersetPatternInput{
		SubjectType:     "workspace",
		SubjectRelation: "view",
		SourceRelations: []string{"view"},
		IsComplex:       true,
	}

	sql := buildRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()
	assertContains(t, sql, "IN (SELECT obj.object_id FROM list_workspace_view_obj(p_subject_type, p_subject_id, NULL, NULL) obj)")
	// Userset-typed subjects keep a guarded per-candidate check arm.
	assertContains(t, sql, "position('#' in p_subject_id) > 0")
	assertContains(t, sql, "check_permission_internal")
}

func TestBuildRecursiveComplexUsersetBlockFallsBackWhenCyclic(t *testing.T) {
	// group.member holds a userset of element.view — composing would recurse.
	targetAnalysis := RelationAnalysis{
		ObjectType: "group",
		Relation:   "member",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
		UsersetPatterns: []UsersetPattern{{
			SubjectType:     "element",
			SubjectRelation: "view",
		}},
	}

	plan := ListPlan{
		ObjectType: "element",
		Relation:   "view",
		Analysis: RelationAnalysis{
			ObjectType: "element",
			Relation:   "view",
		},
		AnalysisLookup: map[string]*RelationAnalysis{
			"group.member": &targetAnalysis,
		},
	}

	pattern := listUsersetPatternInput{
		SubjectType:     "group",
		SubjectRelation: "member",
		SourceRelations: []string{"view"},
		IsComplex:       true,
	}

	sql := buildRecursiveComplexUsersetBlock(plan, pattern).Query.SQL()
	assertContains(t, sql, "check_permission_internal")
	assertNotContains(t, sql, "list_group_member_obj")
}

func TestListCompositionSafeSelfAndTransitive(t *testing.T) {
	lookup := map[string]*RelationAnalysis{
		"b.rel": {ObjectType: "b", Relation: "rel", ParentRelations: []ParentRelationInfo{{
			Relation: "rel", LinkingRelation: "link", AllowedLinkingTypes: []string{"c"},
		}}},
		"c.rel": {ObjectType: "c", Relation: "rel", UsersetPatterns: []UsersetPattern{{
			SubjectType: "a", SubjectRelation: "rel",
		}}},
	}

	if listCompositionSafe(lookup, "a", "rel", "a", "rel") {
		t.Error("self-composition must be unsafe")
	}
	if listCompositionSafe(lookup, "a", "rel", "b", "rel") {
		t.Error("transitive cycle a->b->c->a must be unsafe")
	}
	if !listCompositionSafe(lookup, "x", "rel", "b", "rel") {
		t.Error("unreachable relation must be safe")
	}
	if listCompositionSafe(nil, "x", "rel", "b", "rel") {
		t.Error("nil lookup must be unsafe (no analysis available)")
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("SQL missing %q:\n%s", want, got)
	}
}

func assertNotContains(t *testing.T, got, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Fatalf("SQL unexpectedly contains %q:\n%s", unwanted, got)
	}
}
