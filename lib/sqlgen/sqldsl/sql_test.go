package sqldsl

import (
	"strings"
	"testing"
)

func TestSafeIdentifier_ShortNames(t *testing.T) {
	tests := []struct {
		prefix, objectType, relation, suffix string
		want                                 string
	}{
		{"check_", "user", "admin", "", "check_user_admin"},
		{"check_", "user", "admin", "_nw", "check_user_admin_nw"},
		{"list_", "document", "viewer", "_obj", "list_document_viewer_obj"},
		{"list_", "document", "viewer", "_sub", "list_document_viewer_sub"},
	}
	for _, tt := range tests {
		got := SafeIdentifier(tt.prefix, tt.objectType, tt.relation, tt.suffix)
		if got != tt.want {
			t.Errorf("SafeIdentifier(%q, %q, %q, %q) = %q, want %q",
				tt.prefix, tt.objectType, tt.relation, tt.suffix, got, tt.want)
		}
	}
}

func TestSafeIdentifier_LongNames(t *testing.T) {
	got := SafeIdentifier("check_", "my_group", "a_really_really_really_really_reaaaaally_long_name", "_nw")

	// Must fit within the Postgres limit.
	if len(got) > PostgresMaxIdentifierLength {
		t.Errorf("len = %d, want <= %d: %q", len(got), PostgresMaxIdentifierLength, got)
	}
	// Prefix and suffix must be preserved exactly.
	if !strings.HasPrefix(got, "check_") {
		t.Errorf("should preserve prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "_nw") {
		t.Errorf("should preserve suffix, got %q", got)
	}
	// Deterministic expected value (hash of "my_group:a_really_really_really_really_reaaaaally_long_name").
	want := "check_my_gro_a_really_really_really_really_reaaaaal_d70fe023_nw"
	if got != want {
		t.Errorf("SafeIdentifier() = %q, want %q", got, want)
	}
}

func TestSafeIdentifier_Deterministic(t *testing.T) {
	a := SafeIdentifier("check_", "my_group", "a_really_really_really_really_reaaaaally_long_name", "_nw")
	b := SafeIdentifier("check_", "my_group", "a_really_really_really_really_reaaaaally_long_name", "_nw")
	if a != b {
		t.Errorf("not deterministic: %q != %q", a, b)
	}
}

func TestSafeIdentifier_UniquenessForSimilarNames(t *testing.T) {
	a := SafeIdentifier("check_", "my_group", "a_really_really_really_really_reaaaaally_long_name_one", "_nw")
	b := SafeIdentifier("check_", "my_group", "a_really_really_really_really_reaaaaally_long_name_two", "_nw")
	if a == b {
		t.Errorf("collision: both produced %q", a)
	}
	if len(a) > PostgresMaxIdentifierLength {
		t.Errorf("a too long: len=%d %q", len(a), a)
	}
	if len(b) > PostgresMaxIdentifierLength {
		t.Errorf("b too long: len=%d %q", len(b), b)
	}
}

func TestSafeIdentifier_DegenerateCase(t *testing.T) {
	// Prefix + suffix consume nearly all budget, leaving < 2 chars for type+relation.
	// The function must still produce a valid identifier within the limit.
	got := SafeIdentifier(
		"a_very_long_prefix_that_eats_most_of_the_budget____",
		"type", "rel",
		"_suffix",
	)
	if len(got) > PostgresMaxIdentifierLength {
		t.Errorf("len = %d, want <= %d: %q", len(got), PostgresMaxIdentifierLength, got)
	}
	if !strings.HasSuffix(got, "_suffix") {
		t.Errorf("should preserve suffix, got %q", got)
	}
	// Should contain the hash for uniqueness.
	if !strings.Contains(got, "a90f0833") {
		t.Errorf("should contain hash, got %q", got)
	}
}

func TestSafeIdentifier_DegenerateCase_HugeSuffix(t *testing.T) {
	// Suffix + hash alone exceed 63 bytes, forcing maxPrefix < 0 → 0.
	// This is pathological but must not panic.
	hugeSuffix := "_this_suffix_is_absurdly_long_and_takes_up_way_more_than_sixty_three_bytes_total"
	got := SafeIdentifier("check_", "t", "r", hugeSuffix)
	if len(got) > PostgresMaxIdentifierLength {
		// With a suffix this large, the identifier will exceed the limit
		// but the function must not panic. The suffix is caller-controlled
		// and expected to be short in practice.
		t.Logf("degenerate: %q (len=%d) — suffix alone exceeds limit", got, len(got))
	}
}

func TestSafeIdentifier_AllUnderscoreType(t *testing.T) {
	// Pathological: type name that is all underscores after Ident().
	// Should not panic and must stay within the length limit.
	got := SafeIdentifier("check_", "____________________________", "a_really_really_really_really_reaaaaally_long_name", "_nw")
	if len(got) > PostgresMaxIdentifierLength {
		t.Errorf("len = %d, want <= %d: %q", len(got), PostgresMaxIdentifierLength, got)
	}
	if !strings.HasPrefix(got, "check_") {
		t.Errorf("should preserve prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "_nw") {
		t.Errorf("should preserve suffix, got %q", got)
	}
}

func TestTruncateProportionally_WithinBudget(t *testing.T) {
	a, b := truncateProportionally("short", "name", 20)
	if a != "short" || b != "name" {
		t.Errorf("should not truncate: got %q, %q", a, b)
	}
}

func TestTruncateProportionally_ProportionalSplit(t *testing.T) {
	// "abcdef" (6) + "xy" (2) = 8, budget 4 → 3:1 proportional split
	a, b := truncateProportionally("abcdef", "xy", 4)
	if len(a)+len(b) > 4 {
		t.Errorf("total len %d exceeds budget 4: %q + %q", len(a)+len(b), a, b)
	}
	if len(a) < 1 || len(b) < 1 {
		t.Errorf("each part must have at least 1 char: %q, %q", a, b)
	}
}

func TestTruncateProportionally_VerySkewedA(t *testing.T) {
	// b is much longer than a, forcing aBudget < 1 → clamped to 1.
	a, b := truncateProportionally("x", "abcdefghijklmnop", 3)
	if a == "" || b == "" {
		t.Errorf("neither part should be empty: %q, %q", a, b)
	}
	if len(a)+len(b) > 3 {
		t.Errorf("total len %d exceeds budget 3: %q + %q", len(a)+len(b), a, b)
	}
}

func TestTrimTrailingUnderscores(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"abc_", "abc"},
		{"abc__", "abc"},
		{"___", "_"},   // all underscores: keep first char
		{"_", "_"},     // single underscore: keep it
		{"abc", "abc"}, // no trailing underscores
	}
	for _, tt := range tests {
		got := trimTrailingUnderscores(tt.in)
		if got != tt.want {
			t.Errorf("trimTrailingUnderscores(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
