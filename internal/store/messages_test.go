package store

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"warte/internal/core"
)

// msg builds an incoming text message with SentAt = ts.
func msg(chat core.ChatID, author core.ACI, ts uint64, body string) core.Message {
	return core.Message{
		ID:         core.MessageID{Chat: chat, Author: author, Timestamp: ts},
		SentAt:     time.UnixMilli(int64(ts)).UTC(),
		ReceivedAt: time.UnixMilli(int64(ts) + 5).UTC(),
		Body:       body,
	}
}

func msgTimestamps(ms []core.Message) []uint64 {
	var out []uint64
	for _, m := range ms {
		out = append(out, m.ID.Timestamp)
	}
	return out
}

func fullMessage() core.Message {
	m := msg("g:grp", "alice", 1_700_000_000_123, "hello **world** ￼")
	m.Kind = core.KindNormal
	m.SenderName = "Alice"
	m.Ranges = []core.BodyRange{{Start: 6, Length: 9, Style: core.StyleBold}, {Start: 16, Length: 1, Style: core.StyleMention, MentionACI: "bob"}}
	m.Quote = &core.Quote{ID: core.MessageID{Chat: "g:grp", Author: "bob", Timestamp: 5}, AuthorName: "Bob", Text: "q",
		Attachment: &core.Attachment{ID: "qa", ContentType: "image/jpeg"}, OriginalFound: true}
	m.Attachments = []core.Attachment{{ID: "att1", ContentType: "image/png", FileName: "a.png", Size: 1234, Width: 10, Height: 20,
		Caption: "cap", BlurHash: "LEHV6n", Downloaded: true, Progress: 1}}
	m.Preview = &core.LinkPreview{URL: "https://x.org", Title: "X", Description: "d", Date: t0, Image: &core.Attachment{ID: "img"}}
	m.Contact = &core.SharedContact{Name: "C", Phones: []string{"+1"}, Emails: []string{"c@x"}, Org: "O"}
	m.Poll = &core.Poll{Question: "?", Options: []core.PollOption{{Text: "a", Voters: []core.ACI{"bob"}}}, AllowMultiple: true}
	m.Reactions = []core.Reaction{{Emoji: "❤️", From: "bob", FromName: "Bob", At: t0}}
	m.Edited = true
	m.EditHistory = []core.EditRevision{{Body: "helo", At: t0.Add(-time.Minute)}}
	m.ViewOnce = false
	m.ExpiresIn = time.Hour
	m.ExpireStartedAt = t0
	m.Pinned = true
	m.PerRecipient = nil
	m.MentionsSelf = true
	m.ServerTimestamp = 1_700_000_000_200
	m.StoryContext = &core.StoryRef{Author: "carol", Timestamp: 77, Emoji: ""}
	m.PaymentNotice = ""
	return m
}

