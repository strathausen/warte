package forms

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

// harness mimics the App's overlay stack: keys go through the keymap
// engine (or raw to key capturers), commands run synchronously (slow ones,
// like cursor blinks, are dropped), Push/Close manipulate the stack and
// every other message is recorded and forwarded to the top overlay.
type harness struct {
	t     *testing.T
	ctx   ui.Context
	svc   *fake.Service
	stack []ui.Overlay
	out   []tea.Msg
	chat  core.ChatID
}

func newHarness(t *testing.T) *harness {
	return newHarnessTheme(t, "charm")
}

func newHarnessTheme(t *testing.T, themeName string) *harness {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = svc.Close() })
	th, ok := theme.Get(themeName)
	if !ok {
		t.Fatalf("theme %q", themeName)
	}
	h := &harness{t: t, svc: svc}
	h.ctx = ui.Context{
		Svc:    svc,
		Styles: theme.NewStyles(th),
		Icons:  theme.Icons(false),
		Theme:  th,
		Config: config.Default(),
		Keys:   keymap.New(),
		Now:    func() time.Time { return testNow },
		Chat:   func() core.ChatID { return h.chat },
		Self:   svc.Self(),
	}
	return h
}

func (h *harness) top() ui.Overlay {
	if len(h.stack) == 0 {
		return nil
	}
	return h.stack[len(h.stack)-1]
}

func (h *harness) push(o ui.Overlay) {
	h.t.Helper()
	h.stack = append(h.stack, o)
	h.run(o.Init())
	h.pump()
}

func (h *harness) pop() {
	if len(h.stack) > 0 {
		h.stack = h.stack[:len(h.stack)-1]
	}
}

// send delivers msg to the top overlay.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	top := h.top()
	if top == nil {
		return
	}
	o, cmd := top.Update(msg)
	if o == nil {
		h.pop()
	} else {
		h.stack[len(h.stack)-1] = o
	}
	h.run(cmd)
}

// run executes cmd and handles its messages like the App would.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(40 * time.Millisecond):
		return // ticks (cursor blink, spinners)
	}
	h.handle(msg)
}

func (h *harness) handle(msg tea.Msg) {
	h.t.Helper()
	if msg == nil {
		return
	}
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			h.run(c)
		}
		return
	case ui.PushOverlayMsg:
		h.push(m.Overlay)
		return
	case ui.CloseOverlayMsg:
		h.pop()
		return
	case ui.ToastMsg, ui.OpenChatMsg, ui.RunCommandMsg:
		h.out = append(h.out, msg)
		return
	}
	// tea.Sequence's message is an unexported []Cmd.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := 0; i < v.Len(); i++ {
			if c, ok := v.Index(i).Interface().(tea.Cmd); ok {
				h.run(c)
			}
		}
		return
	}
	h.out = append(h.out, msg)
	h.send(msg)
}

// pump forwards pending backend events to the top overlay.
func (h *harness) pump() {
	h.t.Helper()
	for {
		select {
		case ev, ok := <-h.svc.Events():
			if !ok {
				return
			}
			h.send(ui.EventMsg{Event: ev})
		default:
			return
		}
	}
}

// key sends key presses (names like "enter", "esc", "tab", "shift+tab",
// "up", "down", "backspace", "ctrl+x" or single characters) through the
// keymap like the App does.
func (h *harness) key(names ...string) {
	h.t.Helper()
	for _, n := range names {
		h.press(keyMsg(n))
		h.pump()
	}
}

// typ types text character by character.
func (h *harness) typ(s string) {
	h.t.Helper()
	for _, r := range s {
		h.press(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	h.pump()
}

func (h *harness) press(msg tea.KeyPressMsg) {
	h.t.Helper()
	top := h.top()
	if top == nil {
		h.t.Fatalf("key %q with no overlay", msg.String())
	}
	if kc, ok := top.(ui.KeyCapturer); ok && kc.CaptureKeys() {
		h.send(msg)
		return
	}
	res := h.ctx.Keys.Feed(keymap.FromTea(msg), keymap.Overlay(top.KeyContext()))
	if res.Expires || res.Pending {
		res = h.ctx.Keys.Flush()
	}
	for _, r := range res.All() {
		switch {
		case r.Action == keymap.ActBack:
			if bh, ok := top.(ui.BackHandler); ok {
				if handled, cmd := bh.HandleBack(); handled {
					h.run(cmd)
					continue
				}
			}
			h.pop()
		case r.Matched():
			h.send(ui.ActionMsg{Action: r.Action, Count: r.Count, Keys: r.Keys})
		case r.Unmatched:
			h.send(msg)
		}
	}
}

func keyMsg(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if strings.HasPrefix(name, "ctrl+") && len(name) == 6 {
		return tea.KeyPressMsg{Code: rune(name[5]), Mod: tea.ModCtrl}
	}
	r := []rune(name)[0]
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// view renders the top overlay like the App: Size, then View.
func (h *harness) view(termW, termH int) string {
	h.t.Helper()
	top := h.top()
	if top == nil {
		return ""
	}
	return renderOverlay(h.t, top, termW, termH)
}

// renderOverlay renders o for a terminal of termW x termH and checks the
// bounds.
func renderOverlay(t *testing.T, o ui.Overlay, termW, termH int) string {
	t.Helper()
	maxW, maxH := max(termW-6, 10)-4, max(termH-4, 3)-2
	w, hh := o.Size(maxW, maxH)
	w = min(max(w, 1), maxW)
	hh = min(max(hh, 1), maxH)
	out := o.View(w, hh)
	checkBounds(t, out, w, hh)
	_ = o.Title()
	if f, ok := o.(ui.Footer); ok {
		_ = f.Footer()
	}
	return out
}

func checkBounds(t *testing.T, s string, w, h int) {
	t.Helper()
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		t.Errorf("%d lines, want <= %d:\n%s", len(lines), h, ansi.Strip(s))
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("line %d is %d cells wide, want <= %d: %q", i, lw, w, ansi.Strip(l))
		}
	}
}

// outOf returns the recorded messages of type T.
func outOf[T any](h *harness) []T {
	var res []T
	for _, m := range h.out {
		if v, ok := m.(T); ok {
			res = append(res, v)
		}
	}
	return res
}

var sizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}}

// plain renders the top overlay at 120x40 without ANSI.
func (h *harness) plain() string {
	h.t.Helper()
	return ansi.Strip(h.view(120, 40))
}
