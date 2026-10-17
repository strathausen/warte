package all_test

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/notify"
	"warte/internal/termimg"
	"warte/internal/tui"
	"warte/internal/tui/ui"
	_ "warte/internal/tui/views/all"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

type nopNotifier struct{}

func (nopNotifier) Notify(notify.Notification) error { return nil }

// driver runs a tui.App synchronously: messages go through Update and the
// resulting commands run (with a short timeout, so ticks and blocking
// commands are skipped), like the Bubble Tea runtime would.
type driver struct {
	t    *testing.T
	a    *tui.App
	svc  *fake.Service
	w, h int
	cfg  string // config.toml path for :w
}

func newDriver(t *testing.T, w, h int) *driver {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = svc.Close() })
	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Images = config.ImagesBlocks
	cfg.Mouse = true
	cfg.Notifications = config.NotifyOff
	cfg.NotifyMethod = config.MethodOff
	none := false
	blocks := termimg.HalfBlocks
	cfgPath := t.TempDir() + "/config.toml"
	a := tui.New(svc, cfg, tui.Options{
		Now:        func() time.Time { return testNow },
		Getenv:     func(string) string { return "" },
		NerdFonts:  &none,
		Images:     &blocks,
		SkipStart:  true,
		Notifier:   nopNotifier{},
		ConfigPath: cfgPath,
	})
	t.Cleanup(a.Close)
	d := &driver{t: t, a: a, svc: svc, w: w, h: h, cfg: cfgPath}
	d.send(tea.WindowSizeMsg{Width: w, Height: h})
	return d
}

func (d *driver) send(msg tea.Msg) {
	d.t.Helper()
	_, cmd := d.a.Update(msg)
	d.run(cmd, 0)
}

func (d *driver) run(cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 40 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("command panicked: %v", r)
			}
		}()
		done <- cmd()
	}()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(300 * time.Millisecond):
		return
	}
	if msg == nil {
		return
	}
	if err, ok := msg.(error); ok && strings.HasPrefix(err.Error(), "command panicked") {
		d.t.Fatal(err)
	}
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			d.run(c, depth+1)
		}
		return
	case tea.QuitMsg:
		return
	}
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := 0; i < v.Len(); i++ {
			d.run(v.Index(i).Interface().(tea.Cmd), depth+1)
		}
		return
	}
	// Skip timer ticks (spinners, toasts, debounces): they would re-arm
	// forever. Their payload types are unexported, so detect them by name.
	if name := fmt.Sprintf("%T", msg); strings.Contains(strings.ToLower(name), "tick") || strings.Contains(name, "toastExpire") || strings.Contains(name, "flushKeys") {
		return
	}
	_, next := d.a.Update(msg)
	d.run(next, depth+1)
}

