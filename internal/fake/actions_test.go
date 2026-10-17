package fake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
)

func lastOf(t *testing.T, s *Service, chat core.ChatID) core.Message {
	t.Helper()
	ms := allMessages(t, s, chat)
	if len(ms) == 0 {
		t.Fatalf("%s is empty", chat)
	}
	return ms[len(ms)-1]
}

func findMsg(t *testing.T, s *Service, chat core.ChatID, pred func(core.Message) bool) core.Message {
	t.Helper()
	for _, m := range allMessages(t, s, chat) {
		if pred(m) {
			return m
		}
	}
	t.Fatalf("no matching message in %s", chat)
	return core.Message{}
}

func TestSendStatusProgression(t *testing.T) {
	tests := []struct {
		name  string
		chat  core.ChatID
		final core.DeliveryStatus
	}{
		{"direct", ChatAlice, core.StatusRead},
		{"group", ChatHike, core.StatusRead},
		{"note to self", ChatNoteToSelf, core.StatusSent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestService(t, Options{Seed: 1, Latency: 5 * time.Millisecond})
			drain(s)
			ctx := context.Background()
			id, err := s.Send(ctx, tt.chat, core.Outgoing{Body: "**hi** there", Ranges: []core.BodyRange{{Start: 0, Length: 2, Style: core.StyleBold}}})
			if err != nil {
				t.Fatal(err)
			}
			added := waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.MessageAdded); return ok }).(core.MessageAdded)
			if added.Message.ID != id || added.Message.Status != core.StatusPending || added.Notify || !added.Message.Outgoing {
				t.Fatalf("MessageAdded = %+v", added)
			}
			var seen []core.DeliveryStatus
			for len(seen) == 0 || seen[len(seen)-1] != tt.final {
				ev := waitEvent(t, s, 2*time.Second, func(ev core.Event) bool {
					u, ok := ev.(core.MessageUpdated)
					return ok && u.Message.ID == id
				}).(core.MessageUpdated)
				seen = append(seen, ev.Message.Status)
				if tt.chat.IsGroup() {
					for aci, st := range ev.Message.PerRecipient {
						if st != ev.Message.Status {
							t.Fatalf("recipient %s at %v, message at %v", aci, st, ev.Message.Status)
						}
					}
				}
			}
			for i := 1; i < len(seen); i++ {
				if seen[i] <= seen[i-1] {
					t.Fatalf("statuses not increasing: %v", seen)
				}
			}
			m, err := s.Message(ctx, id)
			if err != nil || m.Status != tt.final {
				t.Fatalf("final status %v (%v)", m.Status, err)
			}
			c, _ := s.Chat(ctx, tt.chat)
			if c.Unread != 0 || c.LastMessage == nil || c.LastMessage.ID != id {
				t.Errorf("chat after send: unread=%d last=%+v", c.Unread, c.LastMessage)
			}
		})
	}
}

func TestSendValidation(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	st := core.Sticker{PackID: critterPackID, StickerID: 1}
	tests := []struct {
		name string
		chat core.ChatID
		out  core.Outgoing
		want error // nil = any error
		ok   bool
	}{
		{name: "empty", chat: ChatAlice, out: core.Outgoing{}},
		{name: "body+sticker", chat: ChatAlice, out: core.Outgoing{Body: "x", Sticker: &st}},
		{name: "poll one option", chat: ChatAlice, out: core.Outgoing{Poll: &core.Poll{Question: "q", Options: []core.PollOption{{Text: "a"}}}}},
		{name: "view-once text", chat: ChatAlice, out: core.Outgoing{Body: "x", ViewOnce: true}},
		{name: "unknown chat", chat: "u:nope", out: core.Outgoing{Body: "x"}, want: core.ErrNotFound},
		{name: "announcement group", chat: ChatTrailAlerts, out: core.Outgoing{Body: "x"}, want: core.ErrReadOnly},
		{name: "message request", chat: ChatInes, out: core.Outgoing{Body: "x"}, want: core.ErrReadOnly},
		{name: "blocked", chat: ChatMax, out: core.Outgoing{Body: "x"}, want: core.ErrReadOnly},
		{name: "sticker", chat: ChatAlice, out: core.Outgoing{Sticker: &st}, ok: true},
		{name: "poll", chat: ChatHike, out: core.Outgoing{Poll: &core.Poll{Question: "Snacks?", Options: []core.PollOption{{Text: "yes"}, {Text: "YES"}}}}, ok: true},
		{name: "attachment with caption body", chat: ChatAlice, out: core.Outgoing{Body: "look",
			Attachments: []core.OutgoingAttachment{{FileName: "p.png", Data: landscapePNG(40, 30, sceneNoon)}}}, ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Send(ctx, tt.chat, tt.out)
			switch {
			case tt.ok && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case !tt.ok && err == nil:
				t.Fatal("expected error")
			case tt.want != nil && !errors.Is(err, tt.want):
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	m := lastOf(t, s, ChatAlice)
	if len(m.Attachments) != 1 || m.Attachments[0].ContentType != "image/png" || m.Attachments[0].Width != 40 || m.Attachments[0].BlurHash == "" {
		t.Fatalf("sent attachment metadata: %+v", m.Attachments)
	}
	data, err := s.Attachment(ctx, m.ID, m.Attachments[0].ID)
	if err != nil || !isPNG(data) {
		t.Fatalf("sent attachment bytes: %v", err)
	}
}

func TestSendQuoteAndPreview(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	target := findMsg(t, s, ChatAlice, func(m core.Message) bool { return len(m.Attachments) > 0 && m.Attachments[0].IsImage() })
	id, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "see https://example.org/trails!", Quote: &target.ID})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := s.Message(ctx, id)
	if m.Quote == nil || m.Quote.ID != target.ID || !m.Quote.OriginalFound || m.Quote.Attachment == nil || m.Quote.AuthorName == "" {
		t.Fatalf("quote: %+v", m.Quote)
	}
	if m.Preview == nil || m.Preview.URL != "https://example.org/trails" || m.Preview.Title != "example.org" {
		t.Fatalf("auto preview: %+v", m.Preview)
	}
	other := lastOf(t, s, ChatHike)
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "x", Quote: &other.ID}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("cross-chat quote: %v", err)
	}
}

