// Package style is the CLI's single terminal palette, expressed as lipgloss
// colors. Every styled surface (explain/expand trees, doctor, status, the
// update and advisory notices, the init wizard) takes its colors from here so
// they read as one tool.
//
// Styles render full 24-bit color; output is downsampled where it is written
// (see Writer), which is how lipgloss v2 handles terminals and NO_COLOR.
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
	"image/color"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
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

// Fixed brand colors for plain output. They do not adapt to the terminal
// background: that needs a background query, and a reply arriving after the
// CLI exits is echoed onto the user's prompt.
var (
	Accent = lipgloss.Color("#E8813A") // burnt orange
	Muted  = lipgloss.Color("#9C8E80") // warm gray
)

// Brand is the brand palette for one terminal background.
type Brand struct {
	BurntOrange, Sand, WarmGray, Cream color.Color
}

// BrandFor returns the brand palette for a dark or light background. Use it
// only where the background has already been queried, such as the interactive
// init wizard.
func BrandFor(isDark bool) Brand {
	ld := lipgloss.LightDark(isDark)
	return Brand{
		BurntOrange: ld(lipgloss.Color("#C45A20"), Accent),
		Sand:        ld(lipgloss.Color("#A68A64"), lipgloss.Color("#D4B896")),
		WarmGray:    ld(lipgloss.Color("#6B5E50"), Muted),
		Cream:       ld(lipgloss.Color("#8C7A66"), lipgloss.Color("#F5E6D0")),
	}
}

// Writer wraps w so styled text written to it is downsampled to what w
// supports, and stripped entirely unless w is a terminal with color and
// NO_COLOR is unset. colorprofile keeps bold and other non-color attributes
// under NO_COLOR; they are stripped too, so plain means plain. Wrapping an
// already-wrapped writer returns it unchanged: a second wrap would see a
// non-terminal and strip everything. Use it for output with no --color flag.
func Writer(w io.Writer) io.Writer {
	if cw, ok := w.(*colorprofile.Writer); ok {
		return cw
	}
	cw := colorprofile.NewWriter(w, os.Environ())
	if cw.Profile < colorprofile.ANSI {
		cw.Profile = colorprofile.NoTTY
	}
	return cw
}

// Enabled reports whether output to w should be colored by default: the same
// detection Writer uses, so every command agrees.
func Enabled(w io.Writer) bool {
	return colorprofile.Detect(w, os.Environ()) >= colorprofile.ANSI
}

// New returns a style that leaves tabs alone. lipgloss otherwise expands them
// to spaces on every Render, which would rewrite user text such as paths and
// error details.
func New() lipgloss.Style {
	return lipgloss.NewStyle().TabWidth(lipgloss.NoTabConversion)
}

// StatusStyles are the shared styles for status-bearing output (doctor,
// status), so a pass, warning or failure looks the same everywhere. Write
// them through Writer so they are stripped when not on a terminal.
var StatusStyles = struct {
	Good, Warn, Bad, Muted, Bold lipgloss.Style
}{
	Good:  New().Foreground(Pass),
	Warn:  New().Foreground(Warn),
	Bad:   New().Foreground(Fail).Bold(true),
	Muted: New().Foreground(Muted),
	Bold:  New().Bold(true),
}
