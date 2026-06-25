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

	if len(blocks) != 1 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 1", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_namespace_admin_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertContains(t, sql, "INNER JOIN melange_tuples AS child ON")
	assertContains(t, sql, "child.object_type = 'channel'")
	assertContains(t, sql, "child.relation = 'namespace'")
	assertContains(t, sql, "child.subject_type = 'namespace'")
	assertContains(t, sql, "child.subject_id = parent_obj.object_id")
	assertNotContains(t, sql, "check_permission_internal")
}

func TestBuildCrossTypeTTUBlocksFallsBackForRecursiveParentList(t *testing.T) {
	parentAnalysis := RelationAnalysis{
		ObjectType: "folder",
		Relation:   "viewer",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
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

	if len(blocks) != 1 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 1", len(blocks))
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

	if len(blocks) != 1 {
		t.Fatalf("buildCrossTypeTTUBlocks() returned %d blocks, want 1", len(blocks))
	}

	sql := blocks[0].Query.SQL()
	assertContains(t, sql, "FROM list_namespace_admin_obj(p_subject_type, p_subject_id, NULL, NULL) AS parent_obj")
	assertNotContains(t, sql, "check_permission_internal")
}

func TestBuildCrossTypeTTUBlocksFallbackVerifiesClosureSourceRelation(t *testing.T) {
	parentAnalysis := RelationAnalysis{
		ObjectType: "folder",
		Relation:   "viewer",
		Capabilities: GenerationCapabilities{
			ListAllowed: true,
		},
		ListStrategy: ListStrategyRecursive,
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