func TestReactToggle(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	target := lastOf(t, s, ChatAlice)
	selfReactions := func() []string {
		m, _ := s.Message(ctx, target.ID)
		var out []string
		for _, r := range m.Reactions {
			if r.From == SelfACI {
				out = append(out, r.Emoji)
				if r.FromName != "Robin Weber" {
					t.Errorf("FromName = %q", r.FromName)
				}
			}
		}
		return out
	}
	steps := []struct {
		emoji  string
		remove bool
		want   []string
	}{
		{"❤️", false, []string{"❤️"}},
		{"❤️", false, []string{"❤️"}}, // idempotent
		{"😂", false, []string{"😂"}},   // replaces
		{"❤️", true, []string{"😂"}},   // removing a different emoji is a no-op
		{"😂", true, nil},
		{"😂", true, nil},
		{"👍", false, []string{"👍"}},
		{"", true, nil}, // remove whatever
	}
	drain(s)
	for i, st := range steps {
		if err := s.React(ctx, target.ID, st.emoji, st.remove); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := selfReactions(); strings.Join(got, ",") != strings.Join(st.want, ",") {
			t.Fatalf("step %d: reactions %v, want %v", i, got, st.want)
		}
	}
	sys := findMsg(t, s, ChatHike, func(m core.Message) bool { return m.Kind == core.KindSystem })
	if err := s.React(ctx, sys.ID, "👍", false); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("react to system message: %v", err)
	}
	if err := s.React(ctx, target.ID, "", false); err == nil {
		t.Error("empty reaction accepted")
	}
}

func TestEditHistory(t *testing.T) {
	s, clk := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "helo"})
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if err := s.Edit(ctx, id, "hello", nil); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	ranges := []core.BodyRange{{Start: 0, Length: 5, Style: core.StyleBold}}
	if err := s.Edit(ctx, id, "hello!", ranges); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Message(ctx, id)
	if !m.Edited || m.Body != "hello!" || len(m.Ranges) != 1 {
		t.Fatalf("after edit: %+v", m)
	}
	if len(m.EditHistory) != 2 || m.EditHistory[0].Body != "helo" || m.EditHistory[1].Body != "hello" {
		t.Fatalf("history: %+v", m.EditHistory)
	}
	if !m.EditHistory[0].At.Equal(m.SentAt) || !m.EditHistory[1].At.Equal(testNow.Add(time.Minute)) {
		t.Errorf("history times: %v %v", m.EditHistory[0].At, m.EditHistory[1].At)
	}
	incoming := lastOf(t, s, ChatHike)
	if err := s.Edit(ctx, incoming.ID, "nope", nil); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("edit incoming: %v", err)
	}
	if err := s.Edit(ctx, id, "  ", nil); err == nil {
		t.Error("empty edit accepted")
	}
	// The seeded pinned packing list already has history; a new edit appends.
	gear := findMsg(t, s, ChatHike, func(m core.Message) bool { return m.Pinned && m.Outgoing })
	if err := s.Edit(ctx, gear.ID, "**Packing list** v4", nil); err != nil {
		t.Fatal(err)
	}
	g2, _ := s.Message(ctx, gear.ID)
	if len(g2.EditHistory) != len(gear.EditHistory)+1 || g2.EditHistory[len(g2.EditHistory)-1].Body != gear.Body {
		t.Errorf("seeded history not extended: %d → %d", len(gear.EditHistory), len(g2.EditHistory))
	}
}

