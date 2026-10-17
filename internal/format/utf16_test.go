package format

import (
	"reflect"
	"testing"

	"warte/internal/core"
)

const (
	thumbTone = "👍🏽"    // U+1F44D U+1F3FD: 4 units, 8 bytes
	family    = "👨‍👩‍👧" // ZWJ sequence: 8 units, 18 bytes
	flagDE    = "🇩🇪"    // two regional indicators: 4 units, 8 bytes
)

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"é", 1},
		{"日本", 2},
		{"😀", 2},
		{thumbTone, 4},
		{family, 8},
		{flagDE, 4},
		{"❤️", 2},
		{"a\xffb", 3}, // invalid byte counts as one unit
	}
	for _, tt := range tests {
		if got := UTF16Len(tt.in); got != tt.want {
			t.Errorf("UTF16Len(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestUTF16ToByte(t *testing.T) {
	s := "a" + thumbTone + "b" + family + "c" + flagDE + "é"
	tests := []struct {
		off, want int
	}{
		{-5, 0},
		{0, 0},
		{1, 1},  // start of 👍
		{2, 1},  // inside surrogate pair → rounds down
		{3, 5},  // start of 🏽
		{4, 5},  // inside 🏽
		{5, 9},  // b
		{6, 10}, // start of family
		{14, 28},
		{15, 29}, // flag
		{19, 37}, // é
		{20, 39}, // end
		{999, 39},
	}
	for _, tt := range tests {
		if got := UTF16ToByte(s, tt.off); got != tt.want {
			t.Errorf("UTF16ToByte(%d) = %d, want %d", tt.off, got, tt.want)
		}
	}
}

func TestByteToUTF16(t *testing.T) {
	s := "a" + thumbTone + "b" + family + "é"
	tests := []struct {
		b, want int
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{3, 1}, // inside 👍 → rounds down
		{5, 3},
		{9, 5},
		{10, 6},
		{28, 14},
		{29, 14}, // inside é
		{30, 15},
		{500, 15},
	}
	for _, tt := range tests {
		if got := ByteToUTF16(s, tt.b); got != tt.want {
			t.Errorf("ByteToUTF16(%d) = %d, want %d", tt.b, got, tt.want)
		}
	}
	// Round trip on every rune boundary.
	for i := range s {
		if got := UTF16ToByte(s, ByteToUTF16(s, i)); got != i {
			t.Errorf("round trip byte %d → %d", i, got)
		}
	}
}

func TestNormalizeRanges(t *testing.T) {
	b, i := core.StyleBold, core.StyleItalic
	tests := []struct {
		name   string
		body   string
		ranges []core.BodyRange
		want   []core.BodyRange
	}{
		{"nil", "abc", nil, nil},
		{"empty body", "", []core.BodyRange{{Start: 0, Length: 2, Style: b}}, nil},
		{"clamp", "abc", []core.BodyRange{{Start: -2, Length: 10, Style: b}}, []core.BodyRange{{Start: 0, Length: 3, Style: b}}},
		{"drop zero and negative", "abc", []core.BodyRange{{Start: 1, Length: 0, Style: b}, {Start: 1, Length: -3, Style: b}}, nil},
		{"drop out of range", "abc", []core.BodyRange{{Start: 5, Length: 2, Style: b}}, nil},
		{"drop unknown style", "abc", []core.BodyRange{{Start: 0, Length: 2, Style: core.StyleNone}, {Start: 0, Length: 1, Style: 99}}, nil},
		{"merge overlapping", "abcdef", []core.BodyRange{{Start: 3, Length: 3, Style: b}, {Start: 0, Length: 4, Style: b}}, []core.BodyRange{{Start: 0, Length: 6, Style: b}}},
		{"merge adjacent", "abcdef", []core.BodyRange{{Start: 0, Length: 2, Style: b}, {Start: 2, Length: 2, Style: b}}, []core.BodyRange{{Start: 0, Length: 4, Style: b}}},
		{"different styles kept", "abcdef", []core.BodyRange{{Start: 2, Length: 2, Style: i}, {Start: 0, Length: 3, Style: b}}, []core.BodyRange{{Start: 0, Length: 3, Style: b}, {Start: 2, Length: 2, Style: i}}},
		{"surrogate half widened", "x" + thumbTone, []core.BodyRange{{Start: 2, Length: 1, Style: b}}, []core.BodyRange{{Start: 1, Length: 4, Style: b}}},
		{"zwj widened", family + "!", []core.BodyRange{{Start: 3, Length: 2, Style: b}}, []core.BodyRange{{Start: 0, Length: 8, Style: b}}},
		{"flag widened", flagDE, []core.BodyRange{{Start: 0, Length: 2, Style: b}}, []core.BodyRange{{Start: 0, Length: 4, Style: b}}},
		{"overflow length", "abc", []core.BodyRange{{Start: 1, Length: int(^uint(0) >> 1), Style: b}}, []core.BodyRange{{Start: 1, Length: 2, Style: b}}},
		{"overlapping mentions earliest wins", "￼￼", []core.BodyRange{
			{Start: 1, Length: 1, Style: core.StyleMention, MentionACI: "b"},
			{Start: 0, Length: 2, Style: core.StyleMention, MentionACI: "a"},
		}, []core.BodyRange{{Start: 0, Length: 2, Style: core.StyleMention, MentionACI: "a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeRanges(tt.body, tt.ranges)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
