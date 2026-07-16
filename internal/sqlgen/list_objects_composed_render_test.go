package sqlgen

import (
	"strings"
	"testing"
)

// TestComposedListObjects_SelfCandidateFoldedIntoUnion verifies that the
// composed list_objects renderer folds the same-type userset self-candidate
// (OpenFGA userset-defines-itself reflexivity) into the main UNION instead of a
// separate IF EXISTS early-return that re-evaluated the identical predicate
// twice. A pure cross-type TTU (report.inherited_view: can_view from document)
// must NOT emit WITH RECURSIVE and must keep the reflexive self predicate.
func TestComposedListObjects_SelfCandidateFoldedIntoUnion(t *testing.T) {
	plan := ListPlan{
		ObjectType:          "report",
		Relation:            "inherited_view",
		FunctionName:        "list_report_inherited_view_obj",
		DatabaseSchema:      "public",
		AllowedSubjectTypes: []string{"user"},
		Analysis: RelationAnalysis{
			ObjectType:          "report",
			Relation:            "inherited_view",
			SatisfyingRelations: []string{"inherited_view"},
			IndirectAnchor: &IndirectAnchorInfo{
				AnchorType:     "document",
				AnchorRelation: "can_view",
				Path: []AnchorPathStep{{
					Type:            "ttu",
					LinkingRelation: "document",
					TargetType:      "document",
					TargetRelation:  "can_view",
					AllTargetTypes:  []string{"document"},
				}},
			},
		},
	}

	blocks, err := BuildListObjectsComposedBlocks(plan)
	if err != nil {
		t.Fatalf("BuildListObjectsComposedBlocks: %v", err)
	}
	sql, err := RenderListObjectsComposedFunction(plan, blocks)
	if err != nil {
		t.Fatalf("RenderListObjectsComposedFunction: %v", err)
	}

	if strings.Contains(sql, "IF EXISTS") {
		t.Errorf("composed self-candidate must be a UNION arm, not an IF EXISTS gate:\n%s", sql)
	}
	if strings.Contains(sql, "WITH RECURSIVE") {
		t.Errorf("cross-type-only TTU must not use WITH RECURSIVE:\n%s", sql)
	}
	if !strings.Contains(sql, "UNION") {
		t.Errorf("expected self-candidate folded into main UNION:\n%s", sql)
	}
	// Reflexive self predicate preserved (userset-defines-itself).
	if !strings.Contains(sql, "split_part(p_subject_id, '#', 2) IN ('inherited_view')") {
		t.Errorf("reflexive self-candidate predicate must be preserved:\n%s", sql)
	}
}
