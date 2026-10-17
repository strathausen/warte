package notify

import (
	"strings"
	"unicode/utf8"
)

// Maximum lengths, in runes, applied to notification text before delivery.
const (
	MaxTitleRunes = 128
	MaxBodyRunes  = 512
)

// Sanitize makes untrusted text safe to embed in a terminal escape sequence
// or hand to a notification daemon.
//
// It removes every C0 control character (including ESC and BEL), DEL, every
// C1 control character (U+0080–U+009F, which 8-bit terminals interpret as
// CSI, OSC, ST, …), invalid UTF-8 bytes, and Unicode bidirectional
// override/isolate characters that could be used to spoof the displayed
// text. Line breaks and tabs become spaces, runs of whitespace collapse to a
// single space, and the result is trimmed. If maxRunes > 0 the result is
// truncated to at most maxRunes runes, ending in an ellipsis when cut.
func Sanitize(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size <= 1:
			continue // invalid byte: may be a raw 8-bit C1 control
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f' || r == ' ' ||
			r == '\u00a0' || r == '\u2028' || r == '\u2029' || r == '\u0085':
			space = b.Len() > 0
			continue
		case isControl(r) || isBidiControl(r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return truncate(b.String(), maxRunes)
}

// isControl reports C0, DEL and C1 control characters.
func isControl(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

// isBidiControl reports explicit bidirectional formatting characters.
func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e: // LRE, RLE, PDF, LRO, RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI, RLI, FSI, PDI
		return true
	case r == 0x200e || r == 0x200f || r == 0x061c: // LRM, RLM, ALM
		return true
	}
	return false
}

func truncate(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	n := 0
	for i := range s {
		if n == maxRunes-1 {
			return strings.TrimRight(s[:i], " ") + "…"
		}
		n++
	}
	return s
}

// sanitizeField sanitizes s and additionally replaces sep, which would
// terminate a field in a ';'-separated escape-sequence payload.
func sanitizeField(s string, maxRunes int, sep string) string {
	s = Sanitize(s, maxRunes)
	if sep != "" {
		s = strings.ReplaceAll(s, sep, ",")
	}
	return s
}
