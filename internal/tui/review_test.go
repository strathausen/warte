package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/notify"
	"warte/internal/termimg"
)

// Regression tests for issues found in review.

func TestEditKeepsDraft(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>")
	h.keys("typo<enter>")
	h.waitFor(time.Second, func() bool { return h.lastMsg().Body == "typo" })
	h.a.comp.ta.SetValue("half-written") // typing is slow in the harness
	h.keys("<esc>")                      // draft put aside
	h.keys("G")
	h.keys("e")
	if h.a.comp.editing == nil || h.a.comp.ta.Value() != "typo" {
		t.Fatalf("edit not started: %q", h.a.comp.ta.Value())
	}
	h.keys("<bs><bs><bs><bs>fixed<enter>")
	if !h.waitFor(time.Second, func() bool { return h.lastMsg().Body == "fixed" }) {
		t.Fatal("edit not applied")
	}
	if got := h.a.comp.ta.Value(); got != "half-written" {
		t.Fatalf("draft after edit = %q, want %q", got, "half-written")
	}
}

func TestSendClearsSavedDraft(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter>i")
	h.a.comp.ta.SetValue("later")
	h.keys("<esc>")
	id := h.a.conv.chat.ID
	h.keys("a")
	h.a.comp.ta.SetValue("hello")
	h.keys("<enter><esc>")
	c, err := h.svc.Chat(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if c.Draft != "" {
		t.Fatalf("draft still saved after sending: %q", c.Draft)
	}
}

func TestCommandCompletionCandidatesVisible(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys(":theme <tab><tab>")
	status := stripANSI(strings.Split(h.view(), "\n")[39])
	if !strings.Contains(status, ":theme ") {
		t.Fatalf("command line not shown: %q", status)
	}
	// The wildmenu lists more than the current candidate.
	n := 0
	for _, name := range []string{"auto", "catppuccin-frappe", "catppuccin-latte"} {
		if strings.Contains(status, name) {
			n++
		}
	}
	if n < 3 {
		t.Fatalf("candidates not visible: %q", status)
	}
	// Typing after cycling must not leave the input scrolled.
	h.keys("x")
	status = stripANSI(strings.Split(h.view(), "\n")[39])
	if !strings.Contains(status, ":theme catppuccin-frappex") {
		t.Fatalf("input scrolled: %q", status)
	}
}

func TestCommandCompletionMultiWord(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys(":open note to<tab>")
	if got := h.a.cmdline.input.Value(); got != "open Note to Self " {
		t.Fatalf("completed to %q", got)
	}
	h.keys("<enter>")
	if h.a.conv.chat.Kind != core.ChatNoteToSelf {
		t.Fatalf("opened %q", h.a.conv.chat.Title)
	}
}

func TestJumpList(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>")
	start := h.a.conv.selected().ID
	h.keys("gg")
	if h.a.conv.sel != 0 {
		t.Fatalf("gg: sel = %d", h.a.conv.sel)
	}
	h.keys("<C-o>")
	if got := h.a.conv.selected().ID; got != start {
		t.Fatalf("ctrl+o went to %v, want %v", got, start)
	}
	h.keys("<C-i>")
	if h.a.conv.sel != 0 {
		t.Fatalf("ctrl+i: sel = %d", h.a.conv.sel)
	}
	// Across chats.
	h.keys("<C-o>")
	first := h.a.conv.chat.ID
	h.keys(":open note to self<enter><esc>")
	if h.a.conv.chat.ID == first {
		t.Fatal("chat not switched")
	}
	nts := h.a.conv.selected().ID
	h.keys("<C-o>")
	if h.a.conv.chat.ID != first || h.a.conv.selected().ID != start {
		t.Fatalf("ctrl+o did not return to the previous chat")
	}
	h.keys("<C-i>")
	if h.a.conv.chat.ID == first || h.a.conv.selected().ID != nts {
		t.Fatalf("ctrl+i did not go forward to the other chat")
	}
}

func TestShortKeyLabel(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want string
	}{
		{"j/down", 18, "j/down"},
		{"H/ctrl+w h/ctrl+w left", 18, "H/ctrl+w h"},
		{"ctrl+w ctrl+w ctrl+w ctrl+w", 10, "ctrl+w ct…"},
		{"/", 5, "/"},
	}
	for _, c := range cases {
		if got := shortKeyLabel(c.in, c.w); got != c.want {
			t.Errorf("shortKeyLabel(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
}

type recNotifier struct {
	mu  sync.Mutex
	got []notify.Notification
}

func (r *recNotifier) Notify(n notify.Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, n)
	return nil
}

func TestCallNotificationPrivacy(t *testing.T) {
	for _, tc := range []struct {
		level    string
		wantName bool
	}{
		{config.NotifyNameAndMessage, true},
		{config.NotifyNone, false},
	} {
		rec := &recNotifier{}
		h := newHarness(t, 120, 40, withConfig(func(c *config.Config) {
			c.Notifications = tc.level
			c.NotifyMethod = "native"
		}), func(_ *fake.Options, _ *config.Config, o *Options) { o.Notifier = rec })
		h.send(eventMsg{ev: core.IncomingCall{Chat: fake.ChatAlice, Name: "Alice Chen", Ringing: true}})
		rec.mu.Lock()
		got := append([]notify.Notification(nil), rec.got...)
		rec.mu.Unlock()
		if len(got) != 1 {
			t.Fatalf("%s: %d notifications", tc.level, len(got))
		}
		has := strings.Contains(got[0].Title+got[0].Body, "Alice")
		if has != tc.wantName {
			t.Errorf("%s: notification %+v reveals name = %v", tc.level, got[0], has)
		}
		if !strings.Contains(h.plain(), "Alice Chen is calling") {
			t.Errorf("%s: call banner missing", tc.level)
		}
	}
}

func TestReactionBarAnchoredToMessage(t *testing.T) {
	h := newHarness(t, 120, 40)
	h.keys("<enter><esc>kk")
	var mz zone
	for _, z := range h.a.zones.zones {
		if z.kind == zoneMessage && z.index == h.a.conv.sel {
			mz = z
		}
	}
	if mz.kind == 0 {
		t.Fatal("selected message not on screen")
	}
	h.keys("R")
	o := h.a.zones.overlay
	if o == nil {
		t.Fatal("no overlay")
	}
	if o.y != mz.y+mz.h && o.y+o.h+1 != mz.y {
		t.Fatalf("bar at y=%d (h=%d), message spans %d..%d", o.y, o.h, mz.y, mz.y+mz.h)
	}
	checkFrame(t, h.view(), 120, 40, "reaction bar")
}

func TestWheelKeepsSelectionVisible(t *testing.T) {
	h := newHarness(t, 120, 40, withConfig(func(c *config.Config) { c.Mouse = true }))
	h.keys("<enter>G")
	r := h.a.layout.conv
	for i := 0; i < 5; i++ {
		h.send(tea.MouseWheelMsg{X: r.x + 5, Y: r.y + 5, Button: tea.MouseWheelUp})
	}
	if !h.a.isVisible(h.a.conv.sel) {
		t.Fatalf("selection %d scrolled out of view", h.a.conv.sel)
	}
	before := h.a.conv.bottom
	h.keys("k") // must not snap back to the old position
	if h.a.conv.bottom > before {
		t.Fatalf("k jumped the view back: bottom %d -> %d", before, h.a.conv.bottom)
	}
}

func TestKittyInTmuxNeedsPassthrough(t *testing.T) {
	env := map[string]string{"TMUX": "/tmp/tmux-1/default,1,0", "TERM": "tmux-256color", "KITTY_WINDOW_ID": "1"}
	orig := tmuxPassthrough
	t.Cleanup(func() { tmuxPassthrough = orig })
	for _, allowed := range []bool{false, true} {
		tmuxPassthrough = func(func(string) string) bool { return allowed }
		cfg := config.Default()
		m := newImageManager(&App{}, cfg, Options{}, func(k string) string { return env[k] })
		want := termimg.HalfBlocks
		if allowed {
			want = termimg.Kitty
		}
		if m.protocol != want {
			t.Errorf("passthrough=%v: protocol %v, want %v", allowed, m.protocol, want)
		}
	}
}

func TestReactionBarFitsAtAllSizes(t *testing.T) {
	for _, sz := range [][2]int{{60, 20}, {80, 24}, {120, 40}, {200, 60}, {40, 12}} {
		h := newHarness(t, sz[0], sz[1])
		h.keys("<enter>")
		for _, k := range []string{"", "k", "kkkk", "gg"} {
			h.keys(k)
			h.keys("R")
			checkFrame(t, h.view(), sz[0], sz[1], "reaction bar")
			h.keys("<esc>")
		}
	}
}
