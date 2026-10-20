package render

import (
	"strings"
	"testing"

	"github.com/pthm/melange/melange"
)

// Expected escape sequences, spelled out so the tests pin the rendered
// palette byte-for-byte rather than echoing the styles back. A change here
// means the CLI drifted from the openfga-dark theme.
const (
	ansiReset      = "\x1b[0m"
	colorType      = "\x1b[38;2;121;237;131m"  // #79ED83
	colorRelation  = "\x1b[38;2;32;241;245m"   // #20F1F5
	colorTypeRestr = "\x1b[38;2;206;236;147m"  // #CEEC93
	colorKeyword   = "\x1b[38;2;170;170;170m"  // #AAAAAA
	colorDim       = "\x1b[38;2;115;121;129m"  // #737981
	colorDenyMark  = "\x1b[1;38;2;255;83;112m" // bold #FF5370

	markerAllowChip = "\x1b[1;97;42m"              // bold white on ANSI green
	markerDenyChip  = "\x1b[1;97;48;2;255;83;112m" // bold white on #FF5370
)

// TestPalette_FailedNodeMark pins the bold pink ✗ on failed tree nodes.
func TestPalette_FailedNodeMark(t *testing.T) {
	deny := false
	tr := &melange.Trace{
		Subject: "user:bob", Relation: "viewer", Object: "document:1",
		Result: &deny,
		Root:   &melange.Node{Type: melange.NodeUnion, Result: &deny},
	}
	got := TraceString(tr, WithColor(true))
	assertContains(t, got, colorDenyMark+markerDeny+ansiReset, "failed node mark")
}

// TestPaint_KeepsTabs guards against lipgloss expanding tabs in painted text.
func TestPaint_KeepsTabs(t *testing.T) {
	if got := paint(opts{color: true}, keywordStyle, "a\tb"); !strings.Contains(got, "a\tb") {
		t.Errorf("paint rewrote the tab: %q", got)
	}
}
