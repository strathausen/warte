package info

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// loadSidebar sets chat and applies the load result.
func loadSidebar(t *testing.T, s *Sidebar, id core.ChatID) {
	t.Helper()
	for _, m := range execCmd(t, s.SetChat(id)) {
		s.Update(m)
	}
	if s.Chat() == nil {
		t.Fatalf("chat %s not loaded", id)
	}
}

func sidebarMsgs(t *testing.T, s *Sidebar, msg tea.Msg) []tea.Msg {
	t.Helper()
	_, cmd := s.Update(msg)
	return execCmd(t, cmd)
}

func TestSidebarDirect(t *testing.T) {
	ctx, svc := newCtx(t, withImages(true))
	alice := findChat(t, svc, "Alice Chen")
	s := NewSidebar(ctx)
	s.Focus()
	loadSidebar(t, s, alice.ID)

	v := plain(s.View(40, 80))
	for _, want := range []string{"Alice Chen", "alice.07", "+4915112340001", "Verified", "probably on a mountain",
		"MEDIA", "ACTIONS", "Search in chat", "Mute notifications", "Disappearing messages", "Safety number", "Block",
		"GROUPS IN COMMON", "Warte Wanderers"} {
		if !strings.Contains(v, want) {
			t.Errorf("sidebar missing %q:\n%s", want, v)
		}
	}
	if s.total == 0 || len(s.media) == 0 || len(s.media) > MaxMedia {
		t.Errorf("media = %d (total %d)", len(s.media), s.total)
	}

	// The first action is selected initially.
	if it := s.items[s.sel]; it.kind != itemAction || it.action != actSearch {
		t.Fatalf("initial selection %+v", it)
	}
	msgs := sidebarMsgs(t, s, act(keymap.ActItemSelect))
	if len(msgs) != 1 || msgs[0] != (ui.RunCommandMsg{Line: "search"}) {
		t.Fatalf("enter on search = %#v", msgs)
	}

	// Mute opens the duration picker, which emits ":mute 8h".
	s.Update(act(keymap.ActCursorDown))
	msgs = sidebarMsgs(t, s, key("enter"))
	if len(msgs) != 1 {
		t.Fatalf("mute = %#v", msgs)
	}
	push, ok := msgs[0].(ui.PushOverlayMsg)
	if !ok {
		t.Fatalf("mute = %#v", msgs[0])
	}
	o := push.Overlay
	checkBox(t, "mute picker", o.View(30, 5), 30, 5)
	o, _ = o.Update(key("j"))
	o, cmd := o.Update(key("enter"))
	if o != nil {
		t.Error("picker should close on enter")
	}
	if got := execCmd(t, cmd); len(got) != 1 || got[0] != (ui.RunCommandMsg{Line: "mute 8h"}) {
		t.Errorf("picker emitted %#v", got)
	}

	// Disappearing opens the timer picker.
	s.Update(act(keymap.ActCursorDown))
	msgs = sidebarMsgs(t, s, act(keymap.ActItemSelect))
	push, ok = msgs[0].(ui.PushOverlayMsg)
	if !ok {
		t.Fatalf("timer = %#v", msgs)
	}
	if !strings.Contains(plain(push.Overlay.View(40, 8)), "current") {
		t.Error("timer picker should mark the current timer")
	}
	_, cmd = push.Overlay.Update(key("7"))
	if got := execCmd(t, cmd); len(got) != 1 || got[0] != (ui.RunCommandMsg{Line: "disappear 1w"}) {
		t.Errorf("timer picker emitted %#v", got)
	}

	// Safety number and Block.
	s.Update(act(keymap.ActCursorDown))
	if got := sidebarMsgs(t, s, act(keymap.ActItemSelect)); got[0] != (ui.RunCommandMsg{Line: SafetyNumberCommand}) {
		t.Errorf("safety = %#v", got)
	}
	s.Update(act(keymap.ActCursorDown))
	if got := sidebarMsgs(t, s, act(keymap.ActItemSelect)); got[0] != (ui.RunCommandMsg{Line: "block"}) {
		t.Errorf("block = %#v", got)
	}

	// Next: shared group opens it.
	s.Update(act(keymap.ActCursorDown))
	got := sidebarMsgs(t, s, act(keymap.ActItemSelect))
	if oc, ok := got[0].(ui.OpenChatMsg); !ok || !oc.Chat.IsGroup() {
		t.Errorf("group = %#v", got)
	}

	// gg goes to the media strip; l moves the thumbnail; enter opens it.
	s.Update(act(keymap.ActCursorTop))
	if s.items[s.sel].kind != itemMedia {
		t.Fatalf("top item = %+v", s.items[s.sel])
	}
	s.Update(act(keymap.ActCursorRight))
	got = sidebarMsgs(t, s, act(keymap.ActItemSelect))
	oc, ok := got[0].(ui.OpenChatMsg)
	if !ok || oc.Focus == nil || *oc.Focus != s.media[1].msg {
		t.Errorf("media = %#v", got)
	}
	if !strings.Contains(s.View(40, 80), "▔") {
		t.Error("selected thumbnail marker missing")
	}
}