func TestDelete(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, _ := s.Send(ctx, ChatAlice, core.Outgoing{Body: "oops",
		Attachments: []core.OutgoingAttachment{{FileName: "a.png", Data: landscapePNG(20, 20, sceneDusk)}}})
	drain(s)
	if err := s.Delete(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	ev := waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.MessageUpdated); return ok }).(core.MessageUpdated)
	if !ev.Message.RemoteDeleted || ev.Message.Body != "" || len(ev.Message.Attachments) != 0 {
		t.Fatalf("delete for everyone: %+v", ev.Message)
	}
	c, _ := s.Chat(ctx, ChatAlice)
	if c.LastMessage.Text != "This message was deleted" {
		t.Errorf("preview after delete: %q", c.LastMessage.Text)
	}
	if err := s.Delete(ctx, id, true); err != nil {
		t.Errorf("second delete for everyone: %v", err)
	}
	incoming := lastOf(t, s, ChatHike)
	if err := s.Delete(ctx, incoming.ID, true); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("delete incoming for everyone: %v", err)
	}
	before := len(allMessages(t, s, ChatHike))
	drain(s)
	if err := s.Delete(ctx, incoming.ID, false); err != nil {
		t.Fatal(err)
	}
	rm := waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.MessageRemoved); return ok }).(core.MessageRemoved)
	if rm.ID != incoming.ID || len(allMessages(t, s, ChatHike)) != before-1 {
		t.Fatal("delete for me did not remove the message")
	}
	if _, err := s.Message(ctx, incoming.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted message still found: %v", err)
	}
	if err := s.Delete(ctx, incoming.ID, false); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("double delete: %v", err)
	}
}

func TestUnreadAndMarkRead(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	c, _ := s.Chat(ctx, ChatHike)
	if c.Unread != 7 || c.MentionsUnread != 2 {
		t.Fatalf("seeded unread: %d mentions %d", c.Unread, c.MentionsUnread)
	}
	ms := allMessages(t, s, ChatHike)
	// Read up to the third-from-last message: two incoming stay unread.
	if err := s.MarkRead(ctx, ChatHike, ms[len(ms)-3].ID); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Chat(ctx, ChatHike)
	if c.Unread != 2 || c.MentionsUnread != 1 {
		t.Fatalf("partial read: unread %d mentions %d", c.Unread, c.MentionsUnread)
	}
	// Marking an older message read never moves the marker backwards.
	if err := s.MarkRead(ctx, ChatHike, ms[0].ID); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Chat(ctx, ChatHike); c.Unread != 2 {
		t.Fatalf("marker moved back: %d", c.Unread)
	}
	if err := s.MarkRead(ctx, ChatHike, core.MessageID{}); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Chat(ctx, ChatHike); c.Unread != 0 || c.MentionsUnread != 0 {
		t.Fatalf("after full read: %d/%d", c.Unread, c.MentionsUnread)
	}
	// Marked unread round trip.
	tr := true
	if err := s.UpdateChat(ctx, ChatHike, core.ChatUpdate{MarkedUnread: &tr}); err != nil {
		t.Fatal(err)
	}
	unread, _ := s.Chats(ctx, core.ChatFilter{UnreadOnly: true})
	if !containsChat(unread, ChatHike) {
		t.Error("marked-unread chat not in unread filter")
	}
	_ = s.MarkRead(ctx, ChatHike, core.MessageID{})
	if c, _ = s.Chat(ctx, ChatHike); c.MarkedUnread {
		t.Error("MarkRead did not clear MarkedUnread")
	}
	if err := s.MarkRead(ctx, "u:nope", core.MessageID{}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown chat: %v", err)
	}
}

func TestDisappearingMessages(t *testing.T) {
	s, clk := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	c, _ := s.Chat(ctx, ChatNoor)
	if c.ExpireTimer != 7*24*time.Hour {
		t.Fatalf("timer = %v", c.ExpireTimer)
	}
	last := lastOf(t, s, ChatNoor)
	if last.ExpiresIn == 0 || !last.ExpireStartedAt.IsZero() {
		t.Fatalf("unread disappearing message should not be counting: %+v", last.ExpireStartedAt)
	}
	_ = s.MarkRead(ctx, ChatNoor, core.MessageID{})
	last, _ = s.Message(ctx, last.ID)
	if last.ExpireStartedAt.IsZero() {
		t.Fatal("reading did not start the timer")
	}
	id, _ := s.Send(ctx, ChatNoor, core.Outgoing{Body: "poof"})
	sent, _ := s.Message(ctx, id)
	if sent.ExpiresIn != 7*24*time.Hour || sent.ExpireStartedAt.IsZero() {
		t.Fatalf("outgoing timer: %+v", sent)
	}
	clk.Advance(8 * 24 * time.Hour)
	if _, err := s.Message(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("expired message still visible: %v", err)
	}
	drain(s)
	evs := s.reap()
	removed := 0
	for _, ev := range evs {
		if _, ok := ev.(core.MessageRemoved); ok {
			removed++
		}
	}
	if removed == 0 {
		t.Error("reap removed nothing")
	}
	// Setting a timer adds a system notice.
	week := 24 * time.Hour
	if err := s.UpdateChat(ctx, ChatAlice, core.ChatUpdate{ExpireTimer: &week}); err != nil {
		t.Fatal(err)
	}
	if m := lastOf(t, s, ChatAlice); m.Kind != core.KindSystem || !strings.Contains(m.SystemText, "1 day") {
		t.Errorf("timer notice: %+v", m.SystemText)
	}
}

