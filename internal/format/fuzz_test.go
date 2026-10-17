package format

import (
	"github.com/charmbracelet/x/ansi"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"warte/internal/core"
)

func FuzzParse(f *testing.F) {
	for _, s := range []string{"**a** _b_ ~c~ `d` ||e||", "```\nx\n```", "\\*", "@{0b5c1f4e-9a7d-4c1e-8f3a-2d6b7e8f9a01}", "a_b*c~d|e", "\xff**\xfe**"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		body, ranges := Parse(in)
		n := UTF16Len(body)
		for _, r := range ranges {
			if r.Start < 0 || r.Length <= 0 || r.Start+r.Length > n {
				t.Fatalf("Parse(%q): range %+v outside body of %d units", in, r, n)
			}
		}
		_ = Spans(body, ranges, nil)
		_ = Wrap(Render(Spans(body, ranges, nil), DefaultStyles()), 7)
		_ = Snippet(body, ranges, nil, 10)
	})
}

func FuzzSpans(f *testing.F) {
	f.Add("a👍🏽b", 1, 2, 3, 5)
	f.Add("￼", 0, 1, -1, 4)
	f.Fuzz(func(t *testing.T, body string, s1, l1, s2, l2 int) {
		ranges := []core.BodyRange{
			{Start: s1, Length: l1, Style: core.StyleBold},
			{Start: s2, Length: l2, Style: core.StyleMention, MentionACI: "x"},
			{Start: s2, Length: l1, Style: core.StyleSpoiler},
		}
		_ = Spans(body, ranges, nil)
		_ = ToMarkup(body, ranges)
	})
}

func FuzzMarkupRoundTrip(f *testing.F) {
	f.Add("hello *world* a_b", 0, 5, 3, 4, 1)
	f.Add("x￼y `z` ||w||", 1, 1, 0, 3, 3)
	f.Add("👨‍👩‍👧 ~~s~~ \\", 0, 8, 2, 6, 5)
	f.Fuzz(func(t *testing.T, body string, s1, l1, s2, l2, st int) {
		if !utf8.ValidString(body) {
			return
		}
		styles := []core.RangeStyle{core.StyleBold, core.StyleItalic, core.StyleSpoiler, core.StyleStrikethrough, core.StyleMonospace}
		style := styles[(st%len(styles)+len(styles))%len(styles)]
		ranges := []core.BodyRange{
			{Start: s1, Length: l1, Style: core.StyleBold},
			{Start: s2, Length: l2, Style: style},
		}
		// A mention inside monospace is a documented ToMarkup limitation.
		if i := strings.IndexRune(body, MentionPlaceholder); i >= 0 && style != core.StyleMonospace {
			ranges = append(ranges, core.BodyRange{Start: ByteToUTF16(body, i), Length: 1, Style: core.StyleMention, MentionACI: "0b5c1f4e-9a7d-4c1e-8f3a-2d6b7e8f9a01"})
		}
		want := NormalizeRanges(body, ranges)
		m := ToMarkup(body, ranges)
		gotBody, gotRanges := Parse(m)
		if gotBody != body || !reflect.DeepEqual(gotRanges, want) {
			t.Fatalf("round trip of %q %+v via %q:\n got %q %+v\nwant %q %+v", body, ranges, m, gotBody, gotRanges, body, want)
		}
	})
}

func FuzzWrapWidth(f *testing.F) {
	f.Add("hello 日本語 👨‍👩‍👧 https://example.com/very/long/path", 5)
	f.Add("a\tb\x1b[31mred\x1b[m", 3)
	f.Fuzz(func(t *testing.T, s string, w int) {
		if w < 2 || w > 200 {
			return
		}
		out := Render(Spans(s, nil, nil), DefaultStyles())
		for _, l := range Wrap(out, w) {
			if n := ansi.StringWidth(l); n > w {
				t.Fatalf("Wrap(%q, %d): line %q has width %d", out, w, l, n)
			}
		}
	})
}
