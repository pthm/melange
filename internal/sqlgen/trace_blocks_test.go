package sqlgen

import (
	"strings"
	"testing"
)

func TestBuildEvidenceJSON_WithAlias(t *testing.T) {
	got := BuildEvidenceJSON("t")
	want := "jsonb_build_object(" +
		"'subject_type', t.subject_type, " +
		"'subject_id', t.subject_id, " +
		"'relation', t.relation, " +
		"'object_type', t.object_type, " +
		"'object_id', t.object_id)"
	if got != want {
		t.Errorf("with alias:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildEvidenceJSON_EmptyAliasPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("BuildEvidenceJSON(\"\") must panic to prevent ambiguous bare column refs")
		}
	}()
	_ = BuildEvidenceJSON("")
}

func TestBuildNodeJSON_OmitsEmptyFields(t *testing.T) {
	// Direct node with only type — should produce a minimal object.
	got := BuildNodeJSON(TraceNodeDirect, NodeJSONArgs{})
	want := "jsonb_build_object('type', 'direct')"
	if got != want {
		t.Errorf("minimal node:\n got: %s\nwant: %s", got, want)
	}

	// Direct node with label only.
	got = BuildNodeJSON(TraceNodeDirect, NodeJSONArgs{Label: "'direct grant'"})
	want = "jsonb_build_object('type', 'direct', 'label', 'direct grant')"
	if got != want {
		t.Errorf("labeled node:\n got: %s\nwant: %s", got, want)
	}
}

func TestBuildNodeJSON_AllFields(t *testing.T) {
	got := BuildNodeJSON(TraceNodeUserset, NodeJSONArgs{
		Label:    "'via group:eng#member'",
		Evidence: "jsonb_build_array(ev1, ev2)",
		Children: "jsonb_build_array(ch1)",
		Users:    "jsonb_build_array(u1)",
	})
	// Field ordering matters because jsonb_build_object preserves key order.
	keys := []string{"'type'", "'label'", "'evidence'", "'children'", "'users'"}
	for i, key := range keys {
		idx := strings.Index(got, key)
		if idx < 0 {
			t.Fatalf("missing key %s in: %s", key, got)
		}
		if i > 0 {
			prev := strings.Index(got, keys[i-1])
			if prev > idx {
				t.Errorf("keys out of order: %s before %s in: %s",
					keys[i-1], key, got)
			}
		}
	}
}

func TestBuildNodeJSON_ExplainResult(t *testing.T) {
	// Explain failure-path node — must emit result so the renderer can mark
	// the denied branch.
	got := BuildNodeJSON(TraceNodeDirect, NodeJSONArgs{
		Label:  "'no direct grant'",
		Result: "false",
	})
	if !strings.Contains(got, "'result', false") {
		t.Errorf("result field missing; got: %s", got)
	}
}

func TestBuildCycleAndTruncated(t *testing.T) {
	cyc := BuildCycleNode("v_key")
	if !strings.Contains(cyc, "'type', 'cycle'") {
		t.Errorf("cycle missing type discriminator: %s", cyc)
	}
	if !strings.Contains(cyc, "'label', v_key") {
		t.Errorf("cycle label not threaded through: %s", cyc)
	}

	trunc := BuildTruncatedNode()
	want := "jsonb_build_object('type', 'truncated')"
	if trunc != want {
		t.Errorf("truncated node:\n got: %s\nwant: %s", trunc, want)
	}
}

func TestBuildObjectIdentExpr(t *testing.T) {
	got := BuildObjectIdentExpr("p_object_type", "p_object_id")
	want := "(p_object_type || ':' || p_object_id)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
