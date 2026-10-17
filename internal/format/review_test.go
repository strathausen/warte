package format

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

func TestRenderStripsEscapeInjection(t *testing.T) {
	evil := func(core.ACI) string { return "\x1b]0;pwned\x07Eve\nBob" }
	body := "hi \x1b[2J\x1b]52;c;ZXZpbA==\x07 \u009b31m\r\u202eevil\u2066 ￼"
	ranges := []core.BodyRange{{Start: UTF16Len(body) - 1, Length: 1, Style: core.StyleMention, MentionACI: "a"}}
	for _, st := range []Styles{DefaultStyles(), {}} {
		out := Render(Spans(body, ranges, evil), st)
		for _, bad := range []string{"\x1b[2J", "\x1b]52;", "\x1b]0;", "\x07", "\u009b", "\r", "\u202e", "\u2066", "\n"} {
			if strings.Contains(out, bad) {
				t.Errorf("Render output %q contains %q", out, bad)
			}
		}
		if !strings.Contains(out, "@") || !strings.Contains(ansi.Strip(out), "Eve Bob") {
			t.Errorf("mention name not rendered sanitized: %q", out)
		}
	}
}

func TestSanitizeDisplay(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain 👍🏽 日本", "plain 👍🏽 日本"},
		{"a\nb", "a\nb"},
		{"a\tb", "a    b"},
		{"\x1b[31mred\x1b[m", "[31mred[m"},
		{"x\u0085y\u009cz\x7f", "xyz"},
		{"\u202eabc\u202c\u2067d\u2069", "abcd"},
		{"\u200f\u200e\u200d", "\u200f\u200e\u200d"}, // marks and ZWJ are harmless
		{"bad\xffutf8", "bad�utf8"},
	}
	for _, tt := range tests {
		if got := SanitizeDisplay(tt.in); got != tt.want {
			t.Errorf("SanitizeDisplay(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHyperlinkStripsControls(t *testing.T) {
	got := Hyperlink("https://x.y/\x1b\\\x07a b\u009cc", "t")
	if got != "\x1b]8;;https://x.y/\\ab c\x1b\\t\x1b]8;;\x1b\\" && got != "\x1b]8;;https://x.y/\\abc\x1b\\t\x1b]8;;\x1b\\" {
		t.Errorf("Hyperlink = %q", got)
	}
	if strings.Count(got, "\x1b") != 4 {
		t.Errorf("Hyperlink = %q: extra ESC", got)
	}
}

func TestSnippetSanitizes(t *testing.T) {
	got := Snippet("a\x1b]52;c;x\x07b\u202ec\tdone", nil, nil, 0)
	if strings.ContainsAny(got, "\x1b\x07\u202e\t") {
		t.Errorf("Snippet = %q", got)
	}
}

// A mention range must cover exactly its placeholder even when the
// placeholder starts a longer grapheme cluster (here: a combining mark).
func TestMentionNotWidenedToGrapheme(t *testing.T) {
	body := "￼ޮ!"
	ranges := []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "0b5c1f4e-9a7d-4c1e-8f3a-2d6b7e8f9a01"}}
	if got := NormalizeRanges(body, ranges); !reflect.DeepEqual(got, ranges) {
		t.Fatalf("NormalizeRanges = %+v", got)
	}
	gotBody, gotRanges := Parse(ToMarkup(body, ranges))
	if gotBody != body || !reflect.DeepEqual(gotRanges, ranges) {
		t.Fatalf("round trip = %q %+v", gotBody, gotRanges)
	}
	if pt := PlainText(Spans(body, ranges, func(core.ACI) string { return "Ann" })); pt != "@Annޮ!" {
		t.Errorf("PlainText = %q", pt)
	}
}

func TestMonoWithTripleBackticksRoundTrip(t *testing.T) {
	for _, body := range []string{"```", "a```b", "``````", "x````y", "`", "a\n```\nb", "```go\nfmt```"} {
		ranges := []core.BodyRange{{Start: 0, Length: UTF16Len(body), Style: core.StyleMonospace}}
		m := ToMarkup(body, ranges)
		gotBody, gotRanges := Parse(m)
		if gotBody != body || !reflect.DeepEqual(gotRanges, ranges) {
			t.Errorf("round trip of mono %q via %q = %q %+v", body, m, gotBody, gotRanges)
		}
	}
}

func TestSpansManyRangesIsFast(t *testing.T) {
	var b strings.Builder
	var ranges []core.BodyRange
	for i := 0; i < 20000; i++ {
		ranges = append(ranges, core.BodyRange{Start: b.Len(), Length: 1, Style: core.RangeStyle(1 + i%5)})
		b.WriteString("a ")
	}
	body := b.String()
	start := time.Now()
	spans := Spans(body, ranges, nil)
	if d := time.Since(start); d > perfBudget {
		t.Errorf("Spans with %d ranges took %v", len(ranges), d)
	}
	if PlainText(spans) != body {
		t.Error("PlainText mismatch")
	}
}

func TestFindLinksManyClosingParensIsFast(t *testing.T) {
	text := "see http://x.com/" + strings.Repeat(")", 100000)
	start := time.Now()
	links := FindLinks(text)
	if d := time.Since(start); d > perfBudget {
		t.Errorf("FindLinks took %v", d)
	}
	if len(links) != 1 || links[0].Text != "http://x.com/" {
		t.Errorf("FindLinks = %+v", links)
	}
}

func TestSpansStyleSweepMatchesOverlap(t *testing.T) {
	body := "abcdefghij"
	ranges := []core.BodyRange{
		{Start: 0, Length: 6, Style: core.StyleBold},
		{Start: 3, Length: 5, Style: core.StyleItalic},
		{Start: 6, Length: 4, Style: core.StyleBold},
		{Start: 5, Length: 1, Style: core.StyleSpoiler},
	}
	got := Spans(body, ranges, nil)
	want := []Span{
		{Text: "abc", Bold: true},
		{Text: "de", Bold: true, Italic: true},
		{Text: "f", Bold: true, Italic: true, Spoiler: true},
		{Text: "gh", Bold: true, Italic: true},
		{Text: "ij", Bold: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Spans = %+v\nwant %+v", got, want)
	}
}

func TestWrapLeadingSpaceNeverExceedsWidth(t *testing.T) {
	for _, s := range []string{" 00000", "  abcde", "x \x1b[1m00000\x1b[m", " 日本語"} {
		for w := 2; w <= 6; w++ {
			lines := Wrap(s, w)
			for _, l := range lines {
				if n := ansi.StringWidth(l); n > w {
					t.Errorf("Wrap(%q, %d) line %q width %d", s, w, l, n)
				}
			}
			if got, want := strings.ReplaceAll(ansi.Strip(strings.Join(lines, "")), " ", ""), strings.ReplaceAll(ansi.Strip(s), " ", ""); got != want {
				t.Errorf("Wrap(%q, %d) lost text: %q", s, w, got)
			}
		}
	}
}
