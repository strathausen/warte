package format

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

// Snippet returns a single-line plain-text preview of a message body for the
// chat list: mentions resolved through resolve (may be nil), spoilers hidden
// as SpoilerRune blocks, all whitespace runs (including newlines) collapsed to
// one space, and the result truncated with "…" to at most maxCells terminal
// cells. maxCells <= 0 means no limit.
func Snippet(body string, ranges []core.BodyRange, resolve func(core.ACI) string, maxCells int) string {
	var b strings.Builder
	for _, sp := range Spans(body, ranges, resolve) {
		text := collapseSpace(SanitizeDisplay(sp.Text))
		if sp.Spoiler {
			text = strings.Repeat(string(SpoilerRune), ansi.StringWidth(text))
		}
		b.WriteString(text)
	}
	s := strings.TrimSpace(collapseSpace(b.String()))
	return Truncate(s, maxCells)
}

// Truncate shortens plain or ANSI text to at most maxCells terminal cells,
// ending in "…" when shortened. maxCells <= 0 means no limit.
func Truncate(s string, maxCells int) string {
	if maxCells <= 0 || ansi.StringWidth(s) <= maxCells {
		return s
	}
	return ansi.Truncate(s, maxCells, "…")
}

// collapseSpace replaces each run of whitespace and control characters with
// a single space.
func collapseSpace(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || (unicode.IsControl(r) && r != '\u200d') {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}
