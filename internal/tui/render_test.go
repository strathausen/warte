package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/fake"
)

func TestUntrustedTextIsSanitized(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>") // a group, so sender names are shown
	evil := "\x1b]52;c;cHduZWQ=\x07\x1b[2J\x1b]8;;http://evil\x1b\\click\u202e"
	m := core.Message{
		ID:         core.MessageID{Chat: h.a.conv.chat.ID, Author: fake.BobACI, Timestamp: uint64(testNow.UnixMilli())},
		SenderName: "Bob" + evil,
		SentAt:     testNow,
		Body:       "hi " + evil,
		Quote:      &core.Quote{AuthorName: "Q" + evil, Text: "quoted" + evil, OriginalFound: true},
		Attachments: []core.Attachment{{
			ID: "x", ContentType: "application/pdf", FileName: "report" + evil + ".pdf", Size: 1234,
		}},
		Reactions: []core.Reaction{{Emoji: "👍" + evil, From: fake.AliceACI}},
	}
	for _, flags := range []uint16{flagFirst | flagLast, flagFirst | flagIRC} {
		b := h.a.renderMessage(&m, 100, flags, "")
		out := strings.Join(b.lines, "\n")
		for _, bad := range []string{"\x1b]52", "\x1b[2J", "\u202e", "\x07"} {
			if strings.Contains(out, bad) {
				t.Fatalf("flags %b: rendered output contains %q:\n%q", flags, bad, out)
			}
		}
	}
	// Chat titles and snippets in the list.
	c := core.Chat{ID: "u:x", Title: "Eve" + evil, LastMessage: &core.MessagePreview{Text: "yo" + evil, At: testNow}}
	for _, l := range h.a.renderChatItem(&c, 30, false, true) {
		if strings.Contains(l, "\x1b]52") || strings.Contains(l, "\x1b[2J") {
			t.Fatalf("chat item leaks escapes: %q", l)
		}
	}
}

