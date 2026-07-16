package sqlgen

import (
	"strings"
	"testing"

	"github.com/pthm/melange/lib/sqlgen/sqldsl"
)

// TestGeneratedFunctionNameBuildersWithinIdentifierLimit guards against the
// class of bug fixed in issue #42: a generated function name exceeding
// PostgreSQL's 63-char identifier limit is silently truncated, which can
// collide two functions or break a reference. Every per-relation name builder
// must route through SafeIdentifier (which hashes + truncates at the limit).
// This covers the newer expand_/explain_ families, whose longer prefixes cross
// the limit at a shorter type/relation length than check_.
func TestGeneratedFunctionNameBuildersWithinIdentifierLimit(t *testing.T) {
	longType := strings.Repeat("very_long_type_", 8) // ~120 chars
	longRel := strings.Repeat("very_long_relation_", 8)

	cases := []struct{ typ, rel string }{
		{longType, longRel},
		{longType, "r"},
		{"t", longRel},
		{
			"organization_with_a_very_long_type_name_for_testing_limits",
			"extremely_long_relation_name_owner_admin_super_manager",
		},
	}
	builders := []struct {
		name string
		fn   func(objectType, relation string) string
	}{
		{"check", functionName},
		{"check_nw", functionNameNoWildcard},
		{"list_obj", listObjectsFunctionName},
		{"list_sub", listSubjectsFunctionName},
		{"expand", expandFunctionName},
		{"explain", explainFunctionName},
	}

	for _, c := range cases {
		// A given (type, relation) must map to distinct names across families.
		seen := map[string]string{}
		for _, b := range builders {
			got := b.fn(c.typ, c.rel)
			if len(got) > sqldsl.PostgresMaxIdentifierLength {
				t.Errorf("%s(%q, %q) len=%d exceeds limit %d: %q",
					b.name, c.typ, c.rel, len(got), sqldsl.PostgresMaxIdentifierLength, got)
			}
			if prev, dup := seen[got]; dup {
				t.Errorf("name collision between %s and %s families: both %q", prev, b.name, got)
			}
			seen[got] = b.name
		}
	}
}
