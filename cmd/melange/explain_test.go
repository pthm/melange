package main

import (
	"strings"
	"testing"
)

// TestParseTypedIdent_Valid pins the happy path: a well-formed "<type>:<id>"
// string round-trips into a melange.Object with the expected fields.
func TestParseTypedIdent_Valid(t *testing.T) {
	cases := []struct {
		in       string
		wantType string
		wantID   string
	}{
		{"user:alice", "user", "alice"},
		{"organization:42", "organization", "42"},
		// Multi-colon IDs (e.g. "doc:proj/foo:1") preserve the suffix as
		// the id — the parser splits on the FIRST colon only. Documented
		// here so a future "strict mode" change has a regression to catch.
		{"document:proj:1", "document", "proj:1"},
		// Userset references carry a "#relation" suffix in the id portion;
		// the parser does not need to decompose it — the SQL functions
		// detect '#' downstream. We pin that the suffix survives the split
		// so userset-subject Explain calls work end-to-end.
		{"group:eng#member", "group", "eng#member"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			obj, err := parseTypedIdent(tc.in, "subject")
			if err != nil {
				t.Fatalf("parseTypedIdent(%q): unexpected error: %v", tc.in, err)
			}
			if string(obj.Type) != tc.wantType {
				t.Errorf("type: got %q, want %q", obj.Type, tc.wantType)
			}
			if obj.ID != tc.wantID {
				t.Errorf("id: got %q, want %q", obj.ID, tc.wantID)
			}
		})
	}
}

// TestParseTypedIdent_Invalid pins the rejection rules so a future change
// can't silently start accepting malformed identifiers. Each case is a shape
// that would push a SQL error onto the user; the parser must convert these
// to a usage-time error instead.
func TestParseTypedIdent_Invalid(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		role   string
		errSub string
	}{
		{"empty", "", "subject", "subject identifier"},
		{"no colon", "userwithoutcolon", "subject", "expected <type>:<id>"},
		{"only colon", ":", "object", "expected <type>:<id>"},
		{"leading colon (empty type)", ":alice", "subject", "expected <type>:<id>"},
		{"trailing colon (empty id)", "user:", "object", "expected <type>:<id>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTypedIdent(tc.in, tc.role)
			if err == nil {
				t.Fatalf("parseTypedIdent(%q): expected error, got nil", tc.in)
			}
			if !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error missing %q substring; got: %v", tc.errSub, err)
			}
		})
	}
}

// TestShouldColorize pins the --color resolution rules. NO_COLOR override
// must short-circuit even when the explicit mode is "auto"; "always"/"never"
// must ignore environment. The auto branch's TTY detection isn't exercised
// here (it depends on os.Stdout state at test time), but the never/always
// branches are pure functions and easy to pin.
func TestShouldColorize(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if !shouldColorize("always") {
		t.Errorf("--color=always should return true regardless of TTY/env")
	}
	if shouldColorize("never") {
		t.Errorf("--color=never should return false regardless of TTY/env")
	}
	if shouldColorize("bogus") {
		t.Errorf("unknown --color mode should fall through to false (safe default)")
	}

	// NO_COLOR overrides auto, per https://no-color.org. Set it to anything
	// non-empty and auto must return false even if a TTY is attached.
	t.Setenv("NO_COLOR", "1")
	if shouldColorize("auto") {
		t.Errorf("NO_COLOR set should disable color under --color=auto")
	}
	if shouldColorize("") {
		t.Errorf("empty mode (default) should behave as auto and honor NO_COLOR")
	}
}
