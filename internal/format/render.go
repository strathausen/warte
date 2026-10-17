package format

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

// SpoilerRune is the character hidden spoiler text is drawn with.
const SpoilerRune = '░'

// Styles controls how Render draws spans.
type Styles struct {
	Bold        lipgloss.Style
	Italic      lipgloss.Style
	Strike      lipgloss.Style
	Mono        lipgloss.Style
	Mention     lipgloss.Style
	MentionSelf lipgloss.Style // used instead of Mention when MentionACI == SelfACI
	Link        lipgloss.Style
	Spoiler     lipgloss.Style // applied to the hidden '░' run

	// SelfACI identifies the local account, to highlight mentions of it.
	SelfACI core.ACI
	// RevealSpoilers shows spoiler text instead of '░' blocks.
	RevealSpoilers bool
	// NoHyperlinks disables OSC 8 hyperlinks around links.
	NoHyperlinks bool
}

// DefaultStyles returns a reasonable dark-terminal style set. The TUI theme
// usually supplies its own.
func DefaultStyles() Styles {
	accent := lipgloss.Color("#7AA2F7")
	return Styles{
		Bold:        lipgloss.NewStyle().Bold(true),
		Italic:      lipgloss.NewStyle().Italic(true),
		Strike:      lipgloss.NewStyle().Strikethrough(true),
		Mono:        lipgloss.NewStyle().Foreground(lipgloss.Color("#E0AF68")),
		Mention:     lipgloss.NewStyle().Bold(true).Foreground(accent),
		MentionSelf: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#1A1B26")).Background(accent),
		Link:        lipgloss.NewStyle().Underline(true).Foreground(accent),
		Spoiler:     lipgloss.NewStyle().Foreground(lipgloss.Color("#565F89")),
	}
}

// Render draws spans as an ANSI string. Styles combine (a bold mention in a
// link-free italic run gets all three); hidden spoilers are drawn as
// SpoilerRune repeated to the text's cell width, line by line; links are
// wrapped in OSC 8 hyperlinks (ESC ]8;;URL ESC \ … ESC ]8;; ESC \). Newlines
// in span text are preserved and never padded. Span text is passed through
// SanitizeDisplay, so untrusted message content cannot inject terminal
// escape sequences.
func Render(spans []Span, st Styles) string {
	var b strings.Builder
	for _, sp := range spans {
		text := SanitizeDisplay(sp.Text)
		if sp.Spoiler && !st.RevealSpoilers {
			writeLines(&b, text, func(line string) string {
				if line == "" {
					return ""
				}
				return st.Spoiler.Render(strings.Repeat(string(SpoilerRune), ansi.StringWidth(line)))
			})
			continue
		}
		style := lipgloss.NewStyle()
		if sp.Mention {
			if st.SelfACI != "" && sp.MentionACI == st.SelfACI {
				style = style.Inherit(st.MentionSelf)
			} else {
				style = style.Inherit(st.Mention)
			}
		}
		if sp.Link != "" {
			style = style.Inherit(st.Link)
		}
		if sp.Mono {
			style = style.Inherit(st.Mono)
		}
		if sp.Bold {
			style = style.Inherit(st.Bold)
		}
		if sp.Italic {
			style = style.Inherit(st.Italic)
		}
		if sp.Strike {
			style = style.Inherit(st.Strike)
		}
		link := sp.Link
		if st.NoHyperlinks {
			link = ""
		}
		writeLines(&b, text, func(line string) string {
			if line == "" {
				return ""
			}
			out := style.Render(line)
			if link != "" {
				out = Hyperlink(link, out)
			}
			return out
		})
	}
	return b.String()
}

// writeLines applies f to each "\n"-separated line of s and joins the
// results with "\n".
func writeLines(b *strings.Builder, s string, f func(string) string) {
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f(line))
	}
}

// Hyperlink wraps text in an OSC 8 hyperlink to url, terminated with ST.
// Control characters (C0, DEL, C1) and whitespace are removed from url so it
// cannot terminate the sequence early; text is written as given.
func Hyperlink(url, text string) string {
	url = strings.Map(func(r rune) rune {
		if r < 0x21 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == utf8.RuneError ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, url)
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// RenderBody is a convenience for Render(Spans(body, ranges, resolve), st).
func RenderBody(body string, ranges []core.BodyRange, resolve func(core.ACI) string, st Styles) string {
	return Render(Spans(body, ranges, resolve), st)
}
