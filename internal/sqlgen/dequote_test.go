package sqlgen

import (
	"slices"
	"testing"
)

func TestDequoteLinkingRelations(t *testing.T) {
	got := dequoteLinkingRelations(formatSQLStringList([]string{"document", "folder", "project"}))
	if want := []string{"document", "folder", "project"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
