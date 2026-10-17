package format

import (
	"strings"
	"unicode/utf8"
)

// TabWidth is the number of spaces a tab in message text is expanded to by
// SanitizeDisplay, so cell widths stay predictable for wrapping.
const TabWidth = 4

// SanitizeDisplay makes untrusted message text safe to write to a terminal.
// Message bodies, names and URLs come from other people, so escape
// sequences in them could otherwise clear the screen, set the window title,
// write the clipboard (OSC 52) or forge hyperlinks. It:
//
//   - removes C0 controls (including ESC, BEL and CR) except newline, and
//     DEL and C1 controls (U+0080–U+009F, which some terminals treat as
//     8-bit CSI/OSC/ST);
//   - expands tabs to TabWidth spaces;
//   - removes bidi embedding, override and isolate controls
//     (U+202A–U+202E, U+2066–U+2069), which can reorder the rest of a line;
//   - turns the Unicode line and paragraph separators (U+2028, U+2029) into
//     newlines and removes soft hyphens (U+00AD), whose cell width differs
//     between terminals and the layout code;
//   - drops the U+FE0F in keycap sequences ("1\uFE0F\u20E3" becomes
//     "1\u20E3"): the cell-width and truncation functions of the ANSI
//     library disagree on their width, which shifts borders when a line
//     holding one is cut;
//   - replaces invalid UTF-8 with U+FFFD.
//
// Newlines are kept; callers that need a single line should collapse them.
func SanitizeDisplay(s string) string {
	clean := true
	for i, r := range s {
		if r == utf8.RuneError {
			if _, size := utf8.DecodeRuneInString(s[i:]); size <= 1 {
				clean = false
				break
			}
		}
		if unsafeRune(r) || r == '\u2028' || r == '\u2029' || r == '\u00ad' || r == '\u20e3' {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r == '\t':
			b.WriteString(strings.Repeat(" ", TabWidth))
		case r == '\u2028' || r == '\u2029':
			b.WriteByte('\n')
		case r == '\u00ad':
		case r == '\ufe0f' && strings.HasPrefix(s[i+len("\ufe0f"):], "\u20e3"):
		case unsafeRune(r):
		default:
			b.WriteRune(r) // invalid bytes decode to and are written as U+FFFD
		}
	}
	return b.String()
}

// sanitizeLine is SanitizeDisplay for single-line text such as display
// names: newlines become spaces as well.
func sanitizeLine(s string) string {
	return strings.ReplaceAll(SanitizeDisplay(strings.ReplaceAll(s, "\r\n", " ")), "\n", " ")
}

// unsafeRune reports whether r must not reach the terminal verbatim
// (newline excluded).
func unsafeRune(r rune) bool {
	switch {
	case r == '\n':
		return false
	case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
