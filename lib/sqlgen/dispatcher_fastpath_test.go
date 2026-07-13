package sqlgen

import (
	"strings"
	"testing"
)

// The internal dispatchers are invoked as per-row predicates inside generated
// check/list bodies, so each dispatch branch must stay a trivial PL/pgSQL
// simple expression. Two forms were measured on the issue #67 workload
// (PG15/16/18): the original RETURN (SELECT CASE ...) scalar subquery (~8.0s,
// sublink disqualifies the fast path), RETURN CASE ... END (~4.5s, one N-arm
// expression re-initialized per call), and an IF-chain of guarded single-call
// RETURNs (~2.6s). These tests pin the IF-chain rendering and guard against
// regressing to either slower form.
// The bulk dispatcher's final fallback must deny only genuinely-unknown object
// types (object_type NOT IN known_types), never (object_type, relation) pairs:
// a known-type/unknown-relation request is already denied once by its per-type
// IF block's relation fallback, so matching on pairs would emit a duplicate deny
// row for the same idx.
func TestBulkDispatcher_UnknownTypeFallbackDenominatedByTypeOnly(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
		mkAnalysis("folder", "viewer", RelationFeatures{HasDirect: true}, true),
	}
	for i := range analyses {
		analyses[i].DirectSubjectTypes = []string{"user"}
	}

	sql := generateBulkDispatcher(analyses, "")

	// Final fallback keys on object_type alone over the distinct known types.
	if !strings.Contains(sql, "t.object_type NOT IN ('document', 'folder')") {
		t.Errorf("bulk fallback must deny by object_type NOT IN (known types), got:\n%s", sql)
	}
	// It must NOT re-introduce the (object_type, relation) tuple match that
	// double-counted known-type/unknown-relation requests.
	if strings.Contains(sql, "t.relation) NOT IN") || strings.Contains(sql, "(t.object_type, t.relation)") {
		t.Errorf("bulk fallback must not match on (object_type, relation) pairs (duplicate deny rows), got:\n%s", sql)
	}
}

func TestDispatchers_SimpleExpressionFastPath(t *testing.T) {
	analyses := []RelationAnalysis{
		mkAnalysis("document", "viewer", RelationFeatures{HasDirect: true}, true),
		mkAnalysis("document", "editor", RelationFeatures{HasDirect: true}, true),
	}
	for i := range analyses {
		// Expand eligibility needs at least one rewrite; give the direct
		// rewrite a subject type.
		analyses[i].DirectSubjectTypes = []string{"user"}
	}

	checkSQL, err := generateDispatcher(analyses, "", false)
	if err != nil {
		t.Fatalf("generateDispatcher: %v", err)
	}
	explainSQL, err := generateExplainDispatcher(analyses, "", ComputeExplainEligibility(analyses))
	if err != nil {
		t.Fatalf("generateExplainDispatcher: %v", err)
	}
	expandSQL := generateExpandDispatcher(analyses, "", ComputeExpandEligibility(analyses))

	for name, sql := range map[string]string{
		"check":   checkSQL,
		"explain": explainSQL,
		"expand":  expandSQL,
	} {
		if !strings.Contains(sql, "IF p_object_type = 'document' THEN") || !strings.Contains(sql, "IF p_relation = 'viewer' THEN") {
			t.Errorf("%s dispatcher: missing nested IF-chain dispatch (type then relation) in:\n%s", name, sql)
		}
		if strings.Contains(sql, "RETURN (SELECT") {
			t.Errorf("%s dispatcher: RETURN wrapped in scalar subquery disqualifies the PL/pgSQL simple-expression fast path:\n%s", name, sql)
		}
		if strings.Contains(sql, "RETURN CASE") {
			t.Errorf("%s dispatcher: RETURN CASE is ~1.7x slower than the IF-chain on the hot path (issue #67); use dispatchIfChain:\n%s", name, sql)
		}
	}
}
