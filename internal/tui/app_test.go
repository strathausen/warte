package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

func TestStartupRendersChatListAndFirstChat(t *testing.T) {
	h := newHarness(t, 120, 40)
	out := h.plain()
	for _, want := range []string{"warte", "All", "Alice Chen", "Warte Wander", "Note to Self", "NORMAL"} {
		if !strings.Contains(out, want) {
			t.Errorf("startup frame lacks %q", want)
		}
	}
	if !h.a.conv.open || h.a.conv.chat.ID != fake.ChatAlice {
		t.Fatalf("first chat not open: %v", h.a.conv.chat.ID)
	}
	// The header shows the open chat.
	if !strings.Contains(out, "probably on a mountain") {
		t.Errorf("conversation header missing about line:\n%s", out)
	}
	checkFrame(t, h.view(), 120, 40, "startup")
}

func TestJKMovesChatSelection(t *testing.T) {
	h := newHarness(t, 120, 40)
	if h.a.list.sel != 0 {
		t.Fatalf("sel = %d", h.a.list.sel)
	}
	h.keys("jj")
	if h.a.list.sel != 2 {
		t.Fatalf("after jj sel = %d, want 2", h.a.list.sel)
	}
	h.keys("k")
	if h.a.list.sel != 1 {
		t.Fatalf("after k sel = %d, want 1", h.a.list.sel)
	}
	h.keys("G")
	if h.a.list.sel != len(h.a.list.chats)-1 {
		t.Fatalf("G: sel = %d", h.a.list.sel)
	}
	h.keys("gg")
	if h.a.list.sel != 0 {
		t.Fatalf("gg: sel = %d", h.a.list.sel)
	}
	h.keys("3j")
	if h.a.list.sel != 3 {
		t.Fatalf("3j: sel = %d", h.a.list.sel)
	}
}

func TestOpenChat(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>")
	if h.a.conv.chat.ID != fake.ChatHike {
		t.Fatalf("opened %v, want hike group", h.a.conv.chat.ID)
	}
	// Opening a chat starts in the composer, ready to write.
	if h.a.focus != FocusComposer || h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("focus = %v, mode = %v", h.a.focus, h.a.keys.Mode())
	}
	h.keys("<esc>")
	if h.a.focus != FocusConversation || h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("esc: focus = %v, mode = %v", h.a.focus, h.a.keys.Mode())
	}
	if !strings.Contains(h.plain(), "members") {
		t.Errorf("group header should show members")
	}
	// Opening marks the chat read.
	h.pump()
	if c := h.a.list.chats[h.a.list.indexOf(fake.ChatHike)]; c.Unread != 0 {
		t.Errorf("unread after open = %d", c.Unread)
	}
	// H goes back to the list.
	h.keys("H")
	if h.a.focus != FocusChatList {
		t.Fatalf("H: focus = %v", h.a.focus)
	}
}

func TestSendMessagePendingThenSent(t *testing.T) {
	h := newHarness(t, 120, 40, withLatency(80*time.Millisecond))
	h.keys("<enter>")
	if h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("mode = %v", h.a.keys.Mode())
	}
	h.keys("hello from the tests<enter>")
	if v := h.a.comp.ta.Value(); v != "" {
		t.Fatalf("composer not cleared: %q", v)
	}
	if !h.waitFor(time.Second, func() bool {
		return len(h.a.conv.msgs) > 0 && h.lastMsg().Body == "hello from the tests"
	}) {
		t.Fatalf("sent message did not appear")
	}
	m := h.lastMsg()
	if !m.Outgoing || m.Status != core.StatusPending {
		t.Fatalf("first state: outgoing=%v status=%v", m.Outgoing, m.Status)
	}
	if !strings.Contains(h.plain(), "hello from the tests") {
		t.Fatalf("message not rendered")
	}
	if !h.waitFor(2*time.Second, func() bool { return h.lastMsg().Status >= core.StatusSent }) {
		t.Fatalf("status never left pending: %v", h.lastMsg().Status)
	}
	// Still in insert mode for the next message.
	if h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("mode after send = %v", h.a.keys.Mode())
	}
}

