package finder

import (
	"context"
	"strings"
	"testing"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

func resetRecents() {
	recents.Lock()
	recents.ids = nil
	recents.Unlock()
}

func chatsOf(o ui.Overlay) *Chats { return o.(*Chats) }

func firstTitle(c *Chats) string {
	if len(c.results) == 0 {
		return ""
	}
	return c.results[0].e.title
}

func TestChatSwitcherLists(t *testing.T) {
	resetRecents()
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	c := chatsOf(o)
	if len(c.results) != 13 {
		t.Fatalf("empty query shows %d chats, want 13 (no archived, no contacts)", len(c.results))
	}
	out := plain(o, 72, 26)
	for _, want := range []string{"Alice Chen", "Note to Self", "Search chats"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	for _, r := range c.results {
		if r.e.kind != entryChat || r.e.chat.Archived {
			t.Errorf("contact/archived entry %q shown without a query", r.e.title)
		}
	}
	if !strings.Contains(out, "haha yes") && !strings.Contains(out, "You:") && !strings.Contains(out, ": ") {
		t.Errorf("no snippets in two-line rows:\n%s", out)
	}
	checkView(t, o)
}

func TestChatSwitcherFuzzy(t *testing.T) {
	resetRecents()
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "bkclb")
	if got := firstTitle(chatsOf(o)); !strings.HasPrefix(got, "Book club") {
		t.Fatalf("first match = %q, want Book club", got)
	}
	// The matched characters are highlighted with the FuzzyMatch style.
	view := o.View(72, 26)
	if !strings.Contains(view, ctx.Styles.FuzzyMatch.Render("B")) && !strings.Contains(view, "\x1b[") {
		t.Errorf("no highlighting in view")
	}
	// Archived chats are found by query.
	o, _ = send(t, o, "ctrl+u")
	o = typeText(t, o, "marco")
	if got := firstTitle(chatsOf(o)); got != "Marco Rossi" {
		t.Fatalf("archived chat: first = %q", got)
	}
	checkView(t, o)
}

func TestChatSwitcherUsernameAndPhone(t *testing.T) {
	resetRecents()
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "alice.07")
	c := chatsOf(o)
	if firstTitle(c) != "Alice Chen" || c.results[0].field != 1 {
		t.Fatalf("username match: first = %q field %d", firstTitle(c), c.results[0].field)
	}
	if len(c.results) < 2 || c.results[1].e.kind != entryStart {
		t.Fatalf("expected start pseudo-entry second, got %+v", c.results)
	}
	if !strings.Contains(plain(o, 72, 26), "@alice.07") {
		t.Errorf("matched username not shown:\n%s", plain(o, 72, 26))
	}
	o, _ = send(t, o, "ctrl+u")
	o = typeText(t, o, "12340002")
	if got := firstTitle(chatsOf(o)); got != "Bob Okafor" {
		t.Fatalf("phone match: first = %q", got)
	}
}

func TestChatSwitcherOpensChat(t *testing.T) {
	resetRecents()
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "noor")
	o, msgs := send(t, o, "enter")
	if o != nil {
		t.Fatal("overlay should close on enter")
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs = %#v", msgs)
	}
	open := msgs[0].(ui.OpenChatMsg)
	if open.Chat != core.DirectChatID("a00b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5c") || open.Focus != nil {
		t.Fatalf("opened %+v", open)
	}
	// It is now the most recent chat.
	o = push(t, NewChatSwitcher(ctx))
	if got := firstTitle(chatsOf(o)); got != "Noor Haddad" {
		t.Fatalf("recent chat not boosted: first = %q", got)
	}
}

func TestChatSwitcherAlternate(t *testing.T) {
	resetRecents()
	ctx, svc := testCtx(t)
	chats, _ := svc.Chats(context.Background(), core.ChatFilter{IncludeEmpty: true})
	NoteRecent(chats[3].ID)
	NoteRecent(chats[5].ID)
	ctx.Chat = func() core.ChatID { return chats[5].ID }
	o := push(t, NewChatSwitcher(ctx))
	c := chatsOf(o)
	if c.results[0].e.chat.ID != chats[5].ID || c.sel != 1 || c.results[1].e.chat.ID != chats[3].ID {
		t.Fatalf("alternate chat not preselected: sel=%d first=%q second=%q", c.sel, firstTitle(c), c.results[1].e.title)
	}
	_, msgs := send(t, o, "enter")
	if msgs[0].(ui.OpenChatMsg).Chat != chats[3].ID {
		t.Fatalf("enter should open the previous chat")
	}
}

func TestChatSwitcherContactStartsChat(t *testing.T) {
	resetRecents()
	ctx, svc := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "bob okafor")
	c := chatsOf(o)
	if firstTitle(c) != "Bob Okafor" || c.results[0].e.kind != entryContact {
		t.Fatalf("contact entry not first: %q", firstTitle(c))
	}
	if !strings.Contains(plain(o, 72, 26), "new chat") {
		t.Errorf("contact row lacks hint:\n%s", plain(o, 72, 26))
	}
	o, msgs := send(t, o, "enter")
	if o != nil || len(msgs) != 1 {
		t.Fatalf("closed=%v msgs=%#v", o == nil, msgs)
	}
	id := msgs[0].(ui.OpenChatMsg).Chat
	if _, err := svc.Chat(context.Background(), id); err != nil {
		t.Fatalf("chat %s not created: %v", id, err)
	}
}

