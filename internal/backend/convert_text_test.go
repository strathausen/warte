package backend

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"

	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"

	"warte/internal/core"
)

func styleRange(start, length uint32, s signalpb.BodyRange_Style) *signalpb.BodyRange {
	return &signalpb.BodyRange{Start: proto.Uint32(start), Length: proto.Uint32(length), AssociatedValue: &signalpb.BodyRange_Style_{Style: s}}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"hello", 5},
		{"héllo", 5},
		{"😀", 2},
		{"a😀b", 4},
		{"￼", 1},
		{"日本", 2},
		{string([]byte{0xff}), 1},
	}
	for _, tt := range tests {
		if got := utf16Len(tt.in); got != tt.want {
			t.Errorf("utf16Len(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestRangesFromSignal(t *testing.T) {
	body := "hi ￼ 😀 bold"
	tests := []struct {
		name string
		in   []*signalpb.BodyRange
		want []core.BodyRange
	}{
		{"nil", nil, nil},
		{"bold", []*signalpb.BodyRange{styleRange(8, 4, signalpb.BodyRange_BOLD)},
			[]core.BodyRange{{Start: 8, Length: 4, Style: core.StyleBold}}},
		{"all styles", []*signalpb.BodyRange{
			styleRange(0, 1, signalpb.BodyRange_ITALIC),
			styleRange(0, 1, signalpb.BodyRange_SPOILER),
			styleRange(0, 1, signalpb.BodyRange_STRIKETHROUGH),
			styleRange(0, 1, signalpb.BodyRange_MONOSPACE),
		}, []core.BodyRange{
			{Start: 0, Length: 1, Style: core.StyleItalic},
			{Start: 0, Length: 1, Style: core.StyleSpoiler},
			{Start: 0, Length: 1, Style: core.StyleStrikethrough},
			{Start: 0, Length: 1, Style: core.StyleMonospace},
		}},
		{"mention string", []*signalpb.BodyRange{{Start: proto.Uint32(3), Length: proto.Uint32(1),
			AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: string(aliceACI)}}},
			[]core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: aliceACI}}},
		{"mention binary", []*signalpb.BodyRange{{Start: proto.Uint32(3), Length: proto.Uint32(1),
			AssociatedValue: &signalpb.BodyRange_MentionAciBinary{MentionAciBinary: bobUUID[:]}}},
			[]core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: bobACI}}},
		{"bad mention", []*signalpb.BodyRange{{Start: proto.Uint32(3), Length: proto.Uint32(1),
			AssociatedValue: &signalpb.BodyRange_MentionAci{MentionAci: "nope"}}}, nil},
		{"none style dropped", []*signalpb.BodyRange{styleRange(0, 2, signalpb.BodyRange_NONE)}, nil},
		{"zero length dropped", []*signalpb.BodyRange{styleRange(0, 0, signalpb.BodyRange_BOLD)}, nil},
		{"past end dropped", []*signalpb.BodyRange{styleRange(10, 10, signalpb.BodyRange_BOLD)}, nil},
		{"start past end dropped", []*signalpb.BodyRange{styleRange(12, 1, signalpb.BodyRange_BOLD)}, nil},
		{"exactly to end", []*signalpb.BodyRange{styleRange(11, 1, signalpb.BodyRange_BOLD)},
			[]core.BodyRange{{Start: 11, Length: 1, Style: core.StyleBold}}},
		{"no value dropped", []*signalpb.BodyRange{{Start: proto.Uint32(0), Length: proto.Uint32(1)}}, nil},
		{"nil entry skipped", []*signalpb.BodyRange{nil, styleRange(0, 1, signalpb.BodyRange_BOLD)},
			[]core.BodyRange{{Start: 0, Length: 1, Style: core.StyleBold}}},
		{"sorted by start", []*signalpb.BodyRange{styleRange(5, 1, signalpb.BodyRange_BOLD), styleRange(1, 1, signalpb.BodyRange_ITALIC)},
			[]core.BodyRange{{Start: 1, Length: 1, Style: core.StyleItalic}, {Start: 5, Length: 1, Style: core.StyleBold}}},
	}
	if n := utf16Len(body); n != 12 {
		t.Fatalf("test body length %d", n)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rangesFromSignal(body, tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRangesFromSignalCapsCount(t *testing.T) {
	body := "x"
	var in []*signalpb.BodyRange
	for range maxBodyRanges + 50 {
		in = append(in, styleRange(0, 1, signalpb.BodyRange_BOLD))
	}
	if got := rangesFromSignal(body, in); len(got) != maxBodyRanges {
		t.Errorf("kept %d ranges, want %d", len(got), maxBodyRanges)
	}
}

func TestRangesRoundTrip(t *testing.T) {
	body := "hey ￼ look *here* 😀"
	in := []core.BodyRange{
		{Start: 4, Length: 1, Style: core.StyleMention, MentionACI: aliceACI},
		{Start: 11, Length: 6, Style: core.StyleBold},
		{Start: 11, Length: 6, Style: core.StyleSpoiler},
		{Start: 0, Length: 3, Style: core.StyleMonospace},
	}
	wire := rangesToSignal(body, in)
	if len(wire) != len(in) {
		t.Fatalf("wire has %d ranges, want %d", len(wire), len(in))
	}
	got := rangesFromSignal(body, wire)
	want := []core.BodyRange{in[3], in[0], in[1], in[2]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestRangesToSignalDropsInvalid(t *testing.T) {
	body := "abc"
	in := []core.BodyRange{
		{Start: 0, Length: 0, Style: core.StyleBold},
		{Start: -1, Length: 1, Style: core.StyleBold},
		{Start: 2, Length: 5, Style: core.StyleBold},
		{Start: 0, Length: 1, Style: core.StyleNone},
		{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: "x"},
		{Start: 1, Length: 1, Style: core.StyleItalic},
	}
	got := rangesToSignal(body, in)
	if len(got) != 1 || got[0].GetStyle() != signalpb.BodyRange_ITALIC {
		t.Errorf("got %v, want only the italic range", got)
	}
}

func TestMentionsACI(t *testing.T) {
	rs := []core.BodyRange{{Style: core.StyleBold}, {Style: core.StyleMention, MentionACI: aliceACI}}
	if !mentionsACI(rs, aliceACI) {
		t.Error("expected alice to be mentioned")
	}
	if mentionsACI(rs, bobACI) || mentionsACI(rs, "") || mentionsACI(nil, aliceACI) {
		t.Error("unexpected mention")
	}
}