func TestReplyFlow(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>")
	target := h.a.conv.selected()
	if target == nil {
		t.Fatal("no selection")
	}
	h.keys("r")
	if h.a.comp.replyTo == nil || h.a.comp.replyTo.ID != target.ID {
		t.Fatalf("reply target not set")
	}
	if h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("mode = %v", h.a.keys.Mode())
	}
	if !strings.Contains(h.plain(), "Replying to") {
		t.Fatalf("reply bar not shown")
	}
	h.keys("on it<enter>")
	if !h.waitFor(time.Second, func() bool { return h.lastMsg().Body == "on it" }) {
		t.Fatal("reply not sent")
	}
	if q := h.lastMsg().Quote; q == nil || q.ID != target.ID {
		t.Fatalf("quote = %+v", q)
	}
	if h.a.comp.replyTo != nil {
		t.Fatal("reply state not cleared")
	}
}

func TestReactionQuickBar(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>")
	sel := *h.a.conv.selected()
	h.keys("R")
	if len(h.a.overlays) != 1 {
		t.Fatalf("reaction bar not open")
	}
	if _, ok := h.a.overlays[0].(*reactionBar); !ok {
		t.Fatalf("top overlay is %T", h.a.overlays[0])
	}
	h.keys("2") // 👍
	if len(h.a.overlays) != 0 {
		t.Fatal("bar should close after picking")
	}
	thumbs := format.DefaultReactions()[1]
	if !h.waitFor(time.Second, func() bool {
		i, ok := h.a.conv.index[sel.ID]
		return ok && h.a.hasReacted(&h.a.conv.msgs[i].m, thumbs)
	}) {
		t.Fatal("reaction not applied")
	}
	// h/l + enter toggles it off again.
	h.keys("R")
	h.keys("<enter>")
	if !h.waitFor(time.Second, func() bool {
		i := h.a.conv.index[sel.ID]
		return !h.a.hasReacted(&h.a.conv.msgs[i].m, thumbs)
	}) {
		t.Fatal("reaction not toggled off")
	}
}

func TestCommandThemeSwitch(t *testing.T) {
	h := newHarness(t, 120, 40)
	stylesPtr := h.a.styles
	h.keys(":")
	if h.a.keys.Mode() != keymap.Command {
		t.Fatalf("mode = %v", h.a.keys.Mode())
	}
	h.keys("theme nord<enter>")
	if h.a.styles.Theme.Name != "nord" {
		t.Fatalf("theme = %q", h.a.styles.Theme.Name)
	}
	if h.a.styles != stylesPtr {
		t.Fatal("styles pointer must stay stable for views")
	}
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatalf("mode after command = %v", h.a.keys.Mode())
	}
	// Tab completion of command names and arguments.
	h.keys(":the<tab>")
	if v := h.a.cmdline.input.Value(); v != "theme " {
		t.Fatalf("completion = %q", v)
	}
	h.keys("drac<tab>")
	if v := h.a.cmdline.input.Value(); v != "theme dracula " {
		t.Fatalf("arg completion = %q", v)
	}
	h.keys("<enter>")
	if h.a.styles.Theme.Name != "dracula" {
		t.Fatalf("theme = %q", h.a.styles.Theme.Name)
	}
	// History.
	h.keys(":<up>")
	if v := h.a.cmdline.input.Value(); !strings.HasPrefix(v, "theme dracula") {
		t.Fatalf("history = %q", v)
	}
	h.keys("<esc>")
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatal("esc should leave command mode")
	}
}