func TestViewOnce(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	vo := findMsg(t, s, ChatNoor, func(m core.Message) bool { return m.ViewOnce && !m.ViewOnceOpened })
	attID := vo.Attachments[0].ID
	if _, err := s.Attachment(ctx, vo.ID, attID); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkViewOnceOpened(ctx, vo.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Message(ctx, vo.ID)
	if !m.ViewOnceOpened || len(m.Attachments) != 0 {
		t.Fatalf("after open: %+v", m)
	}
	if _, err := s.Attachment(ctx, vo.ID, attID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("view-once media still available: %v", err)
	}
	if err := s.MarkViewOnceOpened(ctx, lastOf(t, s, ChatAlice).ID); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("non view-once: %v", err)
	}
}

func TestDraftsAndTyping(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	drain(s)
	if err := s.SaveDraft(ctx, ChatAlice, "half a thought"); err != nil {
		t.Fatal(err)
	}
	ev := waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.ChatUpdated); return ok }).(core.ChatUpdated)
	if ev.Chat.Draft != "half a thought" {
		t.Fatalf("draft event: %q", ev.Chat.Draft)
	}
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "whole thought"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Chat(ctx, ChatAlice); c.Draft != "" {
		t.Errorf("send did not clear draft: %q", c.Draft)
	}
	if err := s.SetTyping(ctx, ChatAlice, true); err != nil {
		t.Error(err)
	}
	if err := s.SetTyping(ctx, "u:nope", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("typing unknown chat: %v", err)
	}
}

func TestPolls(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	multi := findMsg(t, s, ChatHike, func(m core.Message) bool { return m.Poll != nil })
	votes := func(id core.MessageID) [][]core.ACI {
		m, _ := s.Message(ctx, id)
		var out [][]core.ACI
		for _, o := range m.Poll.Options {
			out = append(out, o.Voters)
		}
		return out
	}
	has := func(vs []core.ACI) bool {
		for _, v := range vs {
			if v == SelfACI {
				return true
			}
		}
		return false
	}
	if err := s.VotePoll(ctx, multi.ID, []int{0, 2}); err != nil {
		t.Fatal(err)
	}
	v := votes(multi.ID)
	if !has(v[0]) || has(v[1]) || !has(v[2]) {
		t.Fatalf("multi vote: %v", v)
	}
	if err := s.VotePoll(ctx, multi.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, o := range votes(multi.ID) {
		if has(o) {
			t.Fatal("retract left a vote")
		}
	}
	if err := s.VotePoll(ctx, multi.ID, []int{5}); err == nil {
		t.Error("out-of-range option accepted")
	}
	if err := s.EndPoll(ctx, multi.ID); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("ending someone else's poll: %v", err)
	}
	mine := findMsg(t, s, ChatWork, func(m core.Message) bool { return m.Poll != nil })
	if err := s.VotePoll(ctx, mine.ID, []int{0, 1}); err == nil {
		t.Error("multiple votes on single-choice poll accepted")
	}
	if err := s.VotePoll(ctx, mine.ID, []int{2}); err != nil {
		t.Fatal(err)
	}
	if err := s.EndPoll(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.VotePoll(ctx, mine.ID, []int{0}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("vote on ended poll: %v", err)
	}
	if err := s.VotePoll(ctx, lastOf(t, s, ChatAlice).ID, []int{0}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("vote on non-poll: %v", err)
	}
}

func TestPins(t *testing.T) {
	s, clk := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	ms := allMessages(t, s, ChatAlice)
	var normal []core.Message
	for _, m := range ms {
		if m.Kind == core.KindNormal && !m.RemoteDeleted {
			normal = append(normal, m)
		}
	}
	for _, m := range normal[:4] {
		if err := s.PinMessage(ctx, m.ID, true, 0); err != nil {
			t.Fatal(err)
		}
	}
	pinned := 0
	for _, m := range allMessages(t, s, ChatAlice) {
		if m.Pinned {
			pinned++
		}
	}
	if pinned != maxPinnedMessages {
		t.Fatalf("pinned = %d, want %d", pinned, maxPinnedMessages)
	}
	if m, _ := s.Message(ctx, normal[0].ID); m.Pinned {
		t.Error("oldest pin not dropped")
	}
	if err := s.PinMessage(ctx, normal[3].ID, false, 0); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Message(ctx, normal[3].ID); m.Pinned {
		t.Error("unpin failed")
	}
	// Timed pin expires.
	if err := s.PinMessage(ctx, normal[5].ID, true, time.Hour); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Hour)
	s.reap()
	if m, _ := s.Message(ctx, normal[5].ID); m.Pinned {
		t.Error("timed pin did not expire")
	}
	call := findMsg(t, s, ChatAlice, func(m core.Message) bool { return m.Kind == core.KindCall })
	if err := s.PinMessage(ctx, call.ID, true, 0); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("pin call event: %v", err)
	}
}