func TestSidebarGroup(t *testing.T) {
	ctx, svc := newCtx(t)
	g := findChat(t, svc, "Warte Wanderers")
	s := NewSidebar(ctx)
	loadSidebar(t, s, g.ID)
	v := plain(s.View(36, 80))
	for _, want := range []string{"Warte Wanderers", "Group · 5 members", "Ridges", "Invite link", "On",
		"MEMBERS · 5", "You", "admin", "Group settings"} {
		if !strings.Contains(v, want) {
			t.Errorf("group sidebar missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "Safety number") {
		t.Error("groups have no safety number action")
	}
	// You are listed first.
	ms := s.sortedMembers()
	if ms[0].ACI != ctx.Self.ACI {
		t.Errorf("first member = %+v", ms[0])
	}
	// Group settings action.
	for i, it := range s.items {
		if it.kind == itemAction && it.action == actGroupSettings {
			s.sel = i
		}
	}
	if got := sidebarMsgs(t, s, act(keymap.ActItemSelect)); got[0] != (ui.RunCommandMsg{Line: GroupSettingsCommand}) {
		t.Errorf("group settings = %#v", got)
	}
	// Selecting a member opens a direct chat; selecting yourself does nothing.
	s.Update(act(keymap.ActCursorBottom))
	got := sidebarMsgs(t, s, act(keymap.ActItemSelect))
	if len(got) != 1 {
		t.Fatalf("member = %#v", got)
	}
	if oc := got[0].(ui.OpenChatMsg); !oc.Chat.IsDirect() {
		t.Errorf("member chat = %v", oc.Chat)
	}
}

func TestSidebarEventsAndMute(t *testing.T) {
	ctx, svc := newCtx(t)
	alice := findChat(t, svc, "Alice Chen")
	s := NewSidebar(ctx)
	s.Focus()
	loadSidebar(t, s, alice.ID)
	c := *s.Chat()
	c.MutedUntil = testNow.Add(8 * time.Hour)
	c.ExpireTimer = 24 * time.Hour
	s.Update(ui.EventMsg{Event: core.ChatUpdated{Chat: c}})
	v := plain(s.View(40, 80))
	if !strings.Contains(v, "Unmute") || !strings.Contains(v, "Until") || !strings.Contains(v, "1 day") {
		t.Errorf("mute/timer not reflected:\n%s", v)
	}
	// The selection stays on the same action after a rebuild.
	if s.items[s.sel].action != actSearch {
		t.Errorf("selection moved to %+v", s.items[s.sel])
	}
	s.Update(act(keymap.ActCursorDown))
	if got := sidebarMsgs(t, s, act(keymap.ActItemSelect)); got[0] != (ui.RunCommandMsg{Line: "unmute"}) {
		t.Errorf("unmute = %#v", got)
	}
	// An image message reloads the media strip.
	cmd := func() tea.Cmd {
		_, c := s.Update(ui.EventMsg{Event: core.MessageAdded{Message: core.Message{ID: core.MessageID{Chat: alice.ID, Timestamp: 1}, Attachments: []core.Attachment{{ContentType: "image/png"}}}}})
		return c
	}()
	if cmd == nil {
		t.Error("image message should refresh")
	}
	// Removing the chat clears the sidebar.
	s.Update(ui.EventMsg{Event: core.ChatRemoved{ID: alice.ID}})
	if s.ChatID() != "" || !strings.Contains(plain(s.View(30, 5)), "No chat selected") {
		t.Error("removed chat should clear the sidebar")
	}
	// A stale load result is ignored.
	cmdOld := s.SetChat(alice.ID)
	s.SetChat(findChat(t, svc, "Mama").ID)
	for _, m := range execCmd(t, cmdOld) {
		s.Update(m)
	}
	if s.Chat() != nil {
		t.Error("stale load applied")
	}
}

func TestSidebarSizesAllChats(t *testing.T) {
	for _, th := range []string{"charm", "mono", "charm-light"} {
		for _, imgs := range []bool{false, true} {
			ctx, svc := newCtx(t, withTheme(th), withImages(imgs))
			for _, c := range allChats(t, svc) {
				s := NewSidebar(ctx)
				s.Focus()
				loadSidebar(t, s, c.ID)
				for _, sz := range sizes {
					checkBox(t, th+" "+c.Title, s.View(sz[0], sz[1]), sz[0], sz[1])
					// Walk the whole list; the selection must stay visible.
					s.Update(act(keymap.ActCursorBottom))
					v := s.View(sz[0], sz[1])
					checkBox(t, th+" "+c.Title+" bottom", v, sz[0], sz[1])
					s.Update(act(keymap.ActCursorTop))
				}
			}
		}
	}
}

func TestSidebarEmpty(t *testing.T) {
	ctx, _ := newCtx(t)
	ctx.Svc = nil
	s := NewSidebar(ctx)
	for _, sz := range sizes {
		checkBox(t, "empty", s.View(sz[0], sz[1]), sz[0], sz[1])
	}
	if s.View(0, 0) != "" || s.View(10, 0) != "" {
		t.Error("zero size should render nothing")
	}
	// Keys on an empty sidebar are harmless.
	for _, a := range []keymap.Action{keymap.ActCursorDown, keymap.ActItemSelect, keymap.ActCursorRight} {
		if _, cmd := s.Update(act(a)); cmd != nil {
			t.Errorf("%s on empty sidebar returned a command", a)
		}
	}
	if s.SetChat("u:nobody") != nil {
		t.Error("no service: no load")
	}
	if !strings.Contains(plain(s.View(30, 4)), "Loading") {
		t.Error("expected loading state")
	}
}

func TestSidebarSanitizes(t *testing.T) {
	ctx, _ := newCtx(t)
	s := NewSidebar(ctx)
	s.chatID = "u:evil"
	s.gen = 1
	evil := "Eve\x1b[2J\x1b]52;c;aGk=\x07\u202e"
	s.Update(sidebarLoaded{gen: 1, chat: core.Chat{
		ID: "u:evil", Title: evil, Description: evil,
		Contact: &core.Contact{ACI: "evil", ProfileName: evil, About: evil, Username: evil},
	}, shared: []core.Chat{{ID: "g:x", Title: evil}}})
	v := s.View(40, 40)
	for _, bad := range []string{"\x1b[2J", "\x1b]52", "\x07", "\u202e"} {
		if strings.Contains(v, bad) {
			t.Errorf("unsanitized %q in output", bad)
		}
	}
	checkBox(t, "evil", v, 40, 40)
}
