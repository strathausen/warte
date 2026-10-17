package format

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"warte/internal/core"
)

const aliceACI = core.ACI("0b5c1f4e-9a7d-4c1e-8f3a-2d6b7e8f9a01")

func TestParse(t *testing.T) {
	B, I, S, M, P := core.StyleBold, core.StyleItalic, core.StyleStrikethrough, core.StyleMonospace, core.StyleSpoiler
	r := func(start, length int, st core.RangeStyle) core.BodyRange {
		return core.BodyRange{Start: start, Length: length, Style: st}
	}
	tests := []struct {
		in     string
		body   string
		ranges []core.BodyRange
	}{
		{"", "", nil},
		{"plain text", "plain text", nil},
		{"**bold**", "bold", []core.BodyRange{r(0, 4, B)}},
		{"*bold*", "bold", []core.BodyRange{r(0, 4, B)}},
		{"_it_ and __it__", "it and it", []core.BodyRange{r(0, 2, I), r(7, 2, I)}},
		{"~st~ ~~st~~", "st st", []core.BodyRange{r(0, 2, S), r(3, 2, S)}},
		{"||secret||", "secret", []core.BodyRange{r(0, 6, P)}},
		{"`co*d*e`", "co*d*e", []core.BodyRange{r(0, 6, M)}},
		{"```\nfunc main() {}\n```", "func main() {}", []core.BodyRange{r(0, 14, M)}},
		{"```a `b` c```", "a `b` c", []core.BodyRange{r(0, 7, M)}},
		{"a **b _c_ d** e", "a b c d e", []core.BodyRange{r(2, 5, B), r(4, 1, I)}},
		{"**a __b** c__", "a b c", []core.BodyRange{r(0, 3, B), r(2, 3, I)}},
		{"**x `y` z**", "x y z", []core.BodyRange{r(0, 5, B), r(2, 1, M)}},
		{"snake_case_name", "snake_case_name", nil},
		{"2*3*4", "2*3*4", nil},
		{"a * b * c", "a * b * c", nil},
		{"wo**rd**s", "words", []core.BodyRange{r(2, 2, B)}},
		{"in__tra__word", "intraword", []core.BodyRange{r(2, 3, I)}},
		{"_ not_", "_ not_", nil},
		{"**unclosed", "**unclosed", nil},
		{"****", "****", nil},
		{"``", "``", nil},
		{"```unclosed", "```unclosed", nil},
		{"`unclosed", "`unclosed", nil},
		{"| pipe || pipe", "| pipe || pipe", nil},
		{`\*not bold\*`, "*not bold*", nil},
		{`\\`, `\`, nil},
		{`a\b`, `a\b`, nil},
		{`\**bold**`, "**bold**", nil},
		{"**" + thumbTone + "**x", thumbTone + "x", []core.BodyRange{r(0, 4, B)}},
		{"_" + family + "_", family, []core.BodyRange{r(0, 8, I)}},
		{"hi @{" + string(aliceACI) + "}!", "hi \uFFFC!", []core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: aliceACI}}},
		{"@{" + strings.ToUpper(string(aliceACI)) + "}", "\uFFFC", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: aliceACI}}},
		{"@{nope}", "@{nope}", nil},
		{`\@{` + string(aliceACI) + "}", "@{" + string(aliceACI) + "}", nil},
		{"**@{" + string(aliceACI) + "}**", "\uFFFC", []core.BodyRange{r(0, 1, B), {Start: 0, Length: 1, Style: core.StyleMention, MentionACI: aliceACI}}},
		{"`@{" + string(aliceACI) + "}`", "@{" + string(aliceACI) + "}", []core.BodyRange{r(0, 39, M)}},
	}
	for _, tt := range tests {
		body, ranges := Parse(tt.in)
		if body != tt.body || !reflect.DeepEqual(ranges, tt.ranges) {
			t.Errorf("Parse(%q)\n got %q %+v\nwant %q %+v", tt.in, body, ranges, tt.body, tt.ranges)
		}
	}
}

func TestParseWithMentions(t *testing.T) {
	bob := core.ACI("bob")
	in := "hey @Alice and @Bob, `@Alice`"
	mentions := []MentionSpan{
		{Start: 4, End: 10, ACI: aliceACI},
		{Start: 15, End: 19, ACI: bob},
		{Start: 22, End: 28, ACI: bob}, // inside code → ignored by the tokenizer
		{Start: 5, End: 8, ACI: bob},   // overlaps the first → ignored
		{Start: -1, End: 3, ACI: bob},  // out of bounds → ignored
		{Start: 50, End: 60, ACI: bob}, // out of bounds → ignored
		{Start: 12, End: 12, ACI: bob}, // empty → ignored
	}
	body, ranges := ParseWithMentions(in, mentions)
	wantBody := "hey \uFFFC and \uFFFC, @Alice"
	want := []core.BodyRange{
		{Start: 4, Length: 1, Style: core.StyleMention, MentionACI: aliceACI},
		{Start: 10, Length: 1, Style: core.StyleMention, MentionACI: bob},
		{Start: 13, Length: 6, Style: core.StyleMonospace},
	}
	if body != wantBody || !reflect.DeepEqual(ranges, want) {
		t.Errorf("got %q %+v\nwant %q %+v", body, ranges, wantBody, want)
	}

	// A span splitting a multi-byte rune is rejected.
	body, ranges = ParseWithMentions("é", []MentionSpan{{Start: 1, End: 2, ACI: bob}})
	if body != "é" || ranges != nil {
		t.Errorf("mid-rune span: got %q %+v", body, ranges)
	}
}

func TestToMarkup(t *testing.T) {
	B, I, M, P := core.StyleBold, core.StyleItalic, core.StyleMonospace, core.StyleSpoiler
	r := func(start, length int, st core.RangeStyle) core.BodyRange {
		return core.BodyRange{Start: start, Length: length, Style: st}
	}
	tests := []struct {
		body   string
		ranges []core.BodyRange
		want   string
	}{
		{"plain", nil, "plain"},
		{"bold", []core.BodyRange{r(0, 4, B)}, "**bold**"},
		{"a b c", []core.BodyRange{r(0, 3, B), r(2, 3, I)}, "**a __b** c__"},
		{"snake_case and *star*", nil, `snake_case and \*star\*`},
		{"a|b || c", nil, `a|b \|\| c`},
		{"x`y", []core.BodyRange{r(0, 3, M)}, "```x`y```"},
		{"x\ny", []core.BodyRange{r(0, 3, M)}, "```\nx\ny\n```"},
		{"code", []core.BodyRange{r(0, 4, M)}, "`code`"},
		{"hide", []core.BodyRange{r(0, 4, P), r(0, 4, B)}, "||**hide**||"},
		{"\uFFFC!", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: aliceACI}}, "@{" + string(aliceACI) + "}!"},
		{`back\slash\`, nil, `back\slash\\`},
	}
	for _, tt := range tests {
		if got := ToMarkup(tt.body, tt.ranges); got != tt.want {
			t.Errorf("ToMarkup(%q, %+v) = %q, want %q", tt.body, tt.ranges, got, tt.want)
		}
	}
}

func TestToMarkupWithMentions(t *testing.T) {
	body := "hi \uFFFC and \uFFFC"
	ranges := []core.BodyRange{
		{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "alice"},
		{Start: 9, Length: 1, Style: core.StyleMention, MentionACI: "zed"},
		{Start: 0, Length: 4, Style: core.StyleBold},
	}
	markup, spans := ToMarkupWithMentions(body, ranges, names)
	if want := "**hi @Alice** and @Unknown"; markup != want {
		t.Fatalf("markup = %q, want %q", markup, want)
	}
	for _, s := range spans {
		if !strings.HasPrefix(markup[s.Start:s.End], "@") {
			t.Errorf("span %+v covers %q", s, markup[s.Start:s.End])
		}
	}
	gotBody, gotRanges := ParseWithMentions(markup, spans)
	if gotBody != body || !reflect.DeepEqual(gotRanges, NormalizeRanges(body, ranges)) {
		t.Errorf("round trip: %q %+v", gotBody, gotRanges)
	}
}

func TestRoundTripTable(t *testing.T) {
	inputs := []string{
		"**bold** _it_ ~st~ `mono` ||spoil||",
		"nested **bold _and it_ here**",
		"**a __b** c__ ~~d~~",
		"escapes: \\* \\_ \\~ \\| \\` \\\\ \\@{0b5c1f4e-9a7d-4c1e-8f3a-2d6b7e8f9a01}",
		"emoji **" + thumbTone + family + "** " + flagDE,
		"```\nmulti\nline\n```",
		"mention @{" + string(aliceACI) + "} in ||**spoiler**||",
		"snake_case_names stay",
		"trailing backslash \\",
	}
	for _, in := range inputs {
		body, ranges := Parse(in)
		md := ToMarkup(body, ranges)
		body2, ranges2 := Parse(md)
		if body2 != body || !reflect.DeepEqual(ranges2, ranges) {
			t.Errorf("round trip %q via %q:\n got %q %+v\nwant %q %+v", in, md, body2, ranges2, body, ranges)
		}
	}
}

// TestRoundTripRandom checks Parse(ToMarkup(x)) == x for many random bodies
// and normalized ranges.
func TestRoundTripRandom(t *testing.T) {
	alphabet := []string{"a", "b", "Z", "1", " ", " ", "\n", "*", "_", "~", "|", "`", "\\", "@", "{", "}", "é", "日", thumbTone, family, flagDE, "😀", ".", "-", "\uFFFC"}
	styles := []core.RangeStyle{core.StyleBold, core.StyleItalic, core.StyleStrikethrough, core.StyleMonospace, core.StyleSpoiler}
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 5000; iter++ {
		var sb strings.Builder
		n := rng.Intn(12)
		for i := 0; i < n; i++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		body := sb.String()
		units := UTF16Len(body)
		var ranges []core.BodyRange
		for k := rng.Intn(4); k > 0 && units > 0; k-- {
			s := rng.Intn(units)
			ranges = append(ranges, core.BodyRange{Start: s, Length: 1 + rng.Intn(units-s), Style: styles[rng.Intn(len(styles))]})
		}
		for i, r := range body {
			if r == MentionPlaceholder && rng.Intn(3) > 0 {
				ranges = append(ranges, core.BodyRange{Start: ByteToUTF16(body, i), Length: 1, Style: core.StyleMention, MentionACI: aliceACI})
			}
		}
		want := NormalizeRanges(body, ranges)
		if hasCodeFence(body, want) || mentionInMono(want) {
			continue // documented limitation
		}
		md := ToMarkup(body, want)
		gotBody, got := Parse(md)
		if gotBody != body || !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip failed\nbody %q ranges %+v\nmarkup %q\ngot %q %+v", body, want, md, gotBody, got)
		}
	}
}

func mentionInMono(ranges []core.BodyRange) bool {
	for _, m := range ranges {
		if m.Style != core.StyleMention {
			continue
		}
		for _, r := range ranges {
			if r.Style == core.StyleMonospace && r.Start <= m.Start && r.Start+r.Length >= m.Start+m.Length {
				return true
			}
		}
	}
	return false
}

func hasCodeFence(body string, ranges []core.BodyRange) bool {
	for _, r := range ranges {
		if r.Style == core.StyleMonospace {
			a, b := UTF16ToByte(body, r.Start), UTF16ToByte(body, r.Start+r.Length)
			if strings.Contains(body[a:b], "```") {
				return true
			}
		}
	}
	return false
}

func TestRoundTripMarkupStable(t *testing.T) {
	// ToMarkup(Parse(s)) is a fixed point after one iteration.
	inputs := []string{"*a* _b_ ~c~", "x**y**z", "`a` ``` b ```", "a_b_c d*e*f", "|| x ||"}
	for _, in := range inputs {
		m1 := ToMarkup(Parse(in))
		m2 := ToMarkup(Parse(m1))
		if m1 != m2 {
			t.Errorf("%q: %q != %q", in, m1, m2)
		}
	}
}
