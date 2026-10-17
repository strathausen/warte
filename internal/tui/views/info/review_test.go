package info

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// failingSvc returns errors from every query the views use.
type failingSvc struct{ core.Service }

var errBackend = errors.New("backend \x1b[31mdown\x07")

func (failingSvc) Chat(context.Context, core.ChatID) (core.Chat, error) {
	return core.Chat{}, errBackend
}
func (failingSvc) Chats(context.Context, core.ChatFilter) ([]core.Chat, error) {
	return nil, errBackend
}
func (failingSvc) Message(context.Context, core.MessageID) (core.Message, error) {
	return core.Message{}, errBackend
}
func (failingSvc) Messages(context.Context, core.ChatID, core.MessageID, int) ([]core.Message, error) {
	return nil, errBackend
}
func (failingSvc) CallLog(context.Context) ([]core.CallLogEntry, error) { return nil, errBackend }
func (failingSvc) PinMessage(context.Context, core.MessageID, bool, time.Duration) error {
	return errBackend
}

func noEscapesLeak(t *testing.T, name, v string) {
	t.Helper()
	if strings.Contains(v, "\x07") || strings.Contains(v, "[31mdown") {
		t.Errorf("%s: control characters leaked: %q", name, v)
	}
}

func TestServiceErrors(t *testing.T) {
	ctx, svc := newCtx(t)
	alice := findChat(t, svc, "Alice Chen")
	msgs := messages(t, svc, alice.ID)
	ctx.Svc = failingSvc{svc}

	s := NewSidebar(ctx)
	for _, m := range execCmd(t, s.SetChat(alice.ID)) {
		s.Update(m)
	}
	v := s.View(40, 20)
	checkBox(t, "sidebar err", v, 40, 20)
	if !strings.Contains(plain(v), "Couldn't load chat") {
		t.Errorf("sidebar error state:\n%s", plain(v))
	}
	noEscapesLeak(t, "sidebar", v)
	// Actions on an unloaded sidebar are no-ops.
	if _, cmd := s.Update(act(keymap.ActItemSelect)); cmd != nil {
		t.Error("select on error state should do nothing")
	}

	var o ui.Overlay = NewCallLog(ctx)
	o, _ = feedOverlay(t, o, o.Init())
	v = o.View(60, 20)
	if !strings.Contains(plain(v), "Couldn't load calls") {
		t.Errorf("call log error:\n%s", plain(v))
	}
	noEscapesLeak(t, "calls", v)
	overlaySizes(t, "calls err", o)

	pinnedCopy := append([]core.Message(nil), msgs...)
	pinnedCopy[len(pinnedCopy)-1].Pinned = true
	p := NewPinnedMessages(ctx, alice.ID, pinnedCopy)
	o, _ = feedOverlay(t, p, p.Init())
	overlaySizes(t, "pinned err", o)
	if n := len(p.Pinned()); n > 0 {
		// Unpin failure surfaces as an error toast and keeps the entry.
		o2, cmd := o.Update(key("p"))
		o2, out := feedOverlay(t, o2, cmd)
		if o2 == nil || len(p.Pinned()) != n {
			t.Error("failed unpin removed the entry")
		}
		if len(out) != 1 {
			t.Errorf("unpin failure = %#v", out)
		}
	} else {
		t.Error("pinned list from loaded messages lost after load error")
	}

	d := NewMessageDetails(ctx, msgs[len(msgs)-1])
	o, _ = feedOverlay(t, d, d.Init())
	if o == nil {
		t.Fatal("details closed on load error")
	}
	overlaySizes(t, "details err", o)
}

