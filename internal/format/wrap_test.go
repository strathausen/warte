package format

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

func TestWrapWidths(t *testing.T) {
	inputs := []string{
		"",
		"short",
		"the quick brown fox jumps over the lazy dog",
		"see https://example.com/a/very/long/path/that/never/ends/at/all?with=query&and=more ok",
		thumbTone + family + flagDE + "😀😀😀 日本語のテキストです 한국어 텍스트",
		"line one\nline two is longer than the width\n\nafter blank",
		"\x1b[1mbold text that is fairly long\x1b[m and plain text after it",
		Hyperlink("https://signal.org", "a link with several words in it") + " tail",
		"áb́ćd́éf́ǵh́",
		"tabs\tand\tspaces    many     here",
	}
	for _, in := range inputs {
		for width := 2; width <= 40; width++ {
			lines := Wrap(in, width)
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > width {
					t.Fatalf("Wrap(%q, %d): line %q has width %d", in, width, l, w)
				}
			}
			// No visible characters are lost (wrapping may drop break spaces).
			strip := func(s string) string {
				return strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(ansi.Strip(s))
			}
			if got, want := strip(strings.Join(lines, "")), strip(in); got != want {
				t.Fatalf("Wrap(%q, %d) lost text: %q vs %q", in, width, got, want)
			}
		}
	}
}

func TestWrapGraphemesIntact(t *testing.T) {
	in := strings.Repeat(family, 5)
	for _, l := range Wrap(in, 5) {
		if strings.Count(l, family)*len(family) != len(l) {
			t.Fatalf("grapheme split: %q", l)
		}
	}
}

func TestWrapCarriesStyles(t *testing.T) {
	in := "\x1b[1;31mred bold words here\x1b[m done"
	lines := Wrap(in, 8)
	if len(lines) < 3 {
		t.Fatalf("lines = %q", lines)
	}
	for i, l := range lines[:len(lines)-1] {
		if !strings.HasPrefix(l, "\x1b[1;31m") {
			t.Errorf("line %d does not start styled: %q", i, l)
		}
		if strings.Contains(ansi.Strip(l), "words") && !strings.HasSuffix(l, "\x1b[m") {
			t.Errorf("line %d not reset: %q", i, l)
		}
	}
	last := lines[len(lines)-1]
	if strings.Contains(last, "\x1b[1;31m") {
		t.Errorf("style leaked past reset: %q", last)
	}
}

func TestWrapCarriesHyperlinks(t *testing.T) {
	in := Hyperlink("https://signal.org", "one two three four") + " end"
	lines := Wrap(in, 6)
	for i, l := range lines {
		text := ansi.Strip(l)
		inLink := text != "end" && text != ""
		opened := strings.Contains(l, "\x1b]8;;https://signal.org")
		if inLink && !opened {
			t.Errorf("line %d %q lacks hyperlink", i, l)
		}
		if inLink && !strings.HasSuffix(l, "\x1b]8;;\x1b\\") {
			t.Errorf("line %d %q does not close hyperlink", i, l)
		}
		if text == "end" && opened {
			t.Errorf("hyperlink leaked into %q", l)
		}
	}
}

func TestWrapSpans(t *testing.T) {
	spans := Spans("hello world, this is **bold**", []core.BodyRange{{Start: 6, Length: 5, Style: core.StyleBold}}, nil)
	lines := WrapSpans(spans, DefaultStyles(), 10)
	if len(lines) < 3 {
		t.Fatalf("lines = %q", lines)
	}
	if Wrap("abc", 0)[0] != "a" {
		t.Errorf("width 0 treated as 1: %q", Wrap("abc", 0))
	}
}