func TestLinkRanges(t *testing.T) {
	line := "ab " + "\x1b]8;;https://x.y\x1b\\" + "\x1b[4mlink\x1b[m" + "\x1b]8;;\x1b\\" + " 🎉 " + "\x1b]8;;mailto:a@b\x1b\\mail\x1b]8;;\x1b\\"
	got := linkRanges(line)
	if len(got) != 2 {
		t.Fatalf("ranges = %+v", got)
	}
	if got[0] != (linkRange{x0: 3, x1: 7, url: "https://x.y"}) {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].x0 != 11 || got[1].x1 != 15 || got[1].url != "mailto:a@b" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestSpliceKeepsWidth(t *testing.T) {
	bg := lipgloss.NewStyle().Bold(true).Render("hello wide 世界 world and more")
	for x := 0; x < 25; x++ {
		out := splice(bg, "[XX]", x)
		if w := width(out); w != width(bg) {
			t.Fatalf("x=%d: width %d, want %d: %q", x, w, width(bg), out)
		}
	}
}

func TestWithLineBg(t *testing.T) {
	line := "\x1b[38;2;0;0;0;48;2;10;0;0mA\x1b[m B \x1b[1mC\x1b[0m"
	out := withLineBg(line, lipgloss.Color("#123456"))
	// The explicit background of A survives; resets re-apply the band.
	if !strings.Contains(out, "48;2;10;0;0mA") {
		t.Fatalf("own background lost: %q", out)
	}
	if n := strings.Count(out, "48;2;18;52;86m"); n < 3 {
		t.Fatalf("band applied %d times: %q", n, out)
	}
	if width(out) != width(line) {
		t.Fatal("width changed")
	}
}

func TestFitAndBox(t *testing.T) {
	for _, w := range []int{1, 2, 5, 13} {
		if got := width(fit("abc 世界 def", w)); got != w {
			t.Fatalf("fit(%d) = %d", w, got)
		}
	}
	h := newHarness(t, 80, 24)
	lines := box(h.a.styles, []string{"a", "very long line that does not fit at all"}, 12, 5, true, "Title that is long", "foot")
	if len(lines) != 5 {
		t.Fatalf("box height %d", len(lines))
	}
	for _, l := range lines {
		if width(l) != 12 {
			t.Fatalf("box line width %d: %q", width(l), l)
		}
	}
}

func TestMessageKindsRender(t *testing.T) {
	h := newHarness(t, 120, 40)
	at := testNow.Add(-time.Hour)
	id := func(n uint64) core.MessageID {
		return core.MessageID{Chat: h.a.conv.chat.ID, Author: fake.AliceACI, Timestamp: uint64(at.UnixMilli()) + n}
	}
	msgs := []core.Message{
		{ID: id(1), Kind: core.KindSystem, SystemText: "Alice changed the group name", SentAt: at},
		{ID: id(2), Kind: core.KindCall, Call: &core.CallInfo{Missed: true, Video: true}, SentAt: at},
		{ID: id(3), RemoteDeleted: true, SentAt: at},
		{ID: id(4), ViewOnce: true, SentAt: at, Attachments: []core.Attachment{{ID: "v", ContentType: "image/jpeg"}}},
		{ID: id(5), Body: "secret", Ranges: []core.BodyRange{{Start: 0, Length: 6, Style: core.StyleSpoiler}}, SentAt: at},
		{ID: id(6), Poll: &core.Poll{Question: "Lunch?", Options: []core.PollOption{{Text: "Pizza", Voters: []core.ACI{h.a.self.ACI}}, {Text: "Salad"}}}, SentAt: at},
		{ID: id(7), Contact: &core.SharedContact{Name: "Max", Phones: []string{"+49 1"}}, SentAt: at},
		{ID: id(8), Attachments: []core.Attachment{{ID: "a", ContentType: "audio/aac", VoiceNote: true, Size: 4000}}, SentAt: at},
		{ID: id(9), Preview: &core.LinkPreview{URL: "https://example.com/a", Title: "Example", Description: "Desc"}, Body: "https://example.com/a", SentAt: at},
		{ID: id(10), Outgoing: true, Status: core.StatusFailed, Body: "oops", SentAt: at, Edited: true, ExpiresIn: time.Hour, ExpireStartedAt: at},
	}
	want := []string{"changed the group name", "Missed video call", "This message was deleted", "View once",
		"▒▒▒▒▒▒", "Lunch?", "Max", "3.9 KB", "Example", "edited"}
	for i := range msgs {
		for _, irc := range []uint16{0, flagIRC} {
			b := h.a.renderMessage(&msgs[i], 90, flagFirst|flagLast|irc, "")
			out := stripANSI(strings.Join(b.lines, "\n"))
			if !strings.Contains(out, want[i]) {
				t.Errorf("msg %d (irc=%v): %q not in\n%s", i, irc != 0, want[i], out)
			}
			for _, l := range b.lines {
				if width(l) > 90 {
					t.Errorf("msg %d: line too wide (%d)", i, width(l))
				}
			}
		}
	}
	// Revealed spoilers show their text.
	b := h.a.renderMessage(&msgs[4], 90, flagFirst|flagRevealed, "")
	if !strings.Contains(stripANSI(strings.Join(b.lines, "\n")), "secret") {
		t.Error("revealed spoiler hidden")
	}
}

// TestMessageTextIsNeverLost renders every demo message at several widths in
// both layouts and checks that no character of the visible text is dropped
// (e.g. by a line that is one cell wider than its column and gets cut).
func TestMessageTextIsNeverLost(t *testing.T) {
	h := newHarness(t, 160, 48)
	ctx := t.Context()
	chats, err := h.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	checked := 0
	for _, c := range chats {
		h.send(OpenChatForTest(c.ID))
		msgs, err := h.svc.Messages(ctx, c.ID, core.MessageID{}, 200)
		if err != nil {
			t.Fatal(err)
		}
		for i := range msgs {
			m := &msgs[i]
			if m.Kind != core.KindNormal || m.RemoteDeleted || m.ViewOnce || m.Sticker != nil {
				continue
			}
			spoiler := false
			for _, r := range m.Ranges {
				if r.Style == core.StyleSpoiler {
					spoiler = true
				}
			}
			if spoiler {
				continue
			}
			want := squash(clean(h.a.messageText(*m)))
			if want == "" {
				continue
			}
			for _, w := range []int{40, 63, 90, 131} {
				for _, irc := range []uint16{0, flagIRC} {
					b := h.a.renderMessage(m, w, flagFirst|flagLast|irc, "")
					// Strip the IRC gutter ("│") and wrap points before comparing.
					got := squash(strings.ReplaceAll(stripANSI(strings.Join(b.lines, "\n")), "│", ""))
					if !isSubsequence(want, got) {
						t.Errorf("%s w=%d irc=%v: text lost\nwant %q\ngot  %q", c.Title, w, irc != 0, want, got)
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no messages checked")
	}
}

// isSubsequence reports whether every rune of want appears in got in order.
func isSubsequence(want, got string) bool {
	g := []rune(got)
	j := 0
	for _, r := range want {
		for j < len(g) && g[j] != r {
			j++
		}
		if j == len(g) {
			return false
		}
		j++
	}
	return true
}
