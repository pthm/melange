package render

import "strings"

// Palette mapped from the OpenFGA VS Code extension's openfga-dark
// theme (themes/openfga-dark.json in openfga/vscode-ext). Every colour
// is emitted as a 24-bit ANSI escape so the on-terminal output matches
// the editor palette byte-for-byte on modern terminals; consumers on
// legacy terminals opt out with `--color=never`.
//
// Scope mapping:
//   - Type name (`user`, `document`)          → colorType         (#79ED83)
//   - Relation name (`viewer`, `editor`)      → colorRelation     (#20F1F5)
//   - Type restrictions (`[user]`, brackets)  → colorTypeRestr    (#CEEC93)
//   - Keywords / connectors (has, on, →, :)   → colorKeyword      (#AAAAAA)
//   - Tree connectors + dim prose             → colorDim          (#737981)
//   - Allow marker                            → colorAllow        (bright green)
//   - Deny / invalid / truncation warnings    → colorDeny         (#FF5370)
//
// The `ansiGrey` / `ansiGreen` / `ansiRed` names are retained as
// aliases so existing test and call sites continue to compile.
const (
	colorType      = "\x1b[38;2;121;237;131m" // #79ED83 — matches OpenFGA "Type"
	colorRelation  = "\x1b[38;2;32;241;245m"  // #20F1F5 — matches OpenFGA "Relation"
	colorTypeRestr = "\x1b[38;2;206;236;147m" // #CEEC93 — matches OpenFGA "Type Restrictions"
	colorKeyword   = "\x1b[38;2;170;170;170m" // #AAAAAA — matches OpenFGA "Keyword" / "Value"
	colorDim       = "\x1b[38;2;115;121;129m" // #737981 — matches OpenFGA "Comment"
	colorAllow     = "\x1b[32m"               // bright green — foreground allow (retained for symmetry)
	colorDeny      = "\x1b[38;2;255;83;112m"  // #FF5370 — matches OpenFGA "Invalid" (foreground)

	// Marker chip styles: bold white foreground on a coloured
	// background. Used for the ✓/✗ result markers in headers and
	// per-node failure indicators so the decision jumps off the
	// terminal at a glance. Emitted only in coloured mode; plain
	// mode keeps the single-character marker so pipe/capture
	// consumers see the same shape they always did.
	markerAllowChip = "\x1b[1;97;42m"              // bold white on ANSI green (16-colour bg = universal support)
	markerDenyChip  = "\x1b[1;97;48;2;255;83;112m" // bold white on #FF5370 24-bit bg

	// Retained aliases for existing paint() call sites.
	ansiReset = "\x1b[0m"
	ansiGreen = colorAllow
	ansiRed   = colorDeny
	ansiGrey  = colorDim
)

// paintAllowChip renders the ✓ marker as a bold white glyph on a
// green background chip when colour is on, or as the plain "✓"
// character otherwise. The chip has one space of padding on each
// side so the coloured background reads as a proper badge rather
// than a tinted glyph — matches how status pills look in CI logs
// and GitHub PR checks.
func paintAllowChip(o opts) string {
	if !o.color {
		return markerAllow
	}
	return markerAllowChip + " " + markerAllow + " " + ansiReset
}

// paintDenyChip is the ✗ counterpart to paintAllowChip. Uses the
// OpenFGA "Invalid" pink (#FF5370) as the background so the deny
// chip is instantly distinguishable from allow.
func paintDenyChip(o opts) string {
	if !o.color {
		return markerDeny
	}
	return markerDenyChip + " " + markerDeny + " " + ansiReset
}

// paintObjectIdent renders an OpenFGA "type:id" identifier with the
// type portion coloured as a type name and the ":" delimiter dimmed.
// The id portion stays uncoloured so the terminal foreground shows
// through — matches how VS Code's semantic highlighting leaves free
// text alone when no scope claims it.
//
// Inputs without a ":" (defensive — shouldn't occur for real trace
// data) render uncoloured to avoid mangling unknown shapes.
func paintObjectIdent(o opts, s string) string {
	idx := strings.IndexByte(s, ':')
	if idx == -1 {
		return s
	}
	return paint(o, colorType, s[:idx]) + paint(o, colorKeyword, ":") + s[idx+1:]
}

// paintUsersetIdent renders an OpenFGA "type:id#relation" userset
// reference. Type / id styled as paintObjectIdent, the `#` styled as a
// keyword delimiter, the relation coloured cyan.
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
		paint(o, colorKeyword, "#") +
		paint(o, colorRelation, s[hash+1:])
}

// paintRelation is the styled wrapper for a bare relation name (used
// in headers and node labels where a relation appears without an
// object prefix).
func paintRelation(o opts, s string) string {
	return paint(o, colorRelation, s)
}

// paintKeyword styles a DSL-like keyword / connector token. Applies
// to prose words the renderer emits (has, on, does NOT have, via,
// union of, implied, direct) and single-char delimiters (→, ⇒).
func paintKeyword(o opts, s string) string {
	return paint(o, colorKeyword, s)
}

// paintTypeRestriction styles a `[type[, type]]` bracket group — used
// in Explain labels that reference userset patterns like
// "via [group#member] → group:eng".
func paintTypeRestriction(o opts, s string) string {
	return paint(o, colorTypeRestr, s)
}
