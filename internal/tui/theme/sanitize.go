package theme

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize makes untrusted text (message bodies, names, group titles) safe to
// hand to a lipgloss style: it removes ANSI/OSC/DCS escape sequences, C0 and
// C1 control characters (keeping '\n' and '\t'), Unicode bidi overrides and
// isolates (which can visually reorder surrounding UI), and replaces invalid
// UTF-8 with U+FFFD. Line and paragraph separators become newlines, soft
// hyphens are removed and keycap emoji lose their U+FE0F, so layout widths
// match what terminals draw. Styles in this package never sanitize message content on
// their own; the TUI must call Sanitize (or SanitizeLine) on anything that did
// not originate from the program itself.
func Sanitize(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = ansi.Strip(s)
	if strings.Contains(s, "\ufe0f\u20e3") {
		// Keycap emoji: the ANSI library's width and truncation functions
		// disagree on "1\uFE0F\u20E3", which shifts borders when a line is
		// cut. Without the variation selector both agree on one cell.
		s = strings.ReplaceAll(s, "\ufe0f\u20e3", "\u20e3")
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\u2028' || r == '\u2029':
			return '\n'
		case r == '\u00ad':
			return -1
		case unicode.IsControl(r), isBidiControl(r):
			return -1
		}
		return r
	}, s)
}

// SanitizeLine is Sanitize for single-line contexts (list rows, dividers,
// titles): newlines and tabs become spaces.
func SanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, Sanitize(s))
}

func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r == 0x200E || r == 0x200F || r == 0x061C: // LRM, RLM, ALM
		return true
	}
	return false
}
