package info

import (
	"errors"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

func overlaySizes(t *testing.T, name string, o ui.Overlay) {
	t.Helper()
	for _, sz := range sizes {
		w, h := o.Size(sz[0], sz[1])
		if w > sz[0] || h > sz[1] || w <= 0 || h <= 0 {
			t.Errorf("%s: Size(%d,%d) = %d,%d", name, sz[0], sz[1], w, h)
		}
		checkBox(t, name, o.View(w, h), w, h)
		checkBox(t, name+" max", o.View(sz[0], sz[1]), sz[0], sz[1])
	}
	_ = o.Title()
	_ = o.KeyContext()
	if f, ok := o.(ui.Footer); ok {
		_ = f.Footer()
	}
}

func findMsg(t *testing.T, msgs []core.Message, pred func(core.Message) bool) core.Message {
	t.Helper()
	for i := len(msgs) - 1; i >= 0; i-- {
		if pred(msgs[i]) {
			return msgs[i]
		}
	}
	t.Fatal("message not found")
	return core.Message{}
}

func TestMessageDetailsGroupEdits(t *testing.T) {
	ctx, svc := newCtx(t)
	g := findChat(t, svc, "Warte Wanderers")
	m := findMsg(t, messages(t, svc, g.ID), func(m core.Message) bool { return len(m.EditHistory) == 2 })
	var o ui.Overlay = NewMessageDetails(ctx, m)
	o, ext := feedOverlay(t, o, o.Init())
	if len(ext) != 0 {
		t.Fatalf("init emitted %#v", ext)
	}
	d := o.(*MessageDetails)
	if d.chat == nil {
		t.Fatal("chat not loaded")
	}
	v := plain(strings.Join(d.render(70), "\n"))
	for _, want := range []string{"TIMELINE", "Sent", "Server", "Edited", "Pinned", "EDIT HISTORY · 3 versions", "v1", "v2", "v3", "current", "original", "Packing list"} {
		if !strings.Contains(v, want) {
			t.Errorf("details missing %q:\n%s", want, v)
		}
	}
	if m.Outgoing && len(m.PerRecipient) > 0 {
		// Recipients are listed by name, grouped by status.
		if !strings.Contains(v, "Alice Chen") {
			t.Errorf("recipient names missing:\n%s", v)
		}
	}
	overlaySizes(t, "details", o)

	// Scrolling clamps; K closes; enter jumps to the message.
	o.View(60, 10)
	for range 200 {
		o, _ = o.Update(act(keymap.ActCursorDown))
	}
	if d.offset != len(d.lines)-10 {
		t.Errorf("offset %d, lines %d", d.offset, len(d.lines))
	}
	o, _ = o.Update(act(keymap.ActCursorTop))
	if d.offset != 0 {
		t.Error("gg should scroll to top")
	}
	o2, cmd := o.Update(act(keymap.ActItemSelect))
	if o2 != nil {
		t.Error("enter should close")
	}
	if got := execCmd(t, cmd); len(got) != 1 || got[0].(ui.OpenChatMsg).Focus == nil || *got[0].(ui.OpenChatMsg).Focus != m.ID {
		t.Errorf("enter = %#v", got)
	}
	if o3, _ := o.Update(act(keymap.ActMessageInfo)); o3 != nil {
		t.Error("K should close")
	}
}

func TestMessageDetailsOutgoingRecipients(t *testing.T) {
	ctx, svc := newCtx(t)
	g := findChat(t, svc, "Warte Wanderers")
	m := findMsg(t, messages(t, svc, g.ID), func(m core.Message) bool { return m.Outgoing && len(m.PerRecipient) > 0 })
	m.PerRecipient = map[core.ACI]core.DeliveryStatus{}
	statuses := []core.DeliveryStatus{core.StatusRead, core.StatusDelivered, core.StatusViewed, core.StatusFailed}
	for i, mem := range g.Members {
		if mem.ACI != ctx.Self.ACI {
			m.PerRecipient[mem.ACI] = statuses[i%len(statuses)]
		}
	}
	m.PerRecipient["stranger"] = core.StatusSent
	m.Reactions = []core.Reaction{{Emoji: "❤️", From: ctx.Self.ACI, At: testNow.Add(-time.Hour)}, {Emoji: "👍", From: g.Members[1].ACI, FromName: g.Members[1].Name}}
	o := NewMessageDetails(ctx, m)
	feedOverlay(t, o, o.Init())
	o.msg = m // keep the synthetic statuses
	o.dirty = true
	v := plain(o.View(64, 200))
	for _, want := range []string{"READ", "DELIVERED", "SENT", "Unknown", "REACTIONS · 2", "You"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	// An update event refreshes the overlay.
	m2 := m
	m2.Body = "completely new body"
	o.Update(ui.EventMsg{Event: core.MessageUpdated{Message: m2}})
	if !strings.Contains(plain(o.View(64, 200)), "completely new body") {
		t.Error("update event not applied")
	}
	// Removal closes it.
	if o2, _ := o.Update(ui.EventMsg{Event: core.MessageRemoved{ID: m.ID}}); o2 != nil {
		t.Error("removed message should close details")
	}
}

func TestMessageDetailsAttachmentsAndTimer(t *testing.T) {
	ctx, svc := newCtx(t)
	alice := findChat(t, svc, "Alice Chen")
	m := findMsg(t, messages(t, svc, alice.ID), func(m core.Message) bool { return len(m.Attachments) > 0 && m.Attachments[0].Width > 0 })
	o := NewMessageDetails(ctx, m)
	v := plain(o.View(70, 200))
	for _, want := range []string{"ATTACHMENTS · 1", "Type", "Size", "Dimensions", "×"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	noor := findChat(t, svc, "Noor")
	m = findMsg(t, messages(t, svc, noor.ID), func(m core.Message) bool { return m.ExpiresIn > 0 })
	m.ExpireStartedAt = testNow.Add(-2 * 24 * time.Hour)
	o = NewMessageDetails(ctx, m)
	v = plain(o.View(70, 200))
	for _, want := range []string{"DISAPPEARING", "1 week", "Expires", "left"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	overlaySizes(t, "timer details", o)
}

func TestMessageDetailsEmptyAndHostile(t *testing.T) {
	ctx, _ := newCtx(t)
	evil := "x\x1b[2J\x1b]8;;http://evil\x07y\u202e"
	m := core.Message{
		ID:          core.MessageID{Chat: "g:zz", Author: "a", Timestamp: 1},
		SenderName:  evil,
		Body:        evil,
		EditHistory: []core.EditRevision{{Body: evil + " old"}},
		Attachments: []core.Attachment{{FileName: evil, ContentType: evil, Caption: evil}},
		Reactions:   []core.Reaction{{Emoji: evil, FromName: evil}},
	}
	o := NewMessageDetails(ctx, m)
	v := o.View(60, 100)
	for _, bad := range []string{"\x1b[2J", "\x07", "\u202e"} {
		if strings.Contains(v, bad) {
			t.Errorf("unsanitized %q", bad)
		}
	}
	overlaySizes(t, "hostile", o)
	overlaySizes(t, "empty", NewMessageDetails(ctx, core.Message{}))
	ctx.Selected = func() *core.Message { return nil }
	if NewMessageDetailsForSelected(ctx) != nil {
		t.Error("no selection should give nil")
	}
}

func TestWordDiff(t *testing.T) {
	d := wordDiff("the quick brown fox", "the slow brown fox jumps")
	var ins, del []string
	for _, tok := range d {
		switch tok.op {
		case opIns:
			ins = append(ins, strings.TrimSpace(tok.text))
		case opDel:
			del = append(del, strings.TrimSpace(tok.text))
		}
	}
	if strings.Join(del, "|") != "quick" || strings.Join(ins, "|") != "slow|jumps" {
		t.Errorf("del=%q ins=%q", del, ins)
	}
	// Reconstructing both sides from the diff must give the inputs.
	a, b := "", ""
	for _, tok := range wordDiff("a b\nc  d", "a x\nc d e") {
		if tok.op != opIns {
			a += tok.text
		}
		if tok.op != opDel {
			b += tok.text
		}
	}
	if a != "a b\nc  d" || b != "a x\nc d e" {
		t.Errorf("roundtrip a=%q b=%q", a, b)
	}
	if got := wordDiff("", ""); len(got) != 0 {
		t.Errorf("empty diff = %v", got)
	}
	big := strings.Repeat("w ", 1000)
	if got := wordDiff(big, big+"x"); len(got) != 2 {
		t.Errorf("huge diff should degrade to replace, got %d tokens", len(got))
	}
}

func TestCallLog(t *testing.T) {
	ctx, svc := newCtx(t)
	var o ui.Overlay = NewCallLog(ctx)
	o, _ = feedOverlay(t, o, o.Init())
	c := o.(*CallLog)
	if len(c.list) != 8 {
		t.Fatalf("calls = %d", len(c.list))
	}
	v := plain(o.View(64, 40))
	for _, want := range []string{"answer them there", "All", "Missed 2", "Mama", "Missed voice call", "Incoming voice call · 41m", "Outgoing video call · 23m 14s", "Declined", "group"} {
		if !strings.Contains(v, want) {
			t.Errorf("call log missing %q:\n%s", want, v)
		}
	}
	// Newest first.
	for i := 1; i < len(c.list); i++ {
		if c.list[i].Call.StartedAt.After(c.list[i-1].Call.StartedAt) {
			t.Error("not sorted newest first")
		}
	}
	overlaySizes(t, "calls", o)

	// Tab filters missed calls.
	o, _ = o.Update(key("tab"))
	if len(c.list) != 2 || !c.missed {
		t.Fatalf("missed filter: %d", len(c.list))
	}
	o, _ = o.Update(act(keymap.ActFocusNext))
	if len(c.list) != 8 {
		t.Fatal("tab again shows all")
	}
	o, _ = o.Update(act(keymap.ActCursorDown))
	o, _ = o.Update(key("j"))
	sel, _ := c.Selected()
	o2, cmd := o.Update(key("enter"))
	if o2 != nil {
		t.Error("enter should close the call log")
	}
	if got := execCmd(t, cmd); len(got) != 1 || got[0] != (ui.OpenChatMsg{Chat: sel.Chat}) {
		t.Errorf("enter = %#v", got)
	}
	// Call events reload.
	if _, cmd := o.Update(ui.EventMsg{Event: core.IncomingCall{Ringing: false}}); cmd == nil {
		t.Error("call event should reload")
	}
	// The real backend reports call changes as message/chat updates (ring
	// → missed, call event and call log syncs).
	for _, ev := range []core.Event{
		core.MessageUpdated{Message: core.Message{Kind: core.KindCall}},
		core.MessageRemoved{},
		core.ChatUpdated{},
	} {
		if _, cmd := o.Update(ui.EventMsg{Event: ev}); cmd == nil {
			t.Errorf("%T should reload", ev)
		}
	}
	if _, cmd := o.Update(ui.EventMsg{Event: core.MessageUpdated{Message: core.Message{Kind: core.KindNormal}}}); cmd != nil {
		t.Error("ordinary message updates should not reload")
	}
	_ = svc
}

func TestCallLogEmptyAndError(t *testing.T) {
	ctx, _ := newCtx(t)
	o := NewCallLog(ctx)
	overlaySizes(t, "calls loading", o)
	o.Update(callsLoaded{})
	if !strings.Contains(plain(o.View(50, 10)), "No calls yet") {
		t.Error("empty state")
	}
	o.Update(act(keymap.ActItemSelect))
	o.Update(act(keymap.ActCursorRight))
	if !strings.Contains(plain(o.View(50, 10)), "No missed calls") {
		t.Error("empty missed state")
	}
	o.Update(callsLoaded{err: errors.New("boom\x1b[2J")})
	v := o.View(60, 10)
	if !strings.Contains(plain(v), "boom") || strings.Contains(v, "\x1b[2J") {
		t.Error("error state")
	}
	overlaySizes(t, "calls error", o)
	ctx.Svc = nil
	if NewCallLog(ctx).Init() != nil {
		t.Error("no service: no load")
	}
}

func TestPinned(t *testing.T) {
	ctx, svc := newCtx(t)
	g := findChat(t, svc, "Warte Wanderers")
	msgs := messages(t, svc, g.ID)
	pins := PinnedFrom(msgs)
	if len(pins) != 1 {
		t.Fatalf("pins = %d", len(pins))
	}
	strip := RenderPinnedStrip(ctx, &g, pins, 0, 60)
	if !strings.Contains(plain(strip), "Packing list") {
		t.Errorf("strip = %q", plain(strip))
	}
	for _, sz := range sizes {
		checkBox(t, "strip", RenderPinnedStrip(ctx, &g, pins, 5, sz[0]), sz[0], 1)
	}
	if RenderPinnedStrip(ctx, nil, nil, 0, 50) != "" {
		t.Error("no pins → empty strip")
	}

	var o ui.Overlay = NewPinnedMessages(ctx, g.ID, msgs)
	o, _ = feedOverlay(t, o, o.Init())
	p := o.(*PinnedMessages)
	if len(p.Pinned()) != 1 {
		t.Fatalf("overlay pins = %d", len(p.Pinned()))
	}
	overlaySizes(t, "pinned", o)
	_, cmd := o.Update(act(keymap.ActItemSelect))
	got := execCmd(t, cmd)
	if oc, ok := got[0].(ui.OpenChatMsg); !ok || *oc.Focus != pins[0].ID {
		t.Errorf("enter = %#v", got)
	}
	// p unpins through the service.
	o, cmd = o.Update(key("p"))
	o, ext := feedOverlay(t, o, cmd)
	if len(p.Pinned()) != 0 {
		t.Errorf("unpin failed: %d pins, ext=%#v", len(p.Pinned()), ext)
	}
	if !strings.Contains(plain(o.View(50, 6)), "No pinned messages") {
		t.Error("empty state after unpin")
	}
	if left := PinnedFrom(messages(t, svc, g.ID)); len(left) != 0 {
		t.Error("service still has the pin")
	}
	// A pin event adds it back.
	m := pins[0]
	m.Pinned = true
	o.Update(ui.EventMsg{Event: core.MessageUpdated{Message: m}})
	if len(p.Pinned()) != 1 {
		t.Error("pin event not applied")
	}
	o.Update(ui.EventMsg{Event: core.MessageRemoved{ID: m.ID}})
	if len(p.Pinned()) != 0 {
		t.Error("remove event not applied")
	}
}

func TestPinnedLoadsWithoutWindow(t *testing.T) {
	ctx, svc := newCtx(t)
	pt := findChat(t, svc, "Platform Team")
	var o ui.Overlay = NewPinnedMessages(ctx, pt.ID, nil)
	overlaySizes(t, "pinned loading", o)
	o, _ = feedOverlay(t, o, o.Init())
	if n := len(o.(*PinnedMessages).Pinned()); n != 1 {
		t.Errorf("pins = %d", n)
	}
	if !strings.Contains(plain(o.View(60, 10)), "On-call rotation") {
		t.Error("pinned body missing")
	}
}

func TestConnection(t *testing.T) {
	ctx, _ := newCtx(t)
	var o ui.Overlay = NewConnection(ctx, core.ConnLoggedOut, errors.New("device unlinked"))
	if o.Init() != nil {
		t.Error("logged out screen does not animate")
	}
	v := plain(o.View(100, 30))
	for _, want := range []string{"logged out", "Linked devices", "Link new device", "warte link", "device unlinked"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q:\n%s", want, v)
		}
	}
	overlaySizes(t, "logged out", o)
	_, cmd := o.Update(act(keymap.ActItemSelect))
	if got := execCmd(t, cmd); len(got) != 1 || got[0] != (RelinkMsg{}) {
		t.Errorf("enter = %#v", got)
	}
	handled, cmd := o.(ui.BackHandler).HandleBack()
	if !handled || cmd == nil {
		t.Error("back should quit while logged out")
	}

	o = NewConnection(ctx, core.ConnDisconnected, nil)
	cmd = o.Init()
	if cmd == nil {
		t.Fatal("spinner should tick")
	}
	c := o.(*Connection)
	o, _ = o.Update(connTick{c.tick})
	if c.frame != 1 {
		t.Error("tick should advance the spinner")
	}
	o.Update(connTick{-1})
	if c.frame != 1 {
		t.Error("stale tick advanced the spinner")
	}
	if !strings.Contains(plain(o.View(80, 24)), "Can't reach Signal") {
		t.Error("disconnected headline")
	}
	overlaySizes(t, "disconnected", o)
	if handled, _ := o.(ui.BackHandler).HandleBack(); handled {
		t.Error("esc should dismiss when only disconnected")
	}
	o.Update(ui.EventMsg{Event: core.LoggedOut{Err: errors.New("expired")}})
	if c.State() != core.ConnLoggedOut {
		t.Error("LoggedOut event should switch state")
	}
	if o2, _ := o.Update(ui.EventMsg{Event: core.ConnChanged{State: core.ConnConnected}}); o2 != nil {
		t.Error("connected should close the screen")
	}
	ctx.Config.ReduceMotion = true
	if NewConnection(ctx, core.ConnConnecting, nil).Init() != nil {
		t.Error("reduce motion: no spinner")
	}
}

func TestRegistered(t *testing.T) {
	ctx, svc := newCtx(t)
	if _, ok := ui.ActionHandlerFor(keymap.ActCalls); !ok {
		t.Error("calls action not registered")
	}
	names := map[string]ui.Command{}
	for _, c := range ui.Commands() {
		names[c.Name] = c
	}
	for _, n := range []string{"calls", "pinned", "details", "connection"} {
		if _, ok := names[n]; !ok {
			t.Errorf("command %q not registered", n)
		}
	}
	// :pinned without a chat warns; with one it pushes.
	if got := execCmd(t, names["pinned"].Run(ctx, nil)); len(got) != 1 {
		t.Errorf("pinned without chat = %#v", got)
	} else if _, ok := got[0].(ui.ToastMsg); !ok {
		t.Errorf("pinned without chat = %#v", got)
	}
	g := findChat(t, svc, "Warte")
	ctx.Chat = func() core.ChatID { return g.ID }
	if got := execCmd(t, names["pinned"].Run(ctx, nil)); len(got) != 1 {
		t.Errorf("pinned = %#v", got)
	} else if _, ok := got[0].(ui.PushOverlayMsg); !ok {
		t.Errorf("pinned = %#v", got)
	}
	m := messages(t, svc, g.ID)[0]
	ctx.Selected = func() *core.Message { return &m }
	if got := execCmd(t, names["details"].Run(ctx, nil)); len(got) != 1 {
		t.Errorf("details = %#v", got)
	} else if _, ok := got[0].(ui.PushOverlayMsg); !ok {
		t.Errorf("details = %#v", got)
	}
	if got := execCmd(t, names["connection"].Run(ctx, nil)); len(got) != 1 {
		t.Errorf("connection = %#v", got)
	}
}
