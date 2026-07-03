package command

import (
	"strings"
	"testing"

	"github.com/pthm/melange/pkg/schema"
)

func TestSemanticDiffHeader(t *testing.T) {
	// No changes → no header, so a version-only migration stays clean.
	if h := semanticDiffHeader(schema.SchemaDiff{}); h != "" {
		t.Errorf("empty diff should yield no header, got %q", h)
	}

	diff := schema.SchemaDiff{Changes: []schema.Change{
		{Class: schema.ClassBreaking, Type: "document", Relation: "viewer", Summary: "relation document.viewer removed"},
		{Class: schema.ClassAdditive, Type: "audit_log", Summary: "type audit_log added"},
	}}
	h := semanticDiffHeader(diff)

	for _, want := range []string{
		"1 breaking, 1 additive",
		"[breaking] relation document.viewer removed",
		"[additive] type audit_log added",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("header missing %q\n%s", want, h)
		}
	}

	// Every line must be a SQL comment so the header is inert in the migration.
	for _, line := range strings.Split(strings.TrimRight(h, "\n"), "\n") {
		if !strings.HasPrefix(line, "--") {
			t.Errorf("header line is not a SQL comment: %q", line)
		}
	}
}
