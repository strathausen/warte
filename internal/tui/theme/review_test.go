package theme

import (
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTypingFrameExtremes(t *testing.T) {
	ic := Icons(true)
	for _, n := range []int{math.MinInt, math.MaxInt, -7, 0} {
		if got := ic.TypingFrame(n); got == "" {
			t.Errorf("TypingFrame(%d) empty", n)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain":                     "plain",
		"a\x1b[31mred\x1b[0m":       "ared",
		"x\x1b]8;;http://evil\x07y": "xy",
		"bell\x07 nul\x00 del\x7f":  "bell nul del",
		"c1\u009b31m":               "c131m",
		"rtl‮evil‬⁦i⁩":              "rtlevili",
		"keep\nlines\tand tabs":     "keep\nlines\tand tabs",
		"bad\xffutf8":               "bad�utf8",
		"emoji 🎉 ünï":               "emoji 🎉 ünï",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SanitizeLine("a\nb\tc\x1b[2J"); got != "a b c" {
		t.Errorf("SanitizeLine = %q", got)
	}
}

func TestDividerAndHintsStripEscapes(t *testing.T) {
	for _, th := range allThemes(t) {
		s := NewStyles(th)
		out := ansi.Strip(s.RenderDayDivider("Mon\x1b]52;c;ZXZpbA==\x07\nday", 30))
		if strings.Contains(out, "\n") || strings.Contains(out, "52;") || !strings.Contains(out, "Mon day") {
			t.Errorf("%s: day divider not sanitized: %q", th.Name, out)
		}
		if w := ansi.StringWidth(out); w != 30 {
			t.Errorf("%s: divider width %d", th.Name, w)
		}
		h := s.RenderKeyHint("q\x1b[2J", "quit")
		if strings.Contains(h, "[2J") {
			t.Errorf("%s: key hint leaked escape: %q", th.Name, h)
		}
	}
}

func TestRenderSpoilerHidesText(t *testing.T) {
	for _, th := range allThemes(t) {
		s := NewStyles(th)
		secret := "top secret 漢字\nline2"
		out := s.RenderSpoiler(secret)
		plain := ansi.Strip(out)
		if strings.ContainsAny(plain, "tops漢字lin") {
			t.Errorf("%s: spoiler leaks text: %q", th.Name, plain)
		}
		lines := strings.Split(plain, "\n")
		if len(lines) != 2 || ansi.StringWidth(lines[0]) != ansi.StringWidth("top secret 漢字") || ansi.StringWidth(lines[1]) != 5 {
			t.Errorf("%s: spoiler shape wrong: %q", th.Name, plain)
		}
		if s.RenderSpoiler("") != s.Spoiler.Render("") {
			t.Errorf("%s: empty spoiler", th.Name)
		}
	}
}