func TestForward(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	src := findMsg(t, s, ChatHike, func(m core.Message) bool { return len(m.Attachments) == 3 })
	if err := s.Forward(ctx, src.ID, []core.ChatID{ChatAlice, ChatNoteToSelf}); err != nil {
		t.Fatal(err)
	}
	for _, chat := range []core.ChatID{ChatAlice, ChatNoteToSelf} {
		m := lastOf(t, s, chat)
		if !m.Outgoing || len(m.Attachments) != 3 || m.Quote != nil {
			t.Fatalf("forward to %s: %+v", chat, m)
		}
		if _, err := s.Attachment(ctx, m.ID, m.Attachments[0].ID); err != nil {
			t.Errorf("forwarded attachment: %v", err)
		}
	}
	if err := s.Forward(ctx, src.ID, []core.ChatID{ChatTrailAlerts}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("forward into announcement group: %v", err)
	}
	if err := s.Forward(ctx, src.ID, []core.ChatID{"u:nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("forward to unknown: %v", err)
	}
	if err := s.Forward(ctx, src.ID, nil); err == nil {
		t.Error("forward without targets accepted")
	}
}

func TestUpdateChat(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	tr, fa := true, false
	if err := s.UpdateChat(ctx, ChatDev, core.ChatUpdate{Pinned: &tr}); err != nil {
		t.Fatal(err)
	}
	chats, _ := s.Chats(ctx, core.ChatFilter{})
	if chats[3].ID != ChatDev || chats[3].PinOrder != 3 {
		t.Fatalf("new pin should go last among pins: %v", chatIDs(chats)[:4])
	}
	if err := s.UpdateChat(ctx, ChatDev, core.ChatUpdate{Archived: &tr}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Chat(ctx, ChatDev)
	if !c.Archived || c.Pinned {
		t.Fatalf("archive should unpin: %+v", c)
	}
	if err := s.UpdateChat(ctx, ChatDev, core.ChatUpdate{Archived: &fa}); err != nil {
		t.Fatal(err)
	}
	until := testNow.Add(time.Hour)
	if err := s.UpdateChat(ctx, ChatDev, core.ChatUpdate{MutedUntil: &until}); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Chat(ctx, ChatDev); !c.Muted(testNow) || c.Muted(testNow.Add(2*time.Hour)) {
		t.Errorf("mute: %v", c.MutedUntil)
	}
	if err := s.UpdateChat(ctx, "u:nope", core.ChatUpdate{}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown chat: %v", err)
	}
	// Sending into an archived chat unarchives it.
	_ = s.UpdateChat(ctx, ChatAlice, core.ChatUpdate{Archived: &tr})
	_, _ = s.Send(ctx, ChatAlice, core.Outgoing{Body: "back"})
	if c, _ = s.Chat(ctx, ChatAlice); c.Archived {
		t.Error("send did not unarchive")
	}
	drain(s)
	if err := s.DeleteChat(ctx, ChatMarco); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, time.Second, func(ev core.Event) bool { r, ok := ev.(core.ChatRemoved); return ok && r.ID == ChatMarco })
	if _, err := s.Chat(ctx, ChatMarco); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("deleted chat: %v", err)
	}
}