// keys sends space-separated key tokens: "space", "enter", "esc", "tab",
// "ctrl+x", single characters, or ":cmd args" (typed, then enter).
func (d *driver) keys(spec string) {
	d.t.Helper()
	if strings.HasPrefix(spec, ":") {
		for _, r := range spec {
			d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
		return
	}
	for _, tok := range strings.Fields(spec) {
		d.send(key(tok))
	}
}

func key(tok string) tea.KeyPressMsg {
	switch tok {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	if strings.HasPrefix(tok, "ctrl+") {
		return tea.KeyPressMsg{Code: rune(tok[5]), Mod: tea.ModCtrl}
	}
	r := []rune(tok)
	if len(r) != 1 {
		panic("bad key token " + tok)
	}
	return tea.KeyPressMsg{Code: r[0], Text: tok}
}

func (d *driver) top() string {
	o := d.a.TopOverlay()
	if o == nil {
		return ""
	}
	return fmt.Sprintf("%T %s", o, ansi.Strip(o.Title()))
}

// checkFrame asserts the frame is exactly w x h cells.
func (d *driver) checkFrame(label string) string {
	d.t.Helper()
	frame := d.a.View().Content
	lines := strings.Split(frame, "\n")
	if len(lines) != d.h {
		d.t.Fatalf("%s: %d lines, want %d", label, len(lines), d.h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != d.w {
			d.t.Fatalf("%s: line %d is %d cells wide, want %d: %q", label, i, got, d.w, ansi.Strip(l))
		}
	}
	return ansi.Strip(frame)
}

// focus selects the first message (in any chat) matching pred and opens
// its chat with the message selected.
func (d *driver) focus(pred func(core.Message) bool) {
	d.t.Helper()
	ctx := context.Background()
	chats, err := d.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	if err != nil {
		d.t.Fatal(err)
	}
	for _, c := range chats {
		msgs, err := d.svc.Messages(ctx, c.ID, core.MessageID{}, 500)
		if err != nil {
			continue
		}
		for i := len(msgs) - 1; i >= 0; i-- {
			if pred(msgs[i]) {
				id := msgs[i].ID
				d.send(ui.OpenChatMsg{Chat: c.ID, Focus: &id})
				if s := d.a.UIContext().Selected(); s == nil || s.ID != id {
					d.t.Fatalf("message not selected after OpenChatMsg")
				}
				return
			}
		}
	}
	d.t.Skip("no demo message matches")
}

func hasAtt(f func(core.Attachment) bool) func(core.Message) bool {
	return func(m core.Message) bool {
		return len(m.Attachments) > 0 && f(m.Attachments[0]) && !m.ViewOnce
	}
}

type feature struct {
	name string
	// setup prepares the state (optional): selects a message.
	setup func(d *driver)
	// open is the key spec typed from the main view (several specs run in
	// order).
	open []string
	// want is a substring of "<overlay type> <title>" of the top overlay.
	want string
	// close is typed to close it again ("esc" by default).
	close []string
}

func features() []feature {
	img := hasAtt(core.Attachment.IsImage)
	return []feature{
		{name: "chat switcher", open: []string{"space space"}, want: "finder.Chats", close: []string{"esc esc"}},
		{name: "global search", open: []string{"space /"}, want: "finder.Search", close: []string{"esc esc"}},
		{name: "theme picker", open: []string{"space t"}, want: "Theme", close: []string{"esc esc"}},
		{name: "help", open: []string{"?"}, want: "helpOverlay"},
		{name: "new chat", open: []string{"space n"}, want: "forms.", close: []string{"esc"}},
		{name: "new group", open: []string{"space g"}, want: "forms.", close: []string{"esc"}},
		{name: "settings", open: []string{"space ,"}, want: "Settings"},
		{name: "stories", open: []string{"space s"}, want: "stories.List"},
		{name: "story viewer", open: []string{":story view"}, want: "stories.Viewer"},
		{name: "post story", open: []string{":story new"}, want: "stories.Post", close: []string{"ctrl+c"}},
		{name: "call log", open: []string{"space c"}, want: "info.CallLog"},
		{name: "profile", open: []string{":profile"}, want: "forms."},
		{name: "settings chat", open: []string{":settings chat"}, want: "Settings"},
		{name: "chat settings gi", open: []string{"enter esc", "g i"}, want: "Settings"},
		{name: "safety number", open: []string{"enter esc", ":verify"}, want: "Safety number"},
		{name: "message details", open: []string{"enter esc", "K"}, want: "info.MessageDetails"},
		{name: "pinned messages", open: []string{"enter esc", "g p"}, want: "info.PinnedMessages"},
		{name: "pins command", open: []string{":pins"}, want: "info.PinnedMessages"},
		{name: "gallery", open: []string{"enter esc", "g m"}, want: "media.Gallery"},
		{name: "forward", setup: func(d *driver) {
			d.focus(func(m core.Message) bool { return m.Kind == core.KindNormal && !m.ViewOnce && m.Body != "" })
		},
			open: []string{"f"}, want: "Forward", close: []string{"esc esc"}},
		{name: "quick reactions", setup: func(d *driver) {
			d.focus(func(m core.Message) bool { return m.Kind == core.KindNormal && m.Body != "" })
		},
			open: []string{"R"}, want: "React"},
		{name: "emoji picker from reaction bar", setup: func(d *driver) {
			d.focus(func(m core.Message) bool { return m.Kind == core.KindNormal && m.Body != "" })
		},
			open: []string{"+", "7"}, want: "emoji.Picker"},
		{name: "reaction details", setup: func(d *driver) { d.focus(func(m core.Message) bool { return len(m.Reactions) > 0 }) },
			open: []string{"g r"}, want: "emoji.Reactions"},
		{name: "emoji picker from composer", open: []string{"i", "ctrl+g"}, want: "emoji.Picker"},
		{name: "sticker picker from composer", open: []string{"i", "ctrl+s"}, want: "emoji.Stickers"},
		{name: "sticker command", open: []string{":sticker"}, want: "emoji.Stickers"},
		{name: "image viewer", setup: func(d *driver) { d.focus(img) }, open: []string{"enter"}, want: "media.Viewer"},
		{name: "view once", setup: func(d *driver) {
			d.focus(func(m core.Message) bool {
				return m.ViewOnce && !m.Outgoing && !m.ViewOnceOpened && len(m.Attachments) > 0
			})
		}, open: []string{"enter"}, want: "View once"},
		{name: "voice note player", setup: func(d *driver) { d.focus(hasAtt(core.Attachment.IsAudio)) },
			open: []string{"enter"}, want: "media.PlayerOverlay"},
		{name: "find command", open: []string{":find from:alice"}, want: "finder.Search", close: []string{"esc esc"}},
		{name: "group create", open: []string{":group create"}, want: "forms.", close: []string{"esc"}},
		{name: "calls command", open: []string{":calls"}, want: "info.CallLog"},
		{name: "media command", open: []string{":media files"}, want: "media.Gallery"},
	}
}

// TestFeaturesReachable opens every overlay through its key or command from
// the main view, checks the frame, closes it and checks that nothing is left
// open and that the App is back in Normal mode, at 80x24 and 160x48.
func TestFeaturesReachable(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {160, 48}} {
		for _, f := range features() {
			t.Run(fmt.Sprintf("%dx%d/%s", sz[0], sz[1], f.name), func(t *testing.T) {
				d := newDriver(t, sz[0], sz[1])
				if f.setup != nil {
					f.setup(d)
				}
				for _, k := range f.open {
					d.keys(k)
				}
				if got := d.top(); !strings.Contains(got, f.want) {
					t.Fatalf("top overlay = %q, want %q", got, f.want)
				}
				d.checkFrame("open")
				// Navigation keys must not leak to the views below.
				before := d.a.UIContext().Chat()
				closeKeys := f.close
				if closeKeys == nil {
					closeKeys = []string{"esc"}
				}
				for _, k := range closeKeys {
					d.keys(k)
				}
				if n := d.a.Overlays(); n != 0 {
					t.Fatalf("%d overlays still open after %v (top %q)", n, closeKeys, d.top())
				}
				if d.a.UIContext().Chat() != before {
					t.Fatalf("closing changed the open chat")
				}
				d.checkFrame("closed")
				if strings.Contains(f.name, "composer") {
					if m := d.a.Mode().String(); !strings.EqualFold(m, "insert") {
						t.Fatalf("mode after closing = %s, want insert (restored)", m)
					}
					d.keys("esc")
				}
				if m := d.a.Mode().String(); !strings.EqualFold(m, "normal") {
					t.Fatalf("mode = %s", m)
				}
			})
		}
	}
}

// TestOverlayKeysDoNotLeak: j/k inside an overlay move the overlay, not the
// chat list or conversation underneath.
func TestOverlayKeysDoNotLeak(t *testing.T) {
	d := newDriver(t, 120, 40)
	d.keys("enter esc") // focus the conversation (opening starts in Insert)
	sel := d.a.UIContext().Selected()
	d.keys("space c") // call log
	d.keys("j j k G g g")
	if got := d.a.UIContext().Selected(); (sel == nil) != (got == nil) || (sel != nil && sel.ID != got.ID) {
		t.Fatal("keys inside the overlay moved the conversation selection")
	}
	d.keys("esc")
	if d.a.Overlays() != 0 {
		t.Fatal("esc did not close")
	}
}

// TestNestedOverlays: an overlay opened from another one closes first, esc
// by esc.
func TestNestedOverlays(t *testing.T) {
	d := newDriver(t, 120, 40)
	d.focus(func(m core.Message) bool { return len(m.Reactions) > 0 })
	d.keys("g r")
	d.keys("+") // reaction details → reaction picker
	if d.a.Overlays() != 2 || !strings.Contains(d.top(), "emoji.Picker") {
		t.Fatalf("overlays %d, top %q", d.a.Overlays(), d.top())
	}
	d.keys("esc")
	if d.a.Overlays() != 1 || !strings.Contains(d.top(), "Reactions") {
		t.Fatalf("after esc: overlays %d, top %q", d.a.Overlays(), d.top())
	}
	d.keys("esc")
	if d.a.Overlays() != 0 {
		t.Fatalf("after esc esc: %d", d.a.Overlays())
	}
}

// TestSidebarPane: <leader>i opens the info pane focused, keys move inside
// it, ctrl+w h leaves it and <leader>i closes it.
func TestSidebarPane(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {160, 48}} {
		d := newDriver(t, sz[0], sz[1])
		d.keys("enter esc")
		d.keys("space i")
		if !d.a.SidebarOpen() || d.a.FocusedPane() != tui.FocusSidebar {
			t.Fatalf("%v: sidebar open=%v focus=%v", sz, d.a.SidebarOpen(), d.a.FocusedPane())
		}
		out := d.checkFrame("sidebar")
		if !strings.Contains(out, "Info") {
			t.Fatalf("%v: no info pane:\n%s", sz, out)
		}
		d.keys("j j k")
		d.checkFrame("sidebar moved")
		d.keys("ctrl+w h")
		if d.a.FocusedPane() == tui.FocusSidebar {
			t.Fatalf("%v: ctrl+w h kept the sidebar focused", sz)
		}
		if sz[0] >= 90 {
			d.keys("ctrl+w l")
			if d.a.FocusedPane() != tui.FocusSidebar {
				t.Fatalf("%v: ctrl+w l did not focus the sidebar (focus %v)", sz, d.a.FocusedPane())
			}
		}
		d.keys("space i")
		if d.a.SidebarOpen() {
			t.Fatalf("%v: sidebar still open", sz)
		}
		d.checkFrame("closed")
	}
}

// TestThemePreviewReverts: moving in the theme picker re-themes the app;
// esc restores the configured theme, enter keeps the chosen one.
func TestThemePreviewReverts(t *testing.T) {
	d := newDriver(t, 120, 40)
	orig := d.a.Styles().Theme.Name
	d.keys("space t")
	d.keys("down down down")
	if d.a.Styles().Theme.Name == orig {
		t.Fatal("no live preview")
	}
	d.keys("esc esc")
	if got := d.a.Styles().Theme.Name; got != orig {
		t.Fatalf("theme after cancel = %s, want %s", got, orig)
	}
	d.keys("space t")
	d.keys("down")
	chosen := d.a.Styles().Theme.Name
	d.keys("enter")
	if got := d.a.Styles().Theme.Name; got != chosen || got == orig {
		t.Fatalf("theme after enter = %s, want %s", got, chosen)
	}
}

// TestSettingsSave: changing settings applies them live; ":w" writes them.
func TestSettingsSave(t *testing.T) {
	d := newDriver(t, 120, 40)
	d.send(ui.ConfigChangedMsg{Config: func() config.Config {
		c := d.a.UIContext().Config
		c.Layout = config.LayoutIRC
		c.Theme = "nord"
		return c
	}()})
	if d.a.Styles().Theme.Name != "nord" {
		t.Fatalf("theme not applied: %s", d.a.Styles().Theme.Name)
	}
	if d.a.UIContext().Config.Layout != config.LayoutIRC {
		t.Fatal("layout not applied")
	}
	d.keys(":w")
	got, _, err := config.Load(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "nord" || got.Layout != config.LayoutIRC {
		t.Fatalf("saved theme=%q layout=%q", got.Theme, got.Layout)
	}
}

// TestCommandsComplete: every registered view command is reachable and
// tab-completes from its prefix.
func TestCommandsComplete(t *testing.T) {
	names := map[string]bool{}
	for _, c := range ui.Commands() {
		names[c.Name] = true
	}
	for _, want := range []string{"find", "chats", "themes", "forward", "emoji", "stickers", "reactions",
		"media", "play", "view", "story", "group", "settings", "profile", "verify", "chatsettings",
		"calls", "pinned", "details", "connection"} {
		if !names[want] {
			t.Errorf("command :%s not registered", want)
		}
	}
}

// eventsSvc lets a test inject backend events.
type eventsSvc struct {
	*fake.Service
	ch chan core.Event
}

func (s *eventsSvc) Events() <-chan core.Event { return s.ch }

// TestTeatestWalkthrough runs the real program and opens a few overlays by
// key, waiting for them on screen, then quits with :q (which only works
// once every overlay is closed again).
func TestTeatestWalkthrough(t *testing.T) {
	for _, sz := range [][2]int{{80, 24}, {160, 48}} {
		t.Run(fmt.Sprintf("%dx%d", sz[0], sz[1]), func(t *testing.T) {
			svc := fake.New(fake.Options{Seed: 1, Latency: 20 * time.Millisecond})
			t.Cleanup(func() { _ = svc.Close() })
			cfg := config.Default()
			cfg.Theme = "charm"
			cfg.Notifications = config.NotifyOff
			none := false
			blocks := termimg.HalfBlocks
			app := tui.New(svc, cfg, tui.Options{Getenv: func(string) string { return "" }, NerdFonts: &none, Images: &blocks, Notifier: nopNotifier{}, ConfigPath: t.TempDir() + "/c.toml"})
			t.Cleanup(app.Close)
			tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(sz[0], sz[1]))
			wait := func(want string) {
				t.Helper()
				teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(want)) },
					teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
			}
			press := func(keys ...string) {
				for _, k := range keys {
					tm.Send(key(k))
					time.Sleep(30 * time.Millisecond)
				}
			}
			wait("Alice Chen")
			press("space", "space")
			wait("Chats")
			press("esc", "esc")
			press("space", "c")
			wait("Calls")
			press("esc")
			press("space", "s")
			wait("Stories")
			press("esc")
			press("?")
			wait("Keys")
			press("esc")
			press("enter", "esc", "K")
			wait("Message details")
			press("esc")
			tm.Type(":q")
			tm.Send(key("enter"))
			tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
			if fm, ok := tm.FinalModel(t).(*tui.App); !ok || fm.Overlays() != 0 {
				t.Fatal("overlays left open")
			}
		})
	}
}

