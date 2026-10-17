package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui/keymap"
)

// Opening a chat from the list starts in the composer, in Insert mode.
func TestOpenChatStartsInInsert(t *testing.T) {
	h := newHarness(t, 120, 40)
	// The chat opened at startup is only a preview: Normal mode, list focus.
	if !h.a.conv.open || h.a.keys.Mode() != keymap.Normal || h.a.focus != FocusChatList {
		t.Fatalf("startup: open=%v mode=%v focus=%v", h.a.conv.open, h.a.keys.Mode(), h.a.focus)
	}
	h.keys("j<enter>")
	if h.a.conv.chat.ID != fake.ChatHike {
		t.Fatalf("opened %v", h.a.conv.chat.ID)
	}
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("enter: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
}

// Chats we can't write to yet open in Normal mode on the conversation.
func TestOpenUnwritableChatStaysNormal(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   core.ChatID
		ok   func(h *harness) bool
	}{
		{"message request", fake.ChatInes, func(h *harness) bool { return h.a.conv.chat.MessageRequest }},
		{"blocked", fake.ChatMax, func(h *harness) bool { return h.a.conv.chat.Blocked }},
		{"announcement-only", fake.ChatTrailAlerts, func(h *harness) bool {
			return h.a.conv.chat.OnlyAdminsSend && !h.a.conv.chat.SelfIsAdmin
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, 120, 40)
			id := tc.id
			h.run(h.a.openChat(id, nil, true), 0)
			if h.a.conv.chat.ID != id || !tc.ok(h) {
				t.Fatalf("fixture: %+v", h.a.conv.chat)
			}
			if h.a.focus != FocusConversation || h.a.keys.Mode() != keymap.Normal {
				t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
			}
		})
	}
}

// Jumping to a specific message (search result, quote) stays in Normal mode.
func TestJumpToMessageStaysNormal(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter><esc>")
	target := h.a.conv.msgs[0].m.ID
	h.keys("H")
	h.run(h.a.openChat(fake.ChatAlice, nil, false), 0)
	h.run(h.a.openChat(fake.ChatHike, &target, true), 0)
	if h.a.conv.chat.ID != fake.ChatHike || h.a.conv.selected().ID != target {
		t.Fatalf("jump: chat %v sel %v", h.a.conv.chat.ID, h.a.conv.selected())
	}
	if h.a.focus != FocusConversation || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
}

// ← moves the cursor; at the very start of the text it leaves Insert mode
// for the chat list, keeping the draft and the selection.
func TestLeftAtStartGoesToChatList(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>ab<left>")
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("left mid-text: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if c := h.a.comp.ta.Column(); c != 1 {
		t.Fatalf("left mid-text: column = %d", c)
	}
	h.keys("<left>")
	if h.a.focus != FocusComposer || h.a.comp.ta.Column() != 0 {
		t.Fatalf("left to col 0: focus=%v column=%d", h.a.focus, h.a.comp.ta.Column())
	}
	h.keys("<left>")
	if h.a.focus != FocusChatList || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("left at start: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if h.a.list.chats[h.a.list.sel].ID != fake.ChatHike || h.a.conv.chat.ID != fake.ChatHike {
		t.Fatalf("selection moved: %v", h.a.list.chats[h.a.list.sel].ID)
	}
	if h.a.comp.ta.Value() != "ab" || h.a.comp.draftSaved != "ab" {
		t.Fatalf("draft: value %q saved %q", h.a.comp.ta.Value(), h.a.comp.draftSaved)
	}
	// Back in: the draft is still there.
	h.keys("<enter>")
	if h.a.keys.Mode() != keymap.Insert || h.a.comp.ta.Value() != "ab" {
		t.Fatalf("reopen: mode=%v value=%q", h.a.keys.Mode(), h.a.comp.ta.Value())
	}
}

// An empty composer is at its start: ← goes straight to the list.
func TestLeftInEmptyComposer(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><left>")
	if h.a.focus != FocusChatList || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
}

// ← at the start of the second line goes to the end of the first.
func TestLeftAtLineStartWraps(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>ab<A-CR>cd<left><left>")
	if h.a.comp.ta.Line() != 1 || h.a.comp.ta.Column() != 0 {
		t.Fatalf("setup: line %d col %d", h.a.comp.ta.Line(), h.a.comp.ta.Column())
	}
	h.keys("<left>")
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if h.a.comp.ta.Line() != 0 || h.a.comp.ta.Column() != 2 {
		t.Fatalf("line %d col %d", h.a.comp.ta.Line(), h.a.comp.ta.Column())
	}
}

