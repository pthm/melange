package style

import (
	"fmt"
	"strings"
	"testing"
)

func TestWriter_StripsForNonTerminalAndIsIdempotent(t *testing.T) {
	var b strings.Builder
	w := Writer(&b)
	if Writer(w) != w {
		t.Fatal("wrapping a wrapped writer must return it unchanged")
	}
	_, _ = fmt.Fprint(w, StatusStyles.Bad.Render("x\ty"))
	if got := b.String(); got != "x\ty" {
		t.Errorf("non-terminal output = %q, want plain %q", got, "x\ty")
	}
}