func TestChatSwitcherStartByPhone(t *testing.T) {
	resetRecents()
	ctx, svc := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "+4917699999999")
	c := chatsOf(o)
	if len(c.results) == 0 || c.results[0].e.kind != entryStart {
		t.Fatalf("start entry not first")
	}
	if !strings.Contains(plain(o, 72, 26), "Start chat with +4917699999999") {
		t.Errorf("view:\n%s", plain(o, 72, 26))
	}
	o, msgs := send(t, o, "enter")
	if o != nil {
		t.Fatal("should close after resolving")
	}
	id := msgs[0].(ui.OpenChatMsg).Chat
	ch, err := svc.Chat(context.Background(), id)
	if err != nil || ch.Contact == nil || ch.Contact.E164 != "+4917699999999" {
		t.Fatalf("resolved chat = %+v, %v", ch, err)
	}

	// An invalid number keeps the overlay open with an error and a toast.
	o = push(t, NewChatSwitcher(ctx))
	o = typeText(t, o, "+00000")
	if chatsOf(o).results[0].e.kind != entryStart {
		t.Fatalf("expected start entry")
	}
	o, msgs = send(t, o, "enter")
	if o == nil {
		t.Fatal("closed on error")
	}
	if len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("msgs = %#v", msgs)
	}
	checkView(t, o)
}

func TestChatSwitcherNavigation(t *testing.T) {
	resetRecents()
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	c := chatsOf(o)
	o.View(72, 26)
	steps := []struct {
		key  string
		want int
	}{
		{"ctrl+n", 1}, {"down", 2}, {"ctrl+p", 1}, {"up", 0}, {"up", 0},
		{"esc", 0}, // normal mode
		{"j", 1}, {"j", 2}, {"k", 1}, {"G", 12}, {"g", 12}, {"g", 0},
		{"ctrl+d", 6},
	}
	for _, s := range steps {
		o, _ = send(t, o, s.key)
		if o == nil {
			t.Fatalf("closed at %q", s.key)
		}
		if c.sel != s.want {
			t.Fatalf("after %q sel = %d, want %d", s.key, c.sel, s.want)
		}
	}
	if !c.m.normal {
		t.Fatal("esc should enter normal mode")
	}
	// j in normal mode must not edit the query.
	if c.Query() != "" {
		t.Fatalf("query edited in normal mode: %q", c.Query())
	}
	o, _ = send(t, o, "i")
	o = typeText(t, o, "j")
	if c.Query() != "j" {
		t.Fatalf("insert mode should type: %q", c.Query())
	}
	o, _ = send(t, o, "esc")
	o, _ = send(t, o, "q")
	if o != nil {
		t.Fatal("q in normal mode should close")
	}
	o = push(t, NewChatSwitcher(ctx))
	if o, _ = send(t, o, "ctrl+c"); o != nil {
		t.Fatal("ctrl+c should close")
	}
}

func TestChatSwitcherEventsAndSanitize(t *testing.T) {
	resetRecents()
	ctx, svc := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	alice := core.ACI("a11ce5a1-7c3b-4d2e-8f6a-1b9c0d2e3f41")
	if err := svc.SetNickname(context.Background(), alice, "Evil\x1b[31mRed\x07\u202eX"); err != nil {
		t.Fatal(err)
	}
	next, cmd := o.Update(ui.EventMsg{Event: core.ContactUpdated{}})
	o, _ = drain(t, next, cmd)
	o = typeText(t, o, "evil")
	view := o.View(72, 26)
	if strings.Contains(view, "\x1b[31m") || strings.Contains(view, "\x07") || strings.Contains(view, "\u202e") {
		t.Fatalf("unsanitized name rendered: %q", view)
	}
	if !strings.Contains(plain(o, 72, 26), "EvilRed") {
		t.Fatalf("renamed contact not found:\n%s", plain(o, 72, 26))
	}
}

func TestChatSwitcherEmpty(t *testing.T) {
	ctx, _ := testCtx(t)
	ctx.Svc = nil
	o := push(t, NewChatSwitcher(ctx))
	checkView(t, o)
	o = typeText(t, o, "zzz")
	checkView(t, o)
	if o, _ = send(t, o, "enter"); o == nil {
		t.Fatal("enter with no results should not close")
	}
	if !strings.Contains(plain(o, 60, 20), "No chats") {
		t.Errorf("empty state missing:\n%s", plain(o, 60, 20))
	}
}

func TestStartCandidate(t *testing.T) {
	for s, want := range map[string]bool{
		"+4917612345678":           true,
		"+49 176 1234 5678":        true,
		"0176 1234567":             true,
		"@alice.07":                true,
		"alice.07":                 true,
		"https://signal.me/#eu/xy": true,
		"alice":                    false,
		"12":                       false,
		"book club":                false,
	} {
		if got := startCandidate(s); got != want {
			t.Errorf("startCandidate(%q) = %v", s, got)
		}
	}
}
