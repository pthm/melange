// Package style is the CLI's single terminal palette, expressed as lipgloss
// colors. Every styled surface (explain/expand trees, doctor, status, the
// update and advisory notices, the init wizard) takes its colors from here so
// they read as one tool.
//
// Two families:
//
//   - The OpenFGA palette, mapped from the openfga-dark theme of the OpenFGA
//     VS Code extension (themes/openfga-dark.json in openfga/vscode-ext), for
//     anything that shows model syntax: types, relations, restrictions.
//   - The melange brand palette (burnt orange, sand, warm gray, cream) for
//     CLI chrome: headings, prompts, hints.
//
// Status colors (Pass, Warn, Fail) are shared by both.
package style

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// OpenFGA palette.
var (
	TypeName        = lipgloss.Color("#79ED83") // "Type": user, document
	RelationName    = lipgloss.Color("#20F1F5") // "Relation": viewer, editor
	TypeRestriction = lipgloss.Color("#CEEC93") // "Type Restrictions": [user, group#member]
	Keyword         = lipgloss.Color("#AAAAAA") // "Keyword" / "Value": has, on, →, :
	Comment         = lipgloss.Color("#737981") // "Comment": tree connectors, dim prose
)

// Status colors.
var (
	Pass     = lipgloss.Color("2")       // ANSI green, so it follows the terminal theme
	Warn     = Accent                    // burnt orange
	Fail     = lipgloss.Color("#FF5370") // OpenFGA "Invalid"
	OnStatus = lipgloss.Color("15")      // bright white text on a Pass/Fail badge
)

// Brand palette. The adaptive forms feed the init wizard's huh theme, where
// bubbletea has already detected the terminal background. Plain output uses
// the fixed Accent and Muted instead: adaptive colors make lipgloss query the
// terminal background, and a reply arriving after the CLI exits is echoed onto
// the user's prompt.
var (
	BurntOrange = lipgloss.AdaptiveColor{Light: "#C45A20", Dark: "#E8813A"}
	Sand        = lipgloss.AdaptiveColor{Light: "#A68A64", Dark: "#D4B896"}
	WarmGray    = lipgloss.AdaptiveColor{Light: "#6B5E50", Dark: "#9C8E80"}
	Cream       = lipgloss.AdaptiveColor{Light: "#8C7A66", Dark: "#F5E6D0"}

	Accent = lipgloss.Color(BurntOrange.Dark)
	Muted  = lipgloss.Color(WarmGray.Dark)
)

// For returns a renderer for w that colors only when w is a terminal and
// NO_COLOR is unset. Use it for output that has no --color flag.
func For(w io.Writer) *lipgloss.Renderer {
	return lipgloss.NewRenderer(w)
}

// Enabled reports whether output to w should be colored by default: the same
// terminal and NO_COLOR detection For uses, so every command agrees.
func Enabled(w io.Writer) bool {
	return For(w).ColorProfile() != termenv.Ascii
}

// New returns a style on re that leaves tabs alone. lipgloss otherwise expands
// them to spaces on every Render, even when not coloring, which would rewrite
// user text such as paths and error details.
func New(re *lipgloss.Renderer) lipgloss.Style {
	return re.NewStyle().TabWidth(lipgloss.NoTabConversion)
}

// StatusStyles are the shared styles for status-bearing output (doctor,
// status), so a pass, warning or failure looks the same everywhere.
type StatusStyles struct {
	Good, Warn, Bad, Muted, Bold lipgloss.Style
}

// NewStatusStyles returns StatusStyles that color only when w is a terminal
// and NO_COLOR is unset.
func NewStatusStyles(w io.Writer) StatusStyles {
	re := For(w)
	return StatusStyles{
		Good:  New(re).Foreground(Pass),
		Warn:  New(re).Foreground(Warn),
		Bad:   New(re).Foreground(Fail).Bold(true),
		Muted: New(re).Foreground(Muted),
		Bold:  New(re).Bold(true),
	}
}

// TrueColor returns a renderer that always colors, in 24-bit color, wherever
// the output goes. Use it behind an explicit --color decision, applying styles
// only when color is on.
func TrueColor() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	return r
}

// Stderr is the renderer for notices written to stderr.
var Stderr = For(os.Stderr)
