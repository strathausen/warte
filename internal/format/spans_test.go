package format

import (
	"reflect"
	"strings"
	"testing"

	"warte/internal/core"
)

func names(aci core.ACI) string {
	return map[core.ACI]string{"alice": "Alice", "bob": "@Bob"}[aci]
}

func TestSpans(t *testing.T) {
	B, I, S, M, P := core.StyleBold, core.StyleItalic, core.StyleStrikethrough, core.StyleMonospace, core.StyleSpoiler
	r := func(start, length int, st core.RangeStyle) core.BodyRange {
		return core.BodyRange{Start: start, Length: length, Style: st}
	}
	mention := func(start int, aci core.ACI) core.BodyRange {
		return core.BodyRange{Start: start, Length: 1, Style: core.StyleMention, MentionACI: aci}
	}
	tests := []struct {
		name   string
		body   string
		ranges []core.BodyRange
		want   []Span
	}{
		{"empty", "", nil, nil},
		{"plain", "hello", nil, []Span{{Text: "hello"}}},
		{"bold middle", "a bold b", []core.BodyRange{r(2, 4, B)}, []Span{{Text: "a "}, {Text: "bold", Bold: true}, {Text: " b"}}},
		{"nested", "abcdef", []core.BodyRange{r(0, 6, B), r(2, 2, I)}, []Span{
			{Text: "ab", Bold: true}, {Text: "cd", Bold: true, Italic: true}, {Text: "ef", Bold: true},
		}},
		{"overlapping", "abcdef", []core.BodyRange{r(0, 4, B), r(2, 4, S)}, []Span{
			{Text: "ab", Bold: true}, {Text: "cd", Bold: true, Strike: true}, {Text: "ef", Strike: true},
		}},
		{"all styles", "x", []core.BodyRange{r(0, 1, B), r(0, 1, I), r(0, 1, S), r(0, 1, M), r(0, 1, P)}, []Span{
			{Text: "x", Bold: true, Italic: true, Strike: true, Mono: true, Spoiler: true},
		}},
		{"duplicate ranges merge", "abc", []core.BodyRange{r(0, 2, B), r(1, 2, B)}, []Span{{Text: "abc", Bold: true}}},
		{"emoji offsets", thumbTone + " hi " + family, []core.BodyRange{r(5, 2, B), r(8, 8, I)}, []Span{
			{Text: thumbTone + " "}, {Text: "hi", Bold: true}, {Text: " "}, {Text: family, Italic: true},
		}},
		{"range splits surrogate", "a😀b", []core.BodyRange{r(2, 2, B)}, []Span{{Text: "a"}, {Text: "😀b", Bold: true}}},
		{"range splits zwj", family, []core.BodyRange{r(2, 1, B)}, []Span{{Text: family, Bold: true}}},
		{"range splits flag", flagDE + flagDE, []core.BodyRange{r(2, 4, B)}, []Span{{Text: flagDE + flagDE, Bold: true}}},
		{"out of range clamps", "abc", []core.BodyRange{r(1, 50, B), r(-4, 5, I), r(10, 2, S)}, []Span{
			{Text: "a", Italic: true}, {Text: "bc", Bold: true},
		}},
		{"mention", "hi ￼!", []core.BodyRange{mention(3, "alice")}, []Span{
			{Text: "hi "}, {Text: "@Alice", Mention: true, MentionACI: "alice"}, {Text: "!"},
		}},
		{"mention name with at", "￼", []core.BodyRange{mention(0, "bob")}, []Span{{Text: "@Bob", Mention: true, MentionACI: "bob"}}},
		{"unknown mention", "￼", []core.BodyRange{mention(0, "zed")}, []Span{{Text: "@Unknown", Mention: true, MentionACI: "zed"}}},
		{"adjacent mentions", "￼￼", []core.BodyRange{mention(0, "alice"), mention(1, "bob")}, []Span{
			{Text: "@Alice", Mention: true, MentionACI: "alice"}, {Text: "@Bob", Mention: true, MentionACI: "bob"},
		}},
		{"bold mention", "x ￼ y", []core.BodyRange{r(0, 5, B), mention(2, "alice")}, []Span{
			{Text: "x ", Bold: true}, {Text: "@Alice", Bold: true, Mention: true, MentionACI: "alice"}, {Text: " y", Bold: true},
		}},
		{"style edge inside mention ignored", "a￼b", []core.BodyRange{{Start: 1, Length: 1, Style: core.StyleMention, MentionACI: "alice"}, r(0, 2, B)}, []Span{
			{Text: "a", Bold: true}, {Text: "@Alice", Bold: true, Mention: true, MentionACI: "alice"}, {Text: "b"},
		}},
		{"stray placeholder kept", "a￼b", nil, []Span{{Text: "a￼b"}}},
		{"link", "see https://signal.org.", nil, []Span{
			{Text: "see "}, {Text: "https://signal.org", Link: "https://signal.org"}, {Text: "."},
		}},
		{"bold link split", "go www.example.com now", []core.BodyRange{r(0, 6, B)}, []Span{
			{Text: "go ", Bold: true}, {Text: "www", Bold: true, Link: "https://www.example.com"},
			{Text: ".example.com", Link: "https://www.example.com"}, {Text: " now"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Spans(tt.body, tt.ranges, names)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Spans(%q)\n got %+v\nwant %+v", tt.body, got, tt.want)
			}
		})
	}
}

func TestSpansNilResolver(t *testing.T) {
	got := Spans("￼", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "x"}}, nil)
	if len(got) != 1 || got[0].Text != "@Unknown" {
		t.Fatalf("got %+v", got)
	}
}

// TestSpansNeverPanics throws random ranges at tricky bodies and checks the
// spans reassemble the body (when there are no mentions).
func TestSpansNeverPanics(t *testing.T) {
	bodies := []string{"", "a", thumbTone, family + flagDE + "日本語", "a\xff\xfeb", "x́y", strings.Repeat("🇩🇪", 5)}
	for _, body := range bodies {
		n := UTF16Len(body)
		for start := -2; start <= n+2; start++ {
			for length := -1; length <= n+2; length++ {
				for _, st := range []core.RangeStyle{core.StyleBold, core.StyleSpoiler, core.RangeStyle(42)} {
					spans := Spans(body, []core.BodyRange{{Start: start, Length: length, Style: st}, {Start: 1, Length: 1, Style: core.StyleItalic}}, nil)
					if got := PlainText(spans); got != body {
						t.Fatalf("body %q range (%d,%d): reassembled %q", body, start, length, got)
					}
				}
			}
		}
	}
}
