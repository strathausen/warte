package format

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

func TestRenderPlainTextPreserved(t *testing.T) {
	spans := Spans("hello *world*\nline two", []core.BodyRange{{Start: 0, Length: 5, Style: core.StyleBold}}, nil)
	out := Render(spans, DefaultStyles())
	if got := ansi.Strip(out); got != "hello *world*\nline two" {
		t.Errorf("stripped = %q", got)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Error("expected SGR sequences")
	}
}

func TestRenderSpoiler(t *testing.T) {
	body := "a secret 日本 b"
	ranges := []core.BodyRange{{Start: 2, Length: 9, Style: core.StyleSpoiler}}
	st := Styles{}
	got := ansi.Strip(RenderBody(body, ranges, nil, st))
	if want := "a " + strings.Repeat("░", ansi.StringWidth("secret 日本")) + " b"; got != want {
		t.Errorf("hidden = %q, want %q", got, want)
	}
	st.RevealSpoilers = true
	if got := ansi.Strip(RenderBody(body, ranges, nil, st)); got != body {
		t.Errorf("revealed = %q", got)
	}
	// Multi-line spoilers keep line structure.
	got = ansi.Strip(RenderBody("ab\ncd", []core.BodyRange{{Start: 0, Length: 5, Style: core.StyleSpoiler}}, nil, Styles{}))
	if got != "░░\n░░" {
		t.Errorf("multiline spoiler = %q", got)
	}
	// Links inside hidden spoilers are not exposed.
	out := RenderBody("https://x.io", []core.BodyRange{{Start: 0, Length: 12, Style: core.StyleSpoiler}}, nil, Styles{})
	if strings.Contains(out, "x.io") {
		t.Errorf("spoiler leaked link: %q", out)
	}
}

func TestRenderLinkHyperlink(t *testing.T) {
	out := RenderBody("see https://signal.org ok", nil, nil, Styles{})
	want := "see \x1b]8;;https://signal.org\x1b\\https://signal.org\x1b]8;;\x1b\\ ok"
	if out != want {
		t.Errorf("got %q\nwant %q", out, want)
	}
	out = RenderBody("see https://signal.org", nil, nil, Styles{NoHyperlinks: true})
	if out != "see https://signal.org" {
		t.Errorf("no hyperlinks: %q", out)
	}
	if got := Hyperlink("https://a\x1b]b", "t"); strings.Count(got, "\x1b") != 4 {
		t.Errorf("control chars not stripped from URL: %q", got)
	}
}

func TestRenderMentionSelf(t *testing.T) {
	st := Styles{
		Mention:     lipgloss.NewStyle().Underline(true),
		MentionSelf: lipgloss.NewStyle().Reverse(true),
		SelfACI:     "me",
	}
	body := "￼ ￼"
	ranges := []core.BodyRange{
		{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "me"},
		{Start: 2, Length: 1, Style: core.StyleMention, MentionACI: "alice"},
	}
	out := RenderBody(body, ranges, names, st)
	self := lipgloss.NewStyle().Reverse(true).Render("@Unknown")
	other := lipgloss.NewStyle().Underline(true).Render("@Alice")
	if out != self+" "+other {
		t.Errorf("got %q", out)
	}
}

func TestRenderNoPadding(t *testing.T) {
	out := RenderBody("a\nlonger line", []core.BodyRange{{Start: 0, Length: 13, Style: core.StyleBold}}, nil, DefaultStyles())
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		if strings.HasSuffix(l, " ") {
			t.Errorf("line padded: %q", l)
		}
	}
}