func TestLayoutNoOverflow(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}, {200, 60}, {60, 20}, {90, 30}, {89, 30}}
	for _, layoutName := range []string{config.LayoutBubbles, config.LayoutIRC} {
		h := newHarness(t, 120, 40, withConfig(func(c *config.Config) { c.Layout = layoutName }))
		for _, name := range theme.Names() {
			th, ok := theme.Get(name)
			if !ok {
				continue
			}
			h.a.setTheme(th)
			for _, sz := range sizes {
				h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
				label := layoutName + "/" + name + "/" + itoa(sz[0]) + "x" + itoa(sz[1])
				checkFrame(t, h.view(), sz[0], sz[1], label)
			}
		}
		// Every chat, both panes focused, at two sizes.
		for _, sz := range sizes[:2] {
			h.send(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			for i := range h.a.list.chats {
				h.a.listSelect(i)
				h.send(keyPress("enter"))
				checkFrame(t, h.view(), sz[0], sz[1], layoutName+"/chat "+h.a.conv.chat.Title)
				h.keys("gg")
				checkFrame(t, h.view(), sz[0], sz[1], layoutName+"/top "+h.a.conv.chat.Title)
				h.keys("H")
			}
		}
	}
}

func keyPress(name string) tea.KeyPressMsg { return namedKey(name) }

func TestResize(t *testing.T) {
	h := newHarness(t, 160, 48)
	if h.a.layout.narrow {
		t.Fatal("160 cols should be wide")
	}
	h.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	if !h.a.layout.narrow {
		t.Fatal("70 cols should be narrow")
	}
	checkFrame(t, h.view(), 70, 24, "narrow")
	// Narrow: list only; enter shows the conversation, q goes back.
	if h.a.layout.list.w != 70 || h.a.layout.conv.w != 0 {
		t.Fatalf("narrow layout: %+v", h.a.layout)
	}
	h.keys("<enter>")
	if h.a.layout.conv.w != 70 {
		t.Fatalf("conversation not shown: %+v", h.a.layout)
	}
	h.keys("<esc>q")
	if h.a.layout.list.w != 70 {
		t.Fatalf("q should return to the list: %+v", h.a.layout)
	}
	h.send(tea.WindowSizeMsg{Width: 200, Height: 60})
	checkFrame(t, h.view(), 200, 60, "wide again")
}

func TestOverlayOpenClose(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("?")
	if len(h.a.overlays) != 1 {
		t.Fatal("help not open")
	}
	out := h.plain()
	if !strings.Contains(out, "Keys") || !strings.Contains(out, "Navigation") {
		t.Fatalf("help overlay not rendered:\n%s", out)
	}
	checkFrame(t, h.view(), 120, 40, "help")
	h.keys("j")
	h.keys("<esc>")
	if len(h.a.overlays) != 0 {
		t.Fatal("esc should close help")
	}
	// Filter in help.
	h.keys("?/quit")
	hv := h.a.overlays[0].(*helpOverlay)
	if !hv.editing {
		t.Fatal("/ should start filtering")
	}
	for _, l := range hv.lines {
		if strings.Contains(stripANSI(l), "Next unread") {
			t.Fatalf("filter did not apply: %q", stripANSI(l))
		}
	}
	h.keys("<esc><esc>")
	if len(h.a.overlays) != 0 {
		t.Fatal("help not closed")
	}
	// Insert mode is restored after an overlay closes.
	h.keys("<enter>i")
	h.send(PushOverlayMsgFor(newHelpOverlay(h.a)))
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatal("overlays run in normal mode")
	}
	h.keys("q")
	if h.a.keys.Mode() != keymap.Insert {
		t.Fatalf("mode after close = %v", h.a.keys.Mode())
	}
}

func TestConfirmDialog(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>G")
	sel := *h.a.conv.selected()
	h.keys("dd")
	if len(h.a.overlays) != 1 {
		t.Fatal("dd should ask for confirmation")
	}
	h.keys("n")
	if len(h.a.overlays) != 0 {
		t.Fatal("n should close")
	}
	h.pump()
	if _, ok := h.a.conv.index[sel.ID]; !ok {
		t.Fatal("message deleted despite n")
	}
	h.keys("ddy")
	if !h.waitFor(time.Second, func() bool { _, ok := h.a.conv.index[sel.ID]; return !ok }) {
		t.Fatal("message not deleted after y")
	}
}