// TestLoggedOutScreen: a LoggedOut event shows the relink screen; enter
// asks the program to quit for relinking.
func TestLoggedOutScreen(t *testing.T) {
	f := fake.New(fake.Options{Seed: 1, Latency: -1})
	t.Cleanup(func() { _ = f.Close() })
	svc := &eventsSvc{Service: f, ch: make(chan core.Event, 8)}
	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Notifications = config.NotifyOff
	none := false
	blocks := termimg.HalfBlocks
	app := tui.New(svc, cfg, tui.Options{Getenv: func(string) string { return "" }, NerdFonts: &none, Images: &blocks, Notifier: nopNotifier{}, SkipStart: true})
	t.Cleanup(app.Close)
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(100, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Alice Chen")) }, teatest.WithDuration(5*time.Second))
	tm.Send(key("space"))
	tm.Send(key("c")) // an overlay that must be dismissed by the logout
	svc.ch <- core.LoggedOut{}
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("Logged out")) }, teatest.WithDuration(5*time.Second))
	time.Sleep(100 * time.Millisecond)
	tm.Send(key("enter"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
	fm := tm.FinalModel(t).(*tui.App)
	if !fm.RelinkRequested() {
		t.Fatal("enter on the logged-out screen did not request relinking")
	}
}

// TestPinnedStrip: a chat with a pinned message shows the strip under the
// header; clicking it (or gp) opens the pinned list.
func TestPinnedStrip(t *testing.T) {
	d := newDriver(t, 120, 40)
	d.focus(func(m core.Message) bool { return m.Pinned && !m.RemoteDeleted })
	var pinned core.Message
	if s := d.a.UIContext().Selected(); s != nil {
		pinned = *s
	}
	out := d.checkFrame("pinned")
	lines := strings.Split(out, "\n")
	found := -1
	for i := 0; i < 6 && i < len(lines); i++ {
		if strings.Contains(lines[i], strings.Fields(pinned.Body)[0]) {
			found = i
		}
	}
	if found < 0 {
		t.Fatalf("no pinned strip for %q:\n%s", pinned.Body, strings.Join(lines[:6], "\n"))
	}
	d.send(tea.MouseClickMsg{X: 60, Y: found, Button: tea.MouseLeft})
	if !strings.Contains(d.top(), "info.PinnedMessages") {
		t.Fatalf("click on the strip opened %q", d.top())
	}
	// A click outside the dialog closes it (and dismisses cleanly).
	d.send(tea.MouseClickMsg{X: 0, Y: 39, Button: tea.MouseLeft})
	if d.a.Overlays() != 0 {
		t.Fatal("click outside did not close")
	}
}
