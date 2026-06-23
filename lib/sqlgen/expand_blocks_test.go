package sqlgen

import (
	"strings"
	"testing"
)

func TestBuildExpandNodeName(t *testing.T) {
	got := BuildExpandNodeName("p_object_type", "p_object_id", "viewer")
	want := "(p_object_type || ':' || p_object_id || '#viewer')"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildExpandComputedLeafJSON(t *testing.T) {
	got := BuildExpandComputedLeafJSON("(p_object_type || ':' || p_object_id || '#editor')")
	want := "jsonb_build_object('leaf', jsonb_build_object('computed', jsonb_build_object('userset', (p_object_type || ':' || p_object_id || '#editor'))))"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildExpandUsersLeafJSON_NoTruncate(t *testing.T) {
	got := BuildExpandUsersLeafJSON("v_users", "")
	// Outer `leaf` wrapper required so concatenation with `{name: ...}`
	// produces the full UsersetTreeNode shape `{name: ..., leaf: {users: {...}}}`.
	want := "jsonb_build_object('leaf', jsonb_build_object('users', jsonb_build_object('users', v_users)))"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// users_truncated must NOT appear when expression is empty — it would
	// surface even on uncapped responses and pollute every OpenFGA
	// consumer that doesn't know the field exists.
	if strings.Contains(got, "users_truncated") {
		t.Errorf("users_truncated should be omitted; got %q", got)
	}
}

func TestBuildExpandUsersLeafJSON_WithTruncate(t *testing.T) {
	got := BuildExpandUsersLeafJSON("v_users", "v_users_truncated")
	if !strings.Contains(got, "CASE WHEN v_users_truncated THEN") {
		t.Errorf("truncation guard missing: %s", got)
	}
	if !strings.Contains(got, "'users_truncated', true") {
		t.Errorf("users_truncated literal missing: %s", got)
	}
	// ELSE branch must yield an empty object so the || merge is a no-op
	// when the flag is false — never surface `users_truncated: false`.
	if !strings.Contains(got, "ELSE '{}'::jsonb") {
		t.Errorf("ELSE branch missing for no-truncation case: %s", got)
	}
}

func TestBuildExpandUnionJSON(t *testing.T) {
	got := BuildExpandUnionJSON([]string{"v_child_a", "v_child_b"})
	want := "jsonb_build_object('union', jsonb_build_object('nodes', jsonb_build_array(v_child_a, v_child_b)))"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildExpandIntersectionJSON(t *testing.T) {
	got := BuildExpandIntersectionJSON([]string{"v_writer", "v_editor"})
	if !strings.Contains(got, "'intersection'") || !strings.Contains(got, "jsonb_build_array(v_writer, v_editor)") {
		t.Errorf("intersection shape wrong: %s", got)
	}
}

func TestBuildExpandDifferenceJSON(t *testing.T) {
	got := BuildExpandDifferenceJSON("v_base", "v_subtract")
	if !strings.Contains(got, "'base', v_base") || !strings.Contains(got, "'subtract', v_subtract") {
		t.Errorf("difference named slots missing: %s", got)
	}
	// Positional children would be wrong — OpenFGA requires named slots.
	if strings.Contains(got, "'children'") {
		t.Errorf("difference must not use positional children: %s", got)
	}
}

func TestBuildExpandTTULeafJSON(t *testing.T) {
	got := BuildExpandTTULeafJSON(
		"(p_object_type || ':' || p_object_id || '#parent')",
		[]string{
			"jsonb_build_object('userset', 'folder:foo#can_read')",
			"jsonb_build_object('userset', 'workspace:bar#can_read')",
		})
	if !strings.Contains(got, "'tuple_to_userset'") {
		t.Errorf("tuple_to_userset key missing: %s", got)
	}
	if !strings.Contains(got, "'tupleset'") || !strings.Contains(got, "#parent") {
		t.Errorf("tupleset missing: %s", got)
	}
	if !strings.Contains(got, "jsonb_build_array(") {
		t.Errorf("computed must be a JSONB array: %s", got)
	}
}

func TestBuildExpandNodeJSON(t *testing.T) {
	name := BuildExpandNodeName("'document'", "'1'", "viewer")
	value := BuildExpandComputedLeafJSON("'document:1#editor'")
	got := BuildExpandNodeJSON(name, value)
	// Name must come first so jsonb_build_object's key order matches the
	// Go struct's field order.
	if !strings.HasPrefix(got, "jsonb_build_object('name'") {
		t.Errorf("name key not first: %s", got)
	}
	if !strings.Contains(got, "'computed'") {
		t.Errorf("value not preserved: %s", got)
	}
}

func TestBuildExpandTreeRoot(t *testing.T) {
	got := BuildExpandTreeRoot("v_root")
	want := "jsonb_build_object('root', v_root)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
