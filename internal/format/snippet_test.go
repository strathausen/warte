package format

import (
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
)

func TestSnippet(t *testing.T) {
	mention := core.BodyRange{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: "alice"}
	tests := []struct {
		name   string
		body   string
		ranges []core.BodyRange
		max    int
		want   string
	}{
		{"plain", "hello", nil, 20, "hello"},
		{"collapse", "  hello\n\n  world\t!  ", nil, 0, "hello world !"},
		{"mention", "hi ￼!", []core.BodyRange{mention}, 0, "hi @Alice!"},
		{"spoiler", "the end: he dies", []core.BodyRange{{Start: 9, Length: 7, Style: core.StyleSpoiler}}, 0, "the end: ░░░░░░░"},
		{"spoiler newline", "a\nb", []core.BodyRange{{Start: 0, Length: 3, Style: core.StyleSpoiler}}, 0, "░░░"},
		{"truncate", "hello world", nil, 8, "hello w…"},
		{"truncate wide", "日本語のテキスト", nil, 7, "日本語…"},
		{"truncate emoji", family + family + family, nil, 5, family + family + "…"},
		{"exact fit", "hello", nil, 5, "hello"},
		{"formatting dropped", "bold", []core.BodyRange{{Start: 0, Length: 4, Style: core.StyleBold}}, 0, "bold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Snippet(tt.body, tt.ranges, names, tt.max)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if tt.max > 0 && ansi.StringWidth(got) > tt.max {
				t.Errorf("width %d > %d", ansi.StringWidth(got), tt.max)
			}
		})
	}
}