// With the completion popup open, ← never leaves the composer.
func TestLeftWithPopupStaysInComposer(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>@ali")
	if h.a.comp.comp.kind != compMention {
		t.Fatal("no mention popup")
	}
	h.a.comp.ta.SetCursorColumn(0)
	h.keys("<left>")
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if h.a.comp.comp.kind != compMention || h.a.comp.ta.Value() != "@ali" {
		t.Fatalf("popup %v value %q", h.a.comp.comp.kind, h.a.comp.ta.Value())
	}
}

// Narrow layout: ← at the start shows the chat list pane.
func TestNarrowLeftShowsChatList(t *testing.T) {
	h := newHarness(t, 80, 24)
	h.keys("<enter>")
	_ = h.view()
	if h.a.layout.conv.w != 80 || h.a.focus != FocusComposer {
		t.Fatalf("enter: focus=%v layout %+v", h.a.focus, h.a.layout)
	}
	h.keys("<left>")
	_ = h.view()
	if h.a.focus != FocusChatList || h.a.layout.list.w != 80 {
		t.Fatalf("left: focus=%v layout %+v", h.a.focus, h.a.layout)
	}
}

// Mouse: a click on a chat previews it (wide); clicking the open chat again,
// or clicking a chat in the narrow layout, goes into it in Insert mode.
func TestClickChatEntersInsert(t *testing.T) {
	click := func(h *harness, index int) {
		t.Helper()
		_ = h.view()
		for _, z := range h.a.zones.zones {
			if z.kind == zoneChat && z.index == index {
				h.send(tea.MouseClickMsg{X: z.x + 2, Y: z.y, Button: tea.MouseLeft})
				return
			}
		}
		t.Fatalf("no zone for chat %d", index)
	}
	mouse := withConfig(func(c *config.Config) { c.Mouse = true })

	h := newHarness(t, 120, 40, mouse)
	click(h, 1)
	if h.a.conv.chat.ID != h.a.list.chats[1].ID || h.a.focus != FocusChatList || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("first click: chat %v focus=%v mode=%v", h.a.conv.chat.ID, h.a.focus, h.a.keys.Mode())
	}
	click(h, 1)
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("second click: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}

	n := newHarness(t, 70, 24, mouse)
	click(n, 1)
	if n.a.conv.chat.ID != n.a.list.chats[1].ID || n.a.focus != FocusComposer || n.a.keys.Mode() != keymap.Insert {
		t.Fatalf("narrow click: chat %v focus=%v mode=%v", n.a.conv.chat.ID, n.a.focus, n.a.keys.Mode())
	}
}

// ↑ on the first line of the composer selects the newest message; on a
// later line it moves the cursor up.
func TestUpOnFirstLineSelectsMessages(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>ab<A-CR>cd<up>")
	if h.a.focus != FocusComposer || h.a.comp.ta.Line() != 0 {
		t.Fatalf("up from line 2: focus=%v line=%d", h.a.focus, h.a.comp.ta.Line())
	}
	h.keys("<up>")
	if h.a.focus != FocusConversation || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("up on line 1: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if h.a.conv.sel != h.a.conv.last() {
		t.Fatalf("sel = %d, want newest %d", h.a.conv.sel, h.a.conv.last())
	}
	if h.a.comp.draftSaved != "ab\ncd" {
		t.Fatalf("draft %q", h.a.comp.draftSaved)
	}
	// Further ↑ walks back through the messages.
	h.keys("<up>")
	if h.a.conv.sel != h.a.conv.last()-1 {
		t.Fatalf("second up: sel = %d", h.a.conv.sel)
	}
}

// With the completion popup open, ↑ moves in the popup.
func TestUpWithPopupStaysInComposer(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>@ali")
	if h.a.comp.comp.kind != compMention {
		t.Fatal("no mention popup")
	}
	h.keys("<up>")
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
}

// ↓ on the newest message returns to the composer in Insert mode; j does not.
func TestDownPastNewestEntersComposer(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>hi<up><up>")
	if h.a.focus != FocusConversation || h.a.conv.sel != h.a.conv.last()-1 {
		t.Fatalf("setup: focus=%v sel=%d", h.a.focus, h.a.conv.sel)
	}
	h.keys("<down>")
	if h.a.focus != FocusConversation || h.a.conv.sel != h.a.conv.last() {
		t.Fatalf("down: focus=%v sel=%d", h.a.focus, h.a.conv.sel)
	}
	h.keys("j")
	if h.a.focus != FocusConversation {
		t.Fatalf("j on newest left the list: focus=%v", h.a.focus)
	}
	h.keys("<down>")
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("down on newest: focus=%v mode=%v", h.a.focus, h.a.keys.Mode())
	}
	if h.a.comp.ta.Value() != "hi" {
		t.Fatalf("draft %q", h.a.comp.ta.Value())
	}
}