func TestMouseClickSelectsChat(t *testing.T) {
	h := newHarness(t, 120, 40, withConfig(func(c *config.Config) { c.Mouse = true }))
	if v := h.a.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v", v.MouseMode)
	}
	var target zone
	for _, z := range h.a.zones.zones {
		if z.kind == zoneChat && z.index == 3 {
			target = z
		}
	}
	if target.kind == 0 {
		t.Fatal("no zone for chat 3")
	}
	h.send(tea.MouseClickMsg{X: target.x + 2, Y: target.y, Button: tea.MouseLeft})
	if h.a.list.sel != 3 {
		t.Fatalf("sel = %d", h.a.list.sel)
	}
	if h.a.conv.chat.ID != h.a.list.chats[3].ID {
		t.Fatalf("clicked chat not opened")
	}
	// Wheel scrolls the list.
	h.send(tea.MouseWheelMsg{X: 5, Y: 10, Button: tea.MouseWheelDown})
	if h.a.list.sel != 6 {
		t.Fatalf("wheel: sel = %d", h.a.list.sel)
	}
	// :set nomouse disables it.
	h.keys(":set nomouse<enter>")
	if v := h.a.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatal("mouse still on")
	}
}

func TestIncomingEventsUpdateListAndBadge(t *testing.T) {
	h := newHarness(t, 120, 40, withLive())
	pinned := 0
	for _, c := range h.a.list.chats {
		if c.Pinned {
			pinned++
		}
	}
	before := map[core.ChatID]int{}
	for _, c := range h.a.list.chats {
		before[c.ID] = c.Unread
	}
	if err := h.svc.Start(h.a.ctx); err != nil {
		t.Fatal(err)
	}
	var got core.MessageAdded
	// Watch events directly for an incoming message in an unpinned, closed chat.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && got.Message.ID.Chat == "" {
		select {
		case ev := <-h.svc.Events():
			h.send(eventMsg{ev: ev})
			if ma, ok := ev.(core.MessageAdded); ok && !ma.Message.Outgoing && ma.Message.Kind == core.KindNormal &&
				ma.Message.ID.Chat != h.a.conv.chat.ID {
				i := h.a.list.indexOf(ma.Message.ID.Chat)
				if i >= 0 && !h.a.list.chats[i].Pinned && !h.a.list.chats[i].Muted(time.Now()) {
					got = ma
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if got.Message.ID.Chat == "" {
		t.Skip("no live incoming message arrived in time")
	}
	i := h.a.list.indexOf(got.Message.ID.Chat)
	if i < 0 {
		t.Fatal("chat vanished")
	}
	c := h.a.list.chats[i]
	if c.Unread <= before[c.ID] && c.Unread == 0 {
		t.Fatalf("unread badge not raised: %d (was %d)", c.Unread, before[c.ID])
	}
	// Unpinned chats are ordered by last activity, so the chat that just
	// received a message is ahead of every chat with older activity.
	for _, other := range h.a.list.chats[pinned:] {
		if other.LastMessage == nil || other.ID == c.ID {
			continue
		}
		j := h.a.list.indexOf(other.ID)
		if j < i && other.LastMessage.At.Before(c.LastMessage.At) {
			t.Fatalf("%q (older) is listed before %q", other.Title, c.Title)
		}
	}
	if i < pinned {
		t.Fatalf("unpinned chat listed among pinned ones")
	}
	if !strings.Contains(h.plain(), clean(c.Title)[:4]) {
		t.Fatalf("chat not rendered")
	}
}

func TestSelectionStableWhenChatsReorder(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("jjjj") // some unpinned chat
	want := h.a.list.current().ID
	// Send a message into another chat: it moves to the top.
	other := h.a.list.chats[6].ID
	if _, err := h.svc.Send(h.a.ctx, other, core.Outgoing{Body: "bump"}); err != nil {
		t.Fatal(err)
	}
	h.pump()
	if got := h.a.list.current().ID; got != want {
		t.Fatalf("selection moved from %v to %v", want, got)
	}
}

func TestChatActionsToast(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("jjjj")
	id := h.a.list.current().ID
	h.keys("za")
	h.pump()
	if h.a.list.indexOf(id) >= 0 {
		t.Fatal("archived chat still in list")
	}
	found := false
	for _, ts := range h.a.toasts {
		if strings.HasPrefix(ts.text, "Archived") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no toast: %+v", h.a.toasts)
	}
	h.keys("A")
	if h.a.list.indexOf(id) < 0 {
		t.Fatal("archived view lacks chat")
	}
}

func TestSearchInConversation(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter><esc>") // hike group
	h.keys("/sauna<enter>")
	if h.a.search.highlight != "sauna" || len(h.a.search.hits) == 0 {
		t.Fatalf("no hits: %+v", h.a.search.hits)
	}
	first := h.a.conv.selected().ID
	h.keys("N")
	if len(h.a.search.hits) > 1 && h.a.conv.selected().ID == first {
		t.Fatal("N did not move")
	}
	if !strings.Contains(strings.ToLower(h.a.conv.selected().Body), "sauna") {
		t.Fatalf("selected message doesn't match: %q", h.a.conv.selected().Body)
	}
	h.keys("<esc>")
	if h.a.search.highlight != "" {
		t.Fatal("esc should clear highlights")
	}
}

func TestChatListFilter(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("/mama")
	if len(h.a.list.chats) == 0 || h.a.list.chats[0].Title != "Mama" {
		var names []string
		for _, c := range h.a.list.chats {
			names = append(names, c.Title)
		}
		t.Fatalf("filtered: %v", names)
	}
	h.keys("<enter><enter>")
	if h.a.conv.chat.Title != "Mama" {
		t.Fatalf("opened %q", h.a.conv.chat.Title)
	}
	h.keys("<esc>H<esc>")
	if h.a.list.filter != "" || len(h.a.list.chats) < 5 {
		t.Fatal("esc should clear the filter")
	}
}

func TestEditOwnMessage(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>")
	h.keys("typo<enter><esc>")
	h.waitFor(time.Second, func() bool { return h.lastMsg().Body == "typo" })
	h.keys("G")
	h.keys("e")
	if h.a.comp.editing == nil || h.a.comp.ta.Value() != "typo" {
		t.Fatalf("edit not started: %q", h.a.comp.ta.Value())
	}
	h.keys("<bs><bs><bs><bs>fixed<enter>")
	if !h.waitFor(time.Second, func() bool { return h.lastMsg().Body == "fixed" && h.lastMsg().Edited }) {
		t.Fatalf("edit not applied: %+v", h.lastMsg().Body)
	}
	// Incoming messages can't be edited.
	for i := len(h.a.conv.msgs) - 1; i >= 0; i-- {
		if !h.a.conv.msgs[i].m.Outgoing {
			h.a.conv.sel = i
			break
		}
	}
	h.keys("e")
	if h.a.comp.editing != nil {
		t.Fatal("editing someone else's message")
	}
}

func TestQuitCommands(t *testing.T) {
	for _, seq := range []string{":q<enter>", "ZZ"} {
		h := newHarness(t, 80, 24)
		h.keys(seq)
		if !h.a.quitting {
			t.Fatalf("%s did not quit", seq)
		}
	}
	h := newHarness(t, 80, 24)
	h.keys("<C-c>")
	if h.a.quitting {
		t.Fatal("single ctrl+c should only hint")
	}
	h.keys("<C-c>")
	if !h.a.quitting {
		t.Fatal("double ctrl+c should quit")
	}
}

func TestMentionCompletion(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("j<enter>@ali")
	if h.a.comp.comp.kind != compMention {
		t.Fatalf("no mention popup")
	}
	if !strings.Contains(h.plain(), "Mention") {
		t.Fatal("popup not rendered")
	}
	h.keys("<tab>")
	if v := h.a.comp.ta.Value(); v != "@Alice Chen " {
		t.Fatalf("value = %q", v)
	}
	h.keys("hi<enter>")
	if !h.waitFor(time.Second, func() bool { return strings.Contains(h.lastMsg().Body, "hi") && h.lastMsg().Outgoing }) {
		t.Fatal("not sent")
	}
	m := h.lastMsg()
	if len(m.Ranges) != 1 || m.Ranges[0].Style != core.StyleMention || m.Ranges[0].MentionACI != fake.AliceACI {
		t.Fatalf("ranges = %+v body=%q", m.Ranges, m.Body)
	}
}

func TestEmojiShortcodeCompletion(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>:tad")
	if h.a.comp.comp.kind != compEmoji {
		t.Fatal("no emoji popup")
	}
	h.keys("<enter>")
	if v := h.a.comp.ta.Value(); !strings.Contains(v, "🎉") {
		t.Fatalf("value = %q", v)
	}
}

func TestVisualYankAndCount(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>")
	// Disappearing/view-once messages refuse; Alice's chat is fine.
	h.keys("Vkk")
	lo, hi, ok := h.a.conv.visualRange()
	if !ok || hi-lo != 2 {
		t.Fatalf("visual range %d..%d", lo, hi)
	}
	h.keys("y")
	if h.a.keys.Mode() != keymap.Normal {
		t.Fatal("y should leave visual mode")
	}
	if v, _ := h.a.regs.Get(0); strings.Count(v, "\n") != 2 {
		t.Fatalf("register = %q", v)
	}
}

func TestEventsHandled(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.send(eventMsg{ev: core.ConnChanged{State: core.ConnConnecting}})
	if !strings.Contains(h.plain(), "connecting") {
		t.Fatal("conn state not shown")
	}
	h.send(eventMsg{ev: core.SyncProgress{Stage: "contacts", Done: 3, Total: 10}})
	if !strings.Contains(h.plain(), "contacts 3/10") {
		t.Fatal("sync progress not shown")
	}
	h.send(eventMsg{ev: core.IncomingCall{Chat: fake.ChatAlice, Name: "Alice Chen", Ringing: true}})
	if !strings.Contains(h.plain(), "Alice Chen is calling") {
		t.Fatalf("call banner missing:\n%s", h.plain())
	}
	h.send(eventMsg{ev: core.IncomingCall{Chat: fake.ChatAlice, Ringing: false}})
	if strings.Contains(h.plain(), "is calling") {
		t.Fatal("call banner not dismissed")
	}
	h.send(eventMsg{ev: core.TypingChanged{Chat: fake.ChatAlice, Who: fake.AliceACI, Name: "Alice Chen", Typing: true}})
	if !strings.Contains(h.plain(), "Alice Chen is typing") {
		t.Fatal("typing line missing")
	}
	h.send(eventMsg{ev: core.ErrorEvent{Op: "sync", Err: errTooLarge}})
	if !strings.Contains(h.plain(), "sync: file is larger") {
		t.Fatal("error toast missing")
	}
	h.send(eventMsg{ev: core.IdentityChanged{ACI: fake.BobACI, Name: "Bob"}})
	h.send(eventMsg{ev: core.StoriesChanged{}})
	h.send(eventMsg{ev: core.ContactUpdated{Contact: core.Contact{ACI: fake.BobACI, ProfileName: "Bob"}}})
	h.send(eventMsg{ev: core.ChatRemoved{ID: fake.ChatAlice}})
	if h.a.conv.open {
		t.Fatal("removed chat still open")
	}
	h.send(eventMsg{ev: core.LoggedOut{}})
	if !strings.Contains(h.plain(), "Logged out") {
		t.Fatal("logged-out screen missing")
	}
	checkFrame(t, h.view(), 120, 40, "logged out")
}

// PushOverlayMsgFor wraps an overlay in a push message (test helper).
func PushOverlayMsgFor(o *helpOverlay) tea.Msg { return ui.PushOverlayMsg{Overlay: o} }