func TestMessageRoundTrip(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	in := fullMessage()
	created, err := env.db.PutMessage(ctx, in)
	if err != nil || !created {
		t.Fatalf("PutMessage = %v, %v", created, err)
	}
	got, err := env.db.GetMessage(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
	in.Body = "replaced"
	created, err = env.db.PutMessage(ctx, in)
	if err != nil || created {
		t.Fatalf("replace = %v, %v", created, err)
	}
	if got, _ := env.db.GetMessage(ctx, in.ID); got.Body != "replaced" {
		t.Errorf("body = %q", got.Body)
	}
	if _, err := env.db.GetMessage(ctx, core.MessageID{Chat: "g:grp", Author: "x", Timestamp: 1}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	if _, err := env.db.PutMessage(ctx, core.Message{}); err == nil {
		t.Error("zero ID should be rejected")
	}
}

func TestMessageNamesResolvedOnRead(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	m := fullMessage()
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	for _, c := range []core.Contact{{ACI: "alice", Nickname: "Al"}, {ACI: "bob", ContactName: "Bobby"}} {
		if err := env.db.UpsertContact(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := env.db.GetMessage(ctx, m.ID)
	if got.SenderName != "Al" || got.Reactions[0].FromName != "Bobby" || got.Quote.AuthorName != "Bobby" {
		t.Errorf("names = %q %q %q", got.SenderName, got.Reactions[0].FromName, got.Quote.AuthorName)
	}
}

func TestPutMessageDefaultsSentAt(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	m := core.Message{ID: core.MessageID{Chat: "u:a", Author: "a", Timestamp: 4242}}
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, _ := env.db.GetMessage(ctx, m.ID)
	if !got.SentAt.Equal(time.UnixMilli(4242)) {
		t.Errorf("SentAt = %v", got.SentAt)
	}
}

func TestListMessagesPagination(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	// Insert out of order; two messages share a SentAt (tie broken by author).
	for _, ts := range []uint64{5000, 1000, 3000, 2000, 4000} {
		if _, err := env.db.PutMessage(ctx, msg("u:a", "a", ts, "m")); err != nil {
			t.Fatal(err)
		}
	}
	tie := msg("u:a", "b", 3001, "tie")
	tie.SentAt = time.UnixMilli(3000).UTC()
	if _, err := env.db.PutMessage(ctx, tie); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.PutMessage(ctx, msg("u:other", "a", 2500, "other chat")); err != nil {
		t.Fatal(err)
	}
	id := func(author core.ACI, ts uint64) core.MessageID {
		return core.MessageID{Chat: "u:a", Author: author, Timestamp: ts}
	}
	tests := []struct {
		name   string
		before core.MessageID
		limit  int
		want   []uint64
	}{
		{"newest page", core.MessageID{}, 3, []uint64{3001, 4000, 5000}},
		{"all", core.MessageID{}, 0, []uint64{1000, 2000, 3000, 3001, 4000, 5000}},
		{"before tie", id("b", 3001), 10, []uint64{1000, 2000, 3000}},
		{"before 3000", id("a", 3000), 2, []uint64{1000, 2000}},
		{"before oldest", id("a", 1000), 10, nil},
		{"unknown before uses timestamp", id("zz", 2500), 10, []uint64{1000, 2000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := env.db.ListMessages(ctx, "u:a", tt.before, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if ts := msgTimestamps(got); !slices.Equal(ts, tt.want) {
				t.Errorf("got %v, want %v", ts, tt.want)
			}
		})
	}
}

func TestMessagesAround(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	for ts := uint64(1); ts <= 10; ts++ {
		if _, err := env.db.PutMessage(ctx, msg("u:a", "a", ts*1000, "m")); err != nil {
			t.Fatal(err)
		}
	}
	id := func(ts uint64) core.MessageID { return core.MessageID{Chat: "u:a", Author: "a", Timestamp: ts * 1000} }
	tests := []struct {
		name  string
		at    uint64
		limit int
		want  []uint64
	}{
		{"middle odd", 5, 5, []uint64{3000, 4000, 5000, 6000, 7000}},
		{"middle even", 5, 4, []uint64{4000, 5000, 6000, 7000}},
		{"start", 1, 5, []uint64{1000, 2000, 3000, 4000, 5000}},
		{"end", 10, 5, []uint64{6000, 7000, 8000, 9000, 10000}},
		{"near end", 9, 5, []uint64{6000, 7000, 8000, 9000, 10000}},
		{"bigger than chat", 4, 50, []uint64{1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000}},
		{"one", 7, 1, []uint64{7000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := env.db.MessagesAround(ctx, id(tt.at), tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if ts := msgTimestamps(got); !slices.Equal(ts, tt.want) {
				t.Errorf("got %v, want %v", ts, tt.want)
			}
		})
	}
	if _, err := env.db.MessagesAround(ctx, id(99), 5); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
}

func TestUpdateMessage(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	m := msg("u:a", "a", 1000, "original text")
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := env.db.UpdateMessage(ctx, m.ID, func(m *core.Message) error {
		m.EditHistory = append(m.EditHistory, core.EditRevision{Body: m.Body})
		m.Body, m.Edited = "edited text", true
		return nil
	})
	if err != nil || got.Body != "edited text" || !got.Edited || len(got.EditHistory) != 1 {
		t.Fatalf("UpdateMessage = %+v, %v", got, err)
	}
	// Unread state survives updates.
	if u, _, _ := env.db.CountUnread(ctx, "u:a"); u != 1 {
		t.Errorf("unread = %d, want 1", u)
	}
	// Search index follows the edit.
	if hits, _ := env.db.Search(ctx, core.SearchQuery{Text: "original"}); len(hits) != 0 {
		t.Errorf("old text still indexed")
	}
	if hits, _ := env.db.Search(ctx, core.SearchQuery{Text: "edited"}); len(hits) != 1 {
		t.Errorf("new text not indexed")
	}
	boom := errors.New("boom")
	if _, err := env.db.UpdateMessage(ctx, m.ID, func(m *core.Message) error { m.Body = "x"; return boom }); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if _, err := env.db.UpdateMessage(ctx, m.ID, func(m *core.Message) error { m.ID.Timestamp++; return nil }); err == nil {
		t.Error("ID change should fail")
	}
	if got, _ := env.db.GetMessage(ctx, m.ID); got.Body != "edited text" {
		t.Errorf("body = %q after failed updates", got.Body)
	}
	if _, err := env.db.UpdateMessage(ctx, core.MessageID{Chat: "u:a", Author: "a", Timestamp: 1}, func(*core.Message) error { return nil }); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	// Remote delete hides the body from search.
	if _, err := env.db.UpdateMessage(ctx, m.ID, func(m *core.Message) error { m.RemoteDeleted = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if hits, _ := env.db.Search(ctx, core.SearchQuery{Text: "edited"}); len(hits) != 0 {
		t.Errorf("remote-deleted message still searchable")
	}
}

func TestSetReactionReplaceSemantics(t *testing.T) {
	type step struct {
		from  core.ACI
		emoji string
		at    int
	}
	tests := []struct {
		name  string
		steps []step
		want  []core.Reaction
	}{
		{"single", []step{{"a", "👍", 1}}, []core.Reaction{{Emoji: "👍", From: "a", At: t0.Add(1 * time.Second)}}},
		{"replace same sender", []step{{"a", "👍", 1}, {"a", "❤️", 2}}, []core.Reaction{{Emoji: "❤️", From: "a", At: t0.Add(2 * time.Second)}}},
		{"two senders sorted by time", []step{{"b", "😂", 2}, {"a", "👍", 1}}, []core.Reaction{
			{Emoji: "👍", From: "a", At: t0.Add(1 * time.Second)}, {Emoji: "😂", From: "b", At: t0.Add(2 * time.Second)}}},
		{"remove", []step{{"a", "👍", 1}, {"b", "😂", 2}, {"a", "", 3}}, []core.Reaction{{Emoji: "😂", From: "b", At: t0.Add(2 * time.Second)}}},
		{"remove last", []step{{"a", "👍", 1}, {"a", "", 2}}, nil},
		{"remove absent", []step{{"a", "", 1}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestDB(t)
			ctx := ctxT(t)
			m := msg("u:x", "x", 1000, "react to me")
			if _, err := env.db.PutMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
			var got core.Message
			var err error
			for _, s := range tt.steps {
				got, err = env.db.SetReaction(ctx, m.ID, core.Reaction{Emoji: s.emoji, From: s.from, At: t0.Add(time.Duration(s.at) * time.Second)})
				if err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(got.Reactions, tt.want) {
				t.Errorf("reactions = %+v, want %+v", got.Reactions, tt.want)
			}
			stored, _ := env.db.GetMessage(ctx, m.ID)
			if !reflect.DeepEqual(stored.Reactions, tt.want) {
				t.Errorf("stored reactions = %+v", stored.Reactions)
			}
		})
	}
}

func TestMergeAndAggregateStatus(t *testing.T) {
	merge := []struct {
		old, upd, want core.DeliveryStatus
	}{
		{core.StatusNone, core.StatusSent, core.StatusSent},
		{core.StatusSent, core.StatusDelivered, core.StatusDelivered},
		{core.StatusRead, core.StatusDelivered, core.StatusRead},
		{core.StatusPending, core.StatusFailed, core.StatusFailed},
		{core.StatusSent, core.StatusFailed, core.StatusSent},
		{core.StatusFailed, core.StatusSent, core.StatusSent},
		{core.StatusFailed, core.StatusPending, core.StatusPending},
		{core.StatusViewed, core.StatusRead, core.StatusViewed},
	}
	for _, tt := range merge {
		if got := MergeStatus(tt.old, tt.upd); got != tt.want {
			t.Errorf("MergeStatus(%d, %d) = %d, want %d", tt.old, tt.upd, got, tt.want)
		}
	}
	agg := []struct {
		per  map[core.ACI]core.DeliveryStatus
		want core.DeliveryStatus
	}{
		{nil, core.StatusNone},
		{map[core.ACI]core.DeliveryStatus{"a": core.StatusDelivered}, core.StatusDelivered},
		{map[core.ACI]core.DeliveryStatus{"a": core.StatusDelivered, "b": core.StatusRead}, core.StatusRead},
		{map[core.ACI]core.DeliveryStatus{"a": core.StatusFailed, "b": core.StatusSent}, core.StatusSent},
		{map[core.ACI]core.DeliveryStatus{"a": core.StatusFailed, "b": core.StatusFailed}, core.StatusFailed},
	}
	for i, tt := range agg {
		if got := AggregateStatus(tt.per); got != tt.want {
			t.Errorf("AggregateStatus #%d = %d, want %d", i, got, tt.want)
		}
	}
}

func TestSetReceipt(t *testing.T) {
	type rc struct {
		who    core.ACI
		status core.DeliveryStatus
	}
	tests := []struct {
		name     string
		chat     core.ChatID
		initial  core.DeliveryStatus
		receipts []rc
		want     core.DeliveryStatus
		wantPer  map[core.ACI]core.DeliveryStatus
	}{
		{"direct delivered", "u:bob", core.StatusSent, []rc{{"bob", core.StatusDelivered}}, core.StatusDelivered, nil},
		{"direct read then late delivered", "u:bob", core.StatusSent, []rc{{"bob", core.StatusRead}, {"bob", core.StatusDelivered}}, core.StatusRead, nil},
		{"group max", "g:x", core.StatusSent, []rc{{"a", core.StatusDelivered}, {"b", core.StatusRead}, {"c", core.StatusDelivered}},
			core.StatusRead, map[core.ACI]core.DeliveryStatus{"a": core.StatusDelivered, "b": core.StatusRead, "c": core.StatusDelivered}},
		{"group per-recipient no regress", "g:x", core.StatusSent, []rc{{"a", core.StatusViewed}, {"a", core.StatusRead}},
			core.StatusViewed, map[core.ACI]core.DeliveryStatus{"a": core.StatusViewed}},
		{"all failed", "g:x", core.StatusPending, []rc{{"a", core.StatusFailed}, {"b", core.StatusFailed}},
			core.StatusFailed, map[core.ACI]core.DeliveryStatus{"a": core.StatusFailed, "b": core.StatusFailed}},
		{"failed then retried", "g:x", core.StatusPending, []rc{{"a", core.StatusFailed}, {"a", core.StatusSent}},
			core.StatusSent, map[core.ACI]core.DeliveryStatus{"a": core.StatusSent}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestDB(t)
			ctx := ctxT(t)
			m := msg(tt.chat, "me", 1000, "out")
			m.Outgoing, m.Status = true, tt.initial
			if _, err := env.db.PutMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
			var got core.Message
			var err error
			for _, r := range tt.receipts {
				if got, err = env.db.SetReceipt(ctx, m.ID, r.who, r.status); err != nil {
					t.Fatal(err)
				}
			}
			if got.Status != tt.want || !reflect.DeepEqual(got.PerRecipient, tt.wantPer) {
				t.Errorf("status %d per %v, want %d %v", got.Status, got.PerRecipient, tt.want, tt.wantPer)
			}
			c, _ := env.db.GetChat(ctx, tt.chat)
			if c.LastMessage == nil || c.LastMessage.Status != tt.want {
				t.Errorf("preview status = %+v", c.LastMessage)
			}
			// Replacing the message keeps recorded receipts folded in.
			m.Body = "out (resent)"
			if _, err := env.db.PutMessage(ctx, m); err != nil {
				t.Fatal(err)
			}
			again, _ := env.db.GetMessage(ctx, m.ID)
			if again.Status != tt.want {
				t.Errorf("after replace status = %d, want %d", again.Status, tt.want)
			}
		})
	}
	env := newTestDB(t)
	ctx := ctxT(t)
	if _, err := env.db.SetReceipt(ctx, core.MessageID{Chat: "u:a", Author: "me", Timestamp: 1}, "a", core.StatusRead); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing message err = %v", err)
	}
}

func TestMarkRead(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	chat := core.ChatID("g:x")
	var ids []core.MessageID
	for i, ts := range []uint64{1000, 2000, 3000, 4000, 5000} {
		m := msg(chat, "bob", ts, "m")
		if i == 1 {
			m.MentionsSelf = true
		}
		if i == 2 {
			m.ExpiresIn = time.Minute // incoming: timer starts on read
		}
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	// Outgoing, system and already-read messages never count as unread.
	out := msg(chat, "me", 2500, "mine")
	out.Outgoing = true
	sys := msg(chat, "bob", 2600, "")
	sys.Kind, sys.SystemText = core.KindSystem, "Bob changed the title"
	hist := msg(chat, "bob", 2700, "old history")
	for _, m := range []core.Message{out, sys} {
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.db.PutMessage(ctx, hist, AsRead()); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.ApplyChatUpdate(ctx, chat, core.ChatUpdate{MarkedUnread: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if u, mn, _ := env.db.CountUnread(ctx, chat); u != 5 || mn != 1 {
		t.Fatalf("unread = %d/%d, want 5/1", u, mn)
	}
	if exp, _ := env.db.ExpiringMessages(ctx, t0.Add(24*time.Hour)); len(exp) != 0 {
		t.Fatalf("timer started before read: %v", exp)
	}

	env.clock.Set(t0.Add(time.Hour))
	got, err := env.db.MarkRead(ctx, chat, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, ids[:3]) {
		t.Errorf("marked = %v, want %v", got, ids[:3])
	}
	c, _ := env.db.GetChat(ctx, chat)
	if c.Unread != 2 || c.MentionsUnread != 0 || c.MarkedUnread {
		t.Errorf("after partial read: unread %d mentions %d marked %v", c.Unread, c.MentionsUnread, c.MarkedUnread)
	}
	expMsg, _ := env.db.GetMessage(ctx, ids[2])
	if !expMsg.ExpireStartedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("ExpireStartedAt = %v", expMsg.ExpireStartedAt)
	}
	// Marking again returns nothing new.
	if again, _ := env.db.MarkRead(ctx, chat, ids[2]); len(again) != 0 {
		t.Errorf("second MarkRead = %v", again)
	}
	all, _ := env.db.MarkRead(ctx, chat, core.MessageID{})
	if !slices.Equal(all, ids[3:]) {
		t.Errorf("mark all = %v, want %v", all, ids[3:])
	}
	if u, _, _ := env.db.CountUnread(ctx, chat); u != 0 {
		t.Errorf("unread after all = %d", u)
	}
	if _, _, err := env.db.CountUnread(ctx, "g:nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("CountUnread missing chat err = %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestExpiringMessages(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if _, ok, _ := env.db.NextExpiration(ctx); ok {
		t.Fatal("no timers expected")
	}
	mk := func(ts uint64, in time.Duration, started time.Time) core.MessageID {
		m := msg("u:a", "me", ts, "x")
		m.Outgoing, m.ExpiresIn, m.ExpireStartedAt = true, in, started
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	a := mk(1000, time.Minute, t0)         // expires t0+1m
	b := mk(2000, 10*time.Second, t0)      // expires t0+10s
	_ = mk(3000, time.Hour, t0)            // expires t0+1h
	_ = mk(4000, time.Minute, time.Time{}) // not started
	_ = mk(5000, 0, t0)                    // not disappearing
	next, ok, err := env.db.NextExpiration(ctx)
	if err != nil || !ok || !next.Equal(t0.Add(10*time.Second)) {
		t.Fatalf("NextExpiration = %v %v %v", next, ok, err)
	}
	tests := []struct {
		at   time.Time
		want []core.MessageID
	}{
		{t0, nil},
		{t0.Add(10 * time.Second), []core.MessageID{b}},
		{t0.Add(2 * time.Minute), []core.MessageID{b, a}},
	}
	for _, tt := range tests {
		got, err := env.db.ExpiringMessages(ctx, tt.at)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("ExpiringMessages(%v) = %v, want %v", tt.at, got, tt.want)
		}
	}
}
