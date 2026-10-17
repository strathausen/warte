package store

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"warte/internal/core"
)

func chatIDs(cs []core.Chat) []core.ChatID {
	var out []core.ChatID
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func TestChatRoundTrip(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	in := core.Chat{
		ID:              core.GroupChatID("Z3JvdXA="),
		Kind:            core.ChatGroup,
		Title:           "Book club 📚",
		Description:     "we read",
		AvatarID:        "av1",
		Color:           "A100",
		MarkedUnread:    true,
		Archived:        false,
		Pinned:          true,
		PinOrder:        3,
		MutedUntil:      t0.Add(8 * time.Hour),
		MessageRequest:  true,
		ExpireTimer:     7 * 24 * time.Hour,
		Members:         []core.Member{{ACI: "a", Name: "Alice", Admin: true}, {ACI: "b", Name: "Bob"}},
		PendingMembers:  []core.Member{{ACI: "c"}},
		RequestingJoin:  []core.Member{{ACI: "d", Name: "Dan"}},
		OnlyAdminsSend:  true,
		InviteLink:      "https://signal.group/#x",
		SelfIsAdmin:     true,
		GroupRevision:   42,
		AddFromLinkMode: core.AccessAdmin,
		// Derived fields: must be ignored on write.
		Unread:         99,
		MentionsUnread: 5,
		Draft:          "ignored",
		LastMessage:    &core.MessagePreview{Text: "ignored"},
	}
	if err := env.db.UpsertChat(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := env.db.GetChat(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := in
	want.Unread, want.MentionsUnread, want.Draft, want.LastMessage = 0, 0, "", nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}

	if _, err := env.db.GetChat(ctx, "u:missing"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing chat err = %v", err)
	}
}

func TestDirectChatContactOverlay(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, core.Chat{ID: "u:alice", Kind: core.ChatDirect, Title: "old"}); err != nil {
		t.Fatal(err)
	}
	c, _ := env.db.GetChat(ctx, "u:alice")
	if c.Title != "old" || c.Contact != nil {
		t.Fatalf("before contact: %+v", c)
	}
	ct := core.Contact{ACI: "alice", ProfileName: "Alice P", Nickname: "Ally", E164: "+491"}
	if err := env.db.UpsertContact(ctx, ct); err != nil {
		t.Fatal(err)
	}
	c, _ = env.db.GetChat(ctx, "u:alice")
	if c.Title != "Ally" || c.Contact == nil || !reflect.DeepEqual(*c.Contact, ct) {
		t.Fatalf("after contact: title %q contact %+v", c.Title, c.Contact)
	}
	// The contact's avatar is the chat's avatar.
	ct.AvatarID = "av1"
	if err := env.db.UpsertContact(ctx, ct); err != nil {
		t.Fatal(err)
	}
	if c, _ = env.db.GetChat(ctx, "u:alice"); c.AvatarID != "av1" {
		t.Errorf("chat avatar %q, want the contact's", c.AvatarID)
	}
	if l, _ := env.db.ListChats(ctx, core.ChatFilter{IncludeEmpty: true}); len(l) != 1 || l[0].AvatarID != "av1" {
		t.Errorf("listed chat avatar %+v", l)
	}
}

func seedChat(t *testing.T, env *testEnv, c core.Chat, lastMsgTs uint64) {
	t.Helper()
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	if lastMsgTs > 0 {
		if _, err := env.db.PutMessage(ctx, msg(c.ID, "peer", lastMsgTs, "hi from "+c.Title)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListChatsOrderingAndFilters(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	seedChat(t, env, core.Chat{ID: "u:a", Kind: core.ChatDirect, Title: "Alice"}, 5000)
	seedChat(t, env, core.Chat{ID: "u:b", Kind: core.ChatDirect, Title: "Bob"}, 9000)
	seedChat(t, env, core.Chat{ID: "g:c", Kind: core.ChatGroup, Title: "Climbing"}, 7000)
	seedChat(t, env, core.Chat{ID: "g:d", Kind: core.ChatGroup, Title: "Dinner", Pinned: true, PinOrder: 2}, 1000)
	seedChat(t, env, core.Chat{ID: "u:e", Kind: core.ChatDirect, Title: "Eve", Pinned: true, PinOrder: 1}, 2000)
	seedChat(t, env, core.Chat{ID: "u:f", Kind: core.ChatDirect, Title: "Frank", Archived: true}, 8000)
	seedChat(t, env, core.Chat{ID: "u:g", Kind: core.ChatDirect, Title: "Grace", Blocked: true}, 8500)
	seedChat(t, env, core.Chat{ID: "u:h", Kind: core.ChatDirect, Title: "Heidi (no messages)"}, 0)
	// Read Bob and Climbing so only some chats are unread.
	for _, id := range []core.ChatID{"u:b", "g:c"} {
		if _, err := env.db.MarkRead(ctx, id, core.MessageID{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.db.PutFolder(ctx, core.Folder{ID: "groups", Name: "Groups", Filter: core.ChatFilter{GroupsOnly: true}}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		f    core.ChatFilter
		want []core.ChatID
	}{
		{"default", core.ChatFilter{}, []core.ChatID{"u:e", "g:d", "u:b", "g:c", "u:a"}},
		{"archived", core.ChatFilter{Archived: true}, []core.ChatID{"u:f"}},
		{"unread", core.ChatFilter{UnreadOnly: true}, []core.ChatID{"u:e", "g:d", "u:a"}},
		{"groups", core.ChatFilter{GroupsOnly: true}, []core.ChatID{"g:d", "g:c"}},
		{"direct", core.ChatFilter{DirectOnly: true}, []core.ChatID{"u:e", "u:b", "u:a"}},
		{"blocked", core.ChatFilter{IncludeBlocked: true}, []core.ChatID{"u:e", "g:d", "u:b", "u:g", "g:c", "u:a"}},
		{"empty", core.ChatFilter{IncludeEmpty: true}, []core.ChatID{"u:e", "g:d", "u:b", "g:c", "u:a", "u:h"}},
		{"query", core.ChatFilter{Query: "clmb"}, []core.ChatID{"g:c"}},
		{"query case", core.ChatFilter{Query: "ALI"}, []core.ChatID{"u:a"}},
		{"folder", core.ChatFilter{FolderID: "groups"}, []core.ChatID{"g:d", "g:c"}},
		{"folder+unread", core.ChatFilter{FolderID: "groups", UnreadOnly: true}, []core.ChatID{"g:d"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := env.db.ListChats(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if ids := chatIDs(got); !slices.Equal(ids, tt.want) {
				t.Errorf("got %v, want %v", ids, tt.want)
			}
		})
	}
	if _, err := env.db.ListChats(ctx, core.ChatFilter{FolderID: "nope"}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown folder err = %v", err)
	}
}

func TestChatPreviewAndCounters(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "bob", ProfileName: "Bob"}); err != nil {
		t.Fatal(err)
	}
	m1 := msg("g:x", "bob", 1000, "first")
	m2 := msg("g:x", "bob", 2000, "hey @￼ look")
	m2.Ranges = []core.BodyRange{{Start: 5, Length: 1, Style: core.StyleMention, MentionACI: "me"}}
	m2.MentionsSelf = true
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "me", ProfileName: "Me", IsSelf: true}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []core.Message{m1, m2} {
		if _, err := env.db.PutMessage(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	c, err := env.db.GetChat(ctx, "g:x")
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != core.ChatGroup {
		t.Errorf("stub kind = %v", c.Kind)
	}
	if c.Unread != 2 || c.MentionsUnread != 1 {
		t.Errorf("unread = %d/%d, want 2/1", c.Unread, c.MentionsUnread)
	}
	want := &core.MessagePreview{ID: m2.ID, Sender: "Bob", Text: "hey @@Me look", At: m2.SentAt}
	if !reflect.DeepEqual(c.LastMessage, want) {
		t.Errorf("preview = %+v, want %+v", c.LastMessage, want)
	}
	// Contact rename shows up in preview sender without rewriting messages.
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "bob", ProfileName: "Robert"}); err != nil {
		t.Fatal(err)
	}
	c, _ = env.db.GetChat(ctx, "g:x")
	if c.LastMessage.Sender != "Robert" {
		t.Errorf("sender = %q, want Robert", c.LastMessage.Sender)
	}
	// Deleting the last message moves the preview back.
	if err := env.db.DeleteMessage(ctx, m2.ID); err != nil {
		t.Fatal(err)
	}
	c, _ = env.db.GetChat(ctx, "g:x")
	if c.LastMessage == nil || c.LastMessage.ID != m1.ID || c.Unread != 1 || c.MentionsUnread != 0 {
		t.Errorf("after delete: %+v unread %d/%d", c.LastMessage, c.Unread, c.MentionsUnread)
	}
	if err := env.db.DeleteMessage(ctx, m1.ID); err != nil {
		t.Fatal(err)
	}
	c, _ = env.db.GetChat(ctx, "g:x")
	if c.LastMessage != nil || c.Unread != 0 {
		t.Errorf("empty chat: %+v unread %d", c.LastMessage, c.Unread)
	}
	if err := env.db.DeleteMessage(ctx, m1.ID); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("double delete err = %v", err)
	}
}

func TestNoteToSelfStub(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "me", IsSelf: true, ProfileName: "Me"}); err != nil {
		t.Fatal(err)
	}
	m := msg(core.DirectChatID("me"), "me", 1000, "note")
	m.Outgoing = true
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	c, err := env.db.GetChat(ctx, core.DirectChatID("me"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != core.ChatNoteToSelf || c.Title != "Note to Self" || c.Unread != 0 {
		t.Errorf("note to self = kind %v title %q unread %d", c.Kind, c.Title, c.Unread)
	}
}

func TestApplyChatUpdate(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	for _, id := range []core.ChatID{"u:a", "u:b", "u:c"} {
		if err := env.db.UpsertChat(ctx, core.Chat{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	yes, no := true, false
	mute := t0.Add(time.Hour)
	timer := time.Hour
	steps := []struct {
		id    core.ChatID
		u     core.ChatUpdate
		check func(c core.Chat) bool
	}{
		{"u:a", core.ChatUpdate{Pinned: &yes}, func(c core.Chat) bool { return c.Pinned && c.PinOrder == 1 }},
		{"u:b", core.ChatUpdate{Pinned: &yes}, func(c core.Chat) bool { return c.Pinned && c.PinOrder == 2 }},
		{"u:a", core.ChatUpdate{Pinned: &yes}, func(c core.Chat) bool { return c.Pinned && c.PinOrder == 1 }},
		{"u:a", core.ChatUpdate{Archived: &yes}, func(c core.Chat) bool { return c.Archived && !c.Pinned && c.PinOrder == 0 }},
		{"u:c", core.ChatUpdate{Pinned: &yes}, func(c core.Chat) bool { return c.PinOrder == 3 }},
		{"u:b", core.ChatUpdate{Pinned: &no}, func(c core.Chat) bool { return !c.Pinned && c.PinOrder == 0 }},
		{"u:c", core.ChatUpdate{MarkedUnread: &yes, MutedUntil: &mute, ExpireTimer: &timer}, func(c core.Chat) bool {
			return c.MarkedUnread && c.MutedUntil.Equal(mute) && c.ExpireTimer == timer && c.Pinned
		}},
	}
	for i, s := range steps {
		c, err := env.db.ApplyChatUpdate(ctx, s.id, s.u)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !s.check(c) {
			t.Errorf("step %d: unexpected chat %+v", i, c)
		}
	}
	if _, err := env.db.ApplyChatUpdate(ctx, "u:zz", core.ChatUpdate{}); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("missing chat err = %v", err)
	}
	// Muted chats are excluded from TotalUnread by default.
	if _, err := env.db.PutMessage(ctx, msg("u:c", "c", 1000, "x")); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.PutMessage(ctx, msg("u:b", "b", 1000, "x")); err != nil {
		t.Fatal(err)
	}
	if n, _ := env.db.TotalUnread(ctx, false); n != 1 {
		t.Errorf("TotalUnread(false) = %d, want 1", n)
	}
	if n, _ := env.db.TotalUnread(ctx, true); n != 2 {
		t.Errorf("TotalUnread(true) = %d, want 2", n)
	}
}

func TestUpdateChat(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertChat(ctx, core.Chat{ID: "g:x", Kind: core.ChatGroup, Title: "x"}); err != nil {
		t.Fatal(err)
	}
	c, err := env.db.UpdateChat(ctx, "g:x", func(c *core.Chat) error {
		c.Title = "renamed"
		c.Members = append(c.Members, core.Member{ACI: "a"})
		return nil
	})
	if err != nil || c.Title != "renamed" || len(c.Members) != 1 {
		t.Fatalf("UpdateChat = %+v, %v", c, err)
	}
	boom := errors.New("boom")
	if _, err := env.db.UpdateChat(ctx, "g:x", func(c *core.Chat) error { c.Title = "no"; return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := env.db.UpdateChat(ctx, "g:x", func(c *core.Chat) error { c.ID = "g:y"; return nil }); err == nil {
		t.Fatal("expected ID change error")
	}
	c, _ = env.db.GetChat(ctx, "g:x")
	if c.Title != "renamed" {
		t.Errorf("title = %q after failed updates", c.Title)
	}
}

func TestDrafts(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.SetDraft(ctx, "u:new", "half a thought"); err != nil {
		t.Fatal(err)
	}
	if d, _ := env.db.GetDraft(ctx, "u:new"); d != "half a thought" {
		t.Errorf("draft = %q", d)
	}
	chats, _ := env.db.ListChats(ctx, core.ChatFilter{})
	if len(chats) != 1 || chats[0].Draft != "half a thought" {
		t.Fatalf("chat with draft should be listed: %+v", chats)
	}
	if err := env.db.SetDraft(ctx, "u:new", ""); err != nil {
		t.Fatal(err)
	}
	if d, _ := env.db.GetDraft(ctx, "u:new"); d != "" {
		t.Errorf("draft after clear = %q", d)
	}
	if chats, _ := env.db.ListChats(ctx, core.ChatFilter{}); len(chats) != 0 {
		t.Errorf("empty chat without draft listed: %v", chatIDs(chats))
	}
}

func TestDeleteChatCascades(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	m := msg("u:a", "a", 1000, "cascade target")
	if _, err := env.db.PutMessage(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.SetReceipt(ctx, m.ID, "a", core.StatusRead); err != nil {
		t.Fatal(err)
	}
	if err := env.db.SetDraft(ctx, "u:a", "draft"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.DeleteChat(ctx, "u:a"); err != nil {
		t.Fatal(err)
	}
	if err := env.db.DeleteChat(ctx, "u:a"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
	for _, tbl := range []string{"warte_messages", "warte_receipts", "warte_drafts"} {
		var n int
		if err := env.db.RawDB().QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s rows = %d, %v", tbl, n, err)
		}
	}
	if hits, _ := env.db.Search(ctx, core.SearchQuery{Text: "cascade"}); len(hits) != 0 {
		t.Errorf("search still finds deleted chat's messages: %d", len(hits))
	}
}

// TestCallAndSystemPreviewHaveNoSender: "Alice Chen started a group call"
// already names the caller, so the chat list must not show
// "Alice Chen: 📞 Alice Chen started …" (neither from the preview itself
// nor from the last-author name overlay in scanChat).
func TestCallAndSystemPreviewHaveNoSender(t *testing.T) {
	env := newTestDB(t)
	ctx := ctxT(t)
	if err := env.db.UpsertContact(ctx, core.Contact{ACI: "alice", ProfileName: "Alice Chen"}); err != nil {
		t.Fatal(err)
	}
	call := msg("g:x", "alice", 1000, "")
	call.Kind = core.KindCall
	call.SenderName = "Alice Chen"
	call.SystemText = "Alice Chen started a group call"
	call.Call = &core.CallInfo{Group: true, Incoming: true}
	if _, err := env.db.PutMessage(ctx, call); err != nil {
		t.Fatal(err)
	}
	c, err := env.db.GetChat(ctx, "g:x")
	if err != nil {
		t.Fatal(err)
	}
	if c.LastMessage == nil || c.LastMessage.Sender != "" || c.LastMessage.Text != "📞 Alice Chen started a group call" {
		t.Fatalf("call preview %+v", c.LastMessage)
	}
	sys := msg("g:x", "alice", 2000, "")
	sys.Kind = core.KindSystem
	sys.SystemText = "Alice Chen changed the group name"
	if _, err := env.db.PutMessage(ctx, sys); err != nil {
		t.Fatal(err)
	}
	if c, _ = env.db.GetChat(ctx, "g:x"); c.LastMessage.Sender != "" {
		t.Errorf("system preview sender %q", c.LastMessage.Sender)
	}
	// A normal message still gets its sender.
	if _, err := env.db.PutMessage(ctx, msg("g:x", "alice", 3000, "hi")); err != nil {
		t.Fatal(err)
	}
	if c, _ = env.db.GetChat(ctx, "g:x"); c.LastMessage.Sender != "Alice Chen" {
		t.Errorf("text preview sender %q", c.LastMessage.Sender)
	}
}