func TestRequestsAndBlocking(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	if err := s.AcceptMessageRequest(ctx, ChatInes); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Chat(ctx, ChatInes); c.MessageRequest {
		t.Fatal("request not accepted")
	}
	if _, err := s.Send(ctx, ChatInes, core.Outgoing{Body: "count me in!"}); err != nil {
		t.Fatalf("send after accept: %v", err)
	}
	drain(s)
	if err := s.Block(ctx, ChatAlice, true); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, time.Second, func(ev core.Event) bool { c, ok := ev.(core.ContactUpdated); return ok && c.Contact.Blocked })
	chats, _ := s.Chats(ctx, core.ChatFilter{})
	if containsChat(chats, ChatAlice) {
		t.Error("blocked chat listed")
	}
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "x"}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("send to blocked: %v", err)
	}
	if err := s.Block(ctx, ChatAlice, false); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Contact(ctx, AliceACI); c.Blocked {
		t.Error("unblock failed")
	}
	if err := s.Block(ctx, ChatNoteToSelf, true); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("block self: %v", err)
	}
	if err := s.ReportSpam(ctx, ChatMax); err != nil {
		t.Fatal(err)
	}
	if m := lastOf(t, s, ChatMax); m.SystemText != "Reported as spam." {
		t.Errorf("spam notice: %q", m.SystemText)
	}
}

func TestResolveRecipient(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	tests := []struct {
		q       string
		want    core.ChatID
		wantErr error
		anyErr  bool
	}{
		{q: "+4915112340001", want: ChatAlice},
		{q: "+49 151 1234-0001", want: ChatAlice},
		{q: "alice.07", want: ChatAlice},
		{q: "@Alice.07", want: ChatAlice},
		{q: "https://signal.me/#u/elif.42", want: core.DirectChatID(ElifACI)},
		{q: "Alice Chen", want: ChatAlice},
		{q: "alice", want: ChatAlice},
		{q: "Mama", want: ChatMom},
		{q: "jo 🐢", want: ChatJonas},
		{q: "note to self", want: ChatNoteToSelf},
		{q: "robin.07", want: ChatNoteToSelf},
		{q: "Zelda", wantErr: core.ErrNotFound},
		{q: "ch", anyErr: true}, // ambiguous
		{q: "+12", anyErr: true},
		{q: "  ", anyErr: true},
		{q: "https://signal.me/#u/nobody.00", wantErr: core.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			got, err := s.ResolveRecipient(ctx, tt.q)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil {
					t.Fatalf("want error, got %s", got)
				}
			case err != nil || got != tt.want:
				t.Fatalf("= %s, %v; want %s", got, err, tt.want)
			}
		})
	}
	// New numbers and usernames create a contact and an (empty) chat.
	id, err := s.ResolveRecipient(ctx, "+4917000000001")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := s.ResolveRecipient(ctx, "+4917000000001")
	if again != id {
		t.Error("resolving twice created two chats")
	}
	c, err := s.Chat(ctx, id)
	if err != nil || c.Contact == nil || c.Contact.E164 != "+4917000000001" {
		t.Fatalf("new chat: %+v %v", c, err)
	}
	uid, err := s.ResolveRecipient(ctx, "newfriend.12")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Chat(ctx, uid); c.Title != "newfriend.12" {
		t.Errorf("username chat title: %q", c.Title)
	}
}

