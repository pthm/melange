package render

import (
	"strings"

	"github.com/pthm/melange/internal/cli/style"
)

// Styles for the OpenFGA palette (see package style). Output is written
// straight through in 24-bit color, not downsampled, so the on-terminal output
// matches the editor palette on modern terminals; whether to color at all is
// the caller's WithColor decision, and consumers on legacy terminals opt out
// with `--color=never`.
//
// Scope mapping:
//   - Type name (`user`, `document`)          → typeStyle
//   - Relation name (`viewer`, `editor`)      → relationStyle
//   - Type restrictions (`[user]`, brackets)  → typeRestrStyle
//   - Keywords / connectors (has, on, →, :)   → keywordStyle
//   - Tree connectors + dim prose             → dimStyle
//   - Deny / invalid / truncation warnings    → denyStyle
var (
	typeStyle      = style.New().Foreground(style.TypeName)
	relationStyle  = style.New().Foreground(style.RelationName)
	typeRestrStyle = style.New().Foreground(style.TypeRestriction)
	keywordStyle   = style.New().Foreground(style.Keyword)
	dimStyle       = style.New().Foreground(style.Comment)
	denyStyle      = style.New().Foreground(style.Fail)

	// Per-node failure markers: bold pink foreground, not a chip, so the
	// tree stays calm and only the header verdict gets the badge.
	denyMarkStyle = denyStyle.Bold(true)

	// Marker chip styles: bold white on a colored background, for the ✓/✗
	// result in headers so the decision jumps off the terminal. Plain mode
	// keeps the single-character marker so pipe/capture consumers see the
	// same shape they always did.
	allowChipStyle = style.New().Bold(true).Foreground(style.OnStatus).Background(style.Pass)
	denyChipStyle  = style.New().Bold(true).Foreground(style.OnStatus).Background(style.Fail)
)

// paintAllowChip renders the ✓ marker as a bold white glyph on a
// green background chip when color is on, or as the plain "✓"
// character otherwise. The chip has one space of padding on each
// side so the colored background reads as a proper badge rather
// than a tinted glyph — matches how status pills look in CI logs
// and GitHub PR checks.
func paintAllowChip(o opts) string {
	if !o.color {
		return markerAllow
	}
	return allowChipStyle.Render(" " + markerAllow + " ")
}

// paintDenyChip is the ✗ counterpart to paintAllowChip. Uses the
// OpenFGA "Invalid" pink (#FF5370) as the background so the deny
// chip is instantly distinguishable from allow.
func paintDenyChip(o opts) string {
	if !o.color {
		return markerDeny
	}
	return denyChipStyle.Render(" " + markerDeny + " ")
}

// paintObjectIdent renders an OpenFGA "type:id" identifier with the
// type portion colored as a type name and the ":" delimiter dimmed.
// The id portion stays uncolored so the terminal foreground shows
// through — matches how VS Code's semantic highlighting leaves free
// text alone when no scope claims it.
//
// Inputs without a ":" (defensive — shouldn't occur for real trace
// data) render uncolored to avoid mangling unknown shapes.
func paintObjectIdent(o opts, s string) string {
	idx := strings.IndexByte(s, ':')
	if idx == -1 {
		return s
	}
	return paint(o, typeStyle, s[:idx]) + paint(o, keywordStyle, ":") + s[idx+1:]
}

// paintUsersetIdent renders an OpenFGA "type:id#relation" userset
// reference. Type / id styled as paintObjectIdent, the `#` styled as a
// keyword delimiter, the relation colored cyan.
//
// Strings that don't contain a `#` fall through to paintObjectIdent
// so callers can pass any identifier they emit — plain object,
// wildcard, userset — without branching.
func paintUsersetIdent(o opts, s string) string {
	hash := strings.IndexByte(s, '#')
	if hash == -1 {
		return paintObjectIdent(o, s)
	}
	return paintObjectIdent(o, s[:hash]) +
		paint(o, keywordStyle, "#") +
		paint(o, relationStyle, s[hash+1:])
}

// paintRelation is the styled wrapper for a bare relation name (used
// in headers and node labels where a relation appears without an
// object prefix).
func paintRelation(o opts, s string) string {
	return paint(o, relationStyle, s)
}

// paintKeyword styles a DSL-like keyword / connector token. Applies
// to prose words the renderer emits (has, on, does NOT have, via,
// union of, implied, direct) and single-char delimiters (→, ⇒).
func paintKeyword(o opts, s string) string {
	return paint(o, keywordStyle, s)
}

// paintTypeRestriction styles a `[type[, type]]` bracket group — used
// in Explain labels that reference userset patterns like
// "via [group#member] → group:eng".
func paintTypeRestriction(o opts, s string) string {
	return paint(o, typeRestrStyle, s)
}
