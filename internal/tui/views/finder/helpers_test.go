package finder

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

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func testCtx(t *testing.T) (ui.Context, *fake.Service) {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Clock: func() time.Time { return testNow }, Latency: -1})
	t.Cleanup(func() { _ = svc.Close() })
	th, _ := theme.Get("charm")
	cfg := config.Default()
	cfg.Theme = "charm"
	open := core.ChatID("")
	return ui.Context{
		Svc:      svc,
		Styles:   theme.NewStyles(th),
		Icons:    theme.Icons(false),
		Theme:    th,
		Config:   cfg,
		Keys:     keymap.New(),
		Now:      func() time.Time { return testNow },
		Chat:     func() core.ChatID { return open },
		Selected: func() *core.Message { return nil },
		Self:     svc.Self(),
	}, svc
}

// drain runs cmd synchronously and feeds every resulting message back into
// o (recursively, flattening batches). Messages for the App (OpenChatMsg,
// ToastMsg, …) are collected and returned.
func drain(t *testing.T, o ui.Overlay, cmd tea.Cmd) (ui.Overlay, []tea.Msg) {
	t.Helper()
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		switch m := msg.(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case ui.OpenChatMsg, ui.ToastMsg, ui.RunCommandMsg, ui.PushOverlayMsg, ui.CloseOverlayMsg:
			out = append(out, m)
		default:
			if reflect.TypeOf(msg).PkgPath() != reflect.TypeOf(chatsLoadedMsg{}).PkgPath() {
				out = append(out, msg)
				continue
			}
			if o == nil {
				continue
			}
			var next tea.Cmd
			o, next = o.Update(msg)
			queue = append(queue, next)
		}
	}
	return o, out
}

func push(t *testing.T, o ui.Overlay) ui.Overlay {
	t.Helper()
	o, _ = drain(t, o, o.Init())
	return o
}

// key builds a key press from a Bubble Tea key string.
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	if strings.HasPrefix(s, "ctrl+") {
		return tea.KeyPressMsg{Code: rune(s[len("ctrl+")]), Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// send delivers a key and drains the resulting command.
func send(t *testing.T, o ui.Overlay, k string) (ui.Overlay, []tea.Msg) {
	t.Helper()
	next, cmd := o.Update(key(k))
	return drain(t, next, cmd)
}

// typeText types s rune by rune.
func typeText(t *testing.T, o ui.Overlay, s string) ui.Overlay {
	t.Helper()
	for _, r := range s {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		var msgs []tea.Msg
		o, msgs = send(t, o, k)
		_ = msgs
		if o == nil {
			t.Fatalf("overlay closed while typing %q", s)
		}
	}
	return o
}

var sizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}, {20, 6}, {1, 1}}

// checkView renders o at several sizes (through Size, like the App) and
// asserts no line exceeds the width and the height is respected.
func checkView(t *testing.T, o ui.Overlay) {
	t.Helper()
	for _, sz := range sizes {
		w, h := o.Size(sz[0], sz[1])
		w, h = max(min(w, sz[0]), 1), max(min(h, sz[1]), 1)
		out := o.View(w, h)
		lines := strings.Split(out, "\n")
		if len(lines) > h {
			t.Errorf("%dx%d: %d lines > %d", w, h, len(lines), h)
		}
		for i, l := range lines {
			if lw := ansi.StringWidth(l); lw > w {
				t.Errorf("%dx%d: line %d is %d cells wide: %q", w, h, i, lw, ansi.Strip(l))
			}
		}
		// Also render at the raw maximum, bypassing Size.
		out = o.View(sz[0], sz[1])
		for i, l := range strings.Split(out, "\n") {
			if lw := ansi.StringWidth(l); lw > sz[0] {
				t.Errorf("raw %dx%d: line %d is %d cells wide", sz[0], sz[1], i, lw)
			}
		}
	}
}

func plain(o ui.Overlay, w, h int) string { return ansi.Strip(o.View(w, h)) }