// TestWideNames renders every view with CJK, emoji ZWJ sequences and
// combining characters in names and bodies.
func TestWideNames(t *testing.T) {
	ctx, svc := newCtx(t, withImages(false))
	wide := "王小明🙂‍↔️👩🏽‍💻 Ǹ̵̡é̷ 日本語のとても長い名前です"
	g := findChat(t, svc, "Warte Wanderers")
	g.Title = wide
	g.Description = wide + "\n" + wide
	for i := range g.Members {
		g.Members[i].Name = wide
	}
	s := NewSidebar(ctx)
	s.Focus()
	loadSidebar(t, s, g.ID)
	s.Update(ui.EventMsg{Event: core.ChatUpdated{Chat: g}})
	for _, w := range []int{16, 23, 31, 40, 57} {
		for range 6 {
			s.Update(act(keymap.ActCursorDown))
			checkBox(t, "wide group", s.View(w, 30), w, 30)
		}
	}

	alice := findChat(t, svc, "Alice Chen")
	msgs := messages(t, svc, alice.ID)
	m := msgs[len(msgs)-1]
	m.SenderName = wide
	m.Body = wide + " " + wide
	m.Reactions = []core.Reaction{{Emoji: "👩🏽‍💻", FromName: wide, From: "x", At: testNow}}
	m.EditHistory = []core.EditRevision{{Body: wide, At: testNow.Add(-time.Hour)}}
	overlaySizes(t, "wide details", NewMessageDetails(ctx, m))

	call := NewCallLog(ctx)
	call.Update(callsLoaded{list: []core.CallLogEntry{{Chat: alice.ID, Title: wide, Call: core.CallInfo{Missed: true, Video: true, StartedAt: testNow}}}})
	overlaySizes(t, "wide calls", call)

	m.Pinned = true
	pin := NewPinnedMessages(ctx, alice.ID, []core.Message{m})
	overlaySizes(t, "wide pinned", pin)
	for _, w := range []int{5, 20, 33, 80} {
		if l := RenderPinnedStrip(ctx, nil, []core.Message{m}, 0, w); len(strings.Split(l, "\n")) != 1 {
			t.Errorf("strip %d: multi-line", w)
		} else {
			checkBox(t, "strip", l, w, 1)
		}
	}
}

// TestSidebarGroupUpdateNoReload checks that group updates patch the
// groups-in-common list without a reload.
func TestSidebarGroupUpdateNoReload(t *testing.T) {
	ctx, svc := newCtx(t)
	alice := findChat(t, svc, "Alice Chen")
	g := findChat(t, svc, "Warte Wanderers")
	s := NewSidebar(ctx)
	loadSidebar(t, s, alice.ID)
	n := len(s.shared)
	if n == 0 {
		t.Fatal("expected groups in common")
	}
	_, cmd := s.Update(ui.EventMsg{Event: core.ChatUpdated{Chat: g}})
	if cmd != nil {
		t.Error("group update triggered a reload")
	}
	// Alice leaves the group.
	g2 := g
	g2.Members = nil
	for _, m := range g.Members {
		if m.ACI != alice.Contact.ACI {
			g2.Members = append(g2.Members, m)
		}
	}
	s.Update(ui.EventMsg{Event: core.ChatUpdated{Chat: g2}})
	if len(s.shared) != n-1 {
		t.Errorf("shared = %d, want %d", len(s.shared), n-1)
	}
	s.Update(ui.EventMsg{Event: core.ChatUpdated{Chat: g}})
	if len(s.shared) != n {
		t.Errorf("shared = %d after rejoin, want %d", len(s.shared), n)
	}
	s.Update(ui.EventMsg{Event: core.ChatRemoved{ID: g.ID}})
	if len(s.shared) != n-1 {
		t.Errorf("shared = %d after removal, want %d", len(s.shared), n-1)
	}
	checkBox(t, "after patch", s.View(40, 40), 40, 40)
}

// TestConnectionSpinnerRestarts checks the spinner resumes after its tick
// went to another overlay.
func TestConnectionSpinnerRestarts(t *testing.T) {
	ctx, _ := newCtx(t)
	c := NewConnection(ctx, core.ConnDisconnected, nil)
	if c.Init() == nil {
		t.Fatal("no spinner")
	}
	// The tick was delivered elsewhere; pretend it is overdue.
	c.tickAt = time.Now().Add(-time.Second)
	if _, cmd := c.Update(ui.EventMsg{Event: core.ConnChanged{State: core.ConnDisconnected}}); cmd == nil {
		t.Error("spinner did not restart")
	}
	// A fresh tick is not duplicated.
	if _, cmd := c.Update(ui.EventMsg{Event: core.ConnChanged{State: core.ConnDisconnected}}); cmd != nil {
		t.Error("spinner chain duplicated")
	}
	// Logged out: no animation.
	c.Update(ui.EventMsg{Event: core.LoggedOut{}})
	c.tickAt = time.Now().Add(-time.Second)
	if _, cmd := c.Update(connTick{c.tick}); cmd != nil {
		t.Error("logged out screen still ticks")
	}
}