func TestGroups(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	id, err := s.CreateGroup(ctx, "Sauna Club 🧖", []core.ACI{AliceACI, ElifACI, SelfACI, AliceACI}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Chat(ctx, id)
	if !c.SelfIsAdmin || len(c.Members) != 3 || c.Kind != core.ChatGroup || c.Title != "Sauna Club 🧖" {
		t.Fatalf("created group: %+v", c)
	}
	if _, err := s.Avatar(ctx, c.AvatarID); err != nil {
		t.Errorf("group avatar: %v", err)
	}
	if ms := allMessages(t, s, id); len(ms) != 2 || ms[0].SystemText != "You created the group." ||
		ms[1].SystemText != "You added Alice Chen and Elif Yılmaz." {
		t.Fatalf("creation notices: %+v", ms)
	}
	if _, err := s.CreateGroup(ctx, " ", nil, nil); err == nil {
		t.Error("untitled group accepted")
	}
	if _, err := s.CreateGroup(ctx, "x", []core.ACI{"nope"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown member: %v", err)
	}

	title, desc, tr := "Sauna Society", "Hot rocks only", true
	mode := core.AccessAdmin
	err = s.UpdateGroup(ctx, id, core.GroupUpdate{
		Title: &title, Description: &desc, AddMembers: []core.ACI{BobACI}, Promote: []core.ACI{AliceACI},
		RemoveMembers: []core.ACI{ElifACI}, OnlyAdminsSend: &tr, LinkMode: &mode, Avatar: []byte("png?"),
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = s.Chat(ctx, id)
	if c.Title != title || c.Description != desc || !c.OnlyAdminsSend || c.InviteLink == "" || c.GroupRevision != 1 {
		t.Fatalf("after update: %+v", c)
	}
	if memberIndex(c.Members, BobACI) < 0 || memberIndex(c.Members, ElifACI) >= 0 || !c.Members[memberIndex(c.Members, AliceACI)].Admin {
		t.Fatalf("members after update: %+v", c.Members)
	}
	link := c.InviteLink
	if err := s.UpdateGroup(ctx, id, core.GroupUpdate{ResetLink: true}); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.Chat(ctx, id); c.InviteLink == link {
		t.Error("link not reset")
	}

	// Approving join requests in the seeded hike group (self is admin).
	if err := s.UpdateGroup(ctx, ChatHike, core.GroupUpdate{Approve: []core.ACI{InesACI}}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.Chat(ctx, ChatHike); len(h.RequestingJoin) != 0 || memberIndex(h.Members, InesACI) < 0 {
		t.Fatalf("approve: %+v", h.RequestingJoin)
	}
	// Non-admin restrictions in the work chat.
	if err := s.UpdateGroup(ctx, ChatWork, core.GroupUpdate{RemoveMembers: []core.ACI{OskarACI}}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("non-admin remove: %v", err)
	}
	newTitle := "Platform Team 🚀"
	if err := s.UpdateGroup(ctx, ChatWork, core.GroupUpdate{Title: &newTitle}); err != nil {
		t.Errorf("non-admin title change: %v", err)
	}
	if err := s.UpdateGroup(ctx, ChatAlice, core.GroupUpdate{Title: &newTitle}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("update on direct chat: %v", err)
	}

	// Leave and rejoin by link.
	hike, _ := s.Chat(ctx, ChatHike)
	if err := s.LeaveGroup(ctx, ChatHike); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, ChatHike, core.Outgoing{Body: "x"}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("send after leave: %v", err)
	}
	if err := s.LeaveGroup(ctx, ChatHike); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("double leave: %v", err)
	}
	joined, err := s.JoinGroupByLink(ctx, hike.InviteLink)
	if err != nil || joined != ChatHike {
		t.Fatalf("rejoin: %s %v", joined, err)
	}
	if _, err := s.Send(ctx, ChatHike, core.Outgoing{Body: "I'm back"}); err != nil {
		t.Errorf("send after rejoin: %v", err)
	}
	if _, err := s.JoinGroupByLink(ctx, "https://signal.group/#unknown"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown link: %v", err)
	}
	if _, err := s.JoinGroupByLink(ctx, "https://example.com"); err == nil {
		t.Error("non-group link accepted")
	}
}

func TestSafetyNumberAndContacts(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	sn, err := s.SafetyNumber(ctx, BobACI)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9]{60}$`).MatchString(sn.Digits) || len(sn.QRData) == 0 || sn.Verified {
		t.Fatalf("safety number: %+v", sn)
	}
	sn2, _ := New(Options{Seed: 99, Clock: func() time.Time { return testNow }}).SafetyNumber(ctx, BobACI)
	if sn2.Digits != sn.Digits {
		t.Error("safety number not deterministic")
	}
	if other, _ := s.SafetyNumber(ctx, AliceACI); other.Digits == sn.Digits {
		t.Error("different contacts share a safety number")
	}
	if _, err := s.SafetyNumber(ctx, SelfACI); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("self safety number: %v", err)
	}
	if err := s.SetVerified(ctx, BobACI, true); err != nil {
		t.Fatal(err)
	}
	if sn, _ = s.SafetyNumber(ctx, BobACI); !sn.Verified {
		t.Error("verification not stored")
	}
	if err := s.SetNickname(ctx, AliceACI, "Al 🏔️"); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Chat(ctx, ChatAlice)
	if c.Title != "Al 🏔️" || lastOf(t, s, ChatAlice).SenderName != "Al 🏔️" {
		t.Errorf("nickname not applied: %q", c.Title)
	}
	given, about := "Robyn", "testing"
	if err := s.UpdateProfile(ctx, core.ProfileUpdate{GivenName: &given, About: &about, Avatar: []byte{}}); err != nil {
		t.Fatal(err)
	}
	if self := s.Self(); self.ProfileName != "Robyn Weber" || self.About != "testing" || self.AvatarID != "" {
		t.Errorf("profile: %+v", self)
	}
	empty := ""
	if err := s.UpdateProfile(ctx, core.ProfileUpdate{GivenName: &empty}); err == nil {
		t.Error("empty given name accepted")
	}
}

func TestStories(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	stories, _ := s.Stories(ctx)
	var alice, group core.Story
	for _, st := range stories {
		switch {
		case st.ID.Author == AliceACI:
			alice = st
		case st.Group != "":
			group = st
		}
	}
	if err := s.MarkStoryViewed(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplyToStory(ctx, alice.ID, "", "😍"); err != nil {
		t.Fatal(err)
	}
	m := lastOf(t, s, ChatAlice)
	if m.Kind != core.KindStoryReply || m.StoryContext == nil || m.StoryContext.Emoji != "😍" || m.StoryContext.Timestamp != alice.ID.Timestamp {
		t.Fatalf("story reaction: %+v", m)
	}
	if err := s.ReplyToStory(ctx, group.ID, "that lake!!", ""); err != nil {
		t.Fatal(err)
	}
	if m := lastOf(t, s, ChatHike); m.Kind != core.KindStoryReply || m.Body != "that lake!!" {
		t.Fatalf("group story reply landed wrong: %+v", m)
	}
	drain(s)
	if err := s.PostStory(ctx, core.OutgoingStory{Text: &core.TextStory{Text: "hello ridge", Background: 0xff000000}}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.StoriesChanged); return ok })
	if err := s.PostStory(ctx, core.OutgoingStory{
		Media: &core.OutgoingAttachment{Data: landscapePNG(30, 50, sceneLake), Caption: "lake"},
		To:    []core.ChatID{ChatHike, ChatBookClub},
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Stories(ctx)
	if len(after) != len(stories)+3 {
		t.Fatalf("stories: %d → %d", len(stories), len(after))
	}
	for _, st := range after {
		if st.Media != nil && st.Caption == "lake" {
			if _, err := s.Attachment(ctx, st.ID, st.Media.ID); err != nil {
				t.Errorf("posted story media: %v", err)
			}
		}
	}
	if err := s.PostStory(ctx, core.OutgoingStory{}); err == nil {
		t.Error("empty story accepted")
	}
	if err := s.PostStory(ctx, core.OutgoingStory{Text: &core.TextStory{Text: "x"}, To: []core.ChatID{ChatAlice}}); !errors.Is(err, core.ErrReadOnly) {
		t.Errorf("story to direct chat: %v", err)
	}
	if err := s.MarkStoryViewed(ctx, core.MessageID{Timestamp: 1}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown story: %v", err)
	}
}

func TestStickersSettingsAccount(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	if err := s.InstallStickerPack(ctx, towerPackID, towerPackKey, true); err != nil {
		t.Fatal(err)
	}
	packs, _ := s.StickerPacks(ctx)
	for _, p := range packs {
		if !p.Installed {
			t.Errorf("pack %s not installed", p.Title)
		}
	}
	if err := s.InstallStickerPack(ctx, towerPackID, "bad", true); err == nil {
		t.Error("wrong key accepted")
	}
	if err := s.InstallStickerPack(ctx, "nope", "", true); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown pack: %v", err)
	}

	set, _ := s.Settings(ctx)
	set.LinkPreviews = false
	set.NotificationLevel = core.NotifyNone
	if err := s.UpdateSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Settings(ctx); got != set {
		t.Errorf("settings: %+v", got)
	}
	id, _ := s.Send(ctx, ChatAlice, core.Outgoing{Body: "https://example.org"})
	if m, _ := s.Message(ctx, id); m.Preview != nil {
		t.Error("link preview generated with previews disabled")
	}

	path := filepath.Join(t.TempDir(), "backup.json")
	if err := s.ExportBackup(ctx, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", info, err)
	}
	raw, _ := os.ReadFile(path)
	var b backupFile
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.Self.ACI != SelfACI || len(b.Chats) < 14 || len(b.Contacts) == 0 {
		t.Errorf("backup contents: %d chats, %d contacts", len(b.Chats), len(b.Contacts))
	}

	drain(s)
	if err := s.Unlink(ctx); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, time.Second, func(ev core.Event) bool { _, ok := ev.(core.LoggedOut); return ok })
	if s.ConnState() != core.ConnLoggedOut {
		t.Errorf("conn state: %v", s.ConnState())
	}
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "x"}); !errors.Is(err, core.ErrNotLoggedIn) {
		t.Errorf("send after unlink: %v", err)
	}
}

func TestAttachmentDownload(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1, Latency: 2 * time.Millisecond})
	ctx := context.Background()
	img := findMsg(t, s, ChatAlice, func(m core.Message) bool { return len(m.Attachments) > 0 && m.Attachments[0].IsImage() })
	id, attID := img.ID, img.Attachments[0].ID
	// Pretend the photo hasn't been downloaded yet.
	s.mu.Lock()
	target := s.chats[ChatAlice].find(id)
	target.Attachments[0] = notDownloaded(target.Attachments[0])
	s.mu.Unlock()
	if m, _ := s.Message(ctx, id); m.Attachments[0].Downloaded {
		t.Fatal("setup failed")
	}
	drain(s)
	data, err := s.Attachment(ctx, id, attID)
	if err != nil || !isPNG(data) {
		t.Fatalf("download: %v", err)
	}
	ev := waitEvent(t, s, time.Second, func(ev core.Event) bool {
		u, ok := ev.(core.MessageUpdated)
		return ok && u.Message.ID == id
	}).(core.MessageUpdated)
	if !ev.Message.Attachments[0].Downloaded || ev.Message.Attachments[0].Progress != 1 {
		t.Fatalf("download state: %+v", ev.Message.Attachments[0])
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Attachment(cctx, id, attID); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
