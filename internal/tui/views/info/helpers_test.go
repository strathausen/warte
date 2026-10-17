package info

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

// sizes covers the required range (60x20 … 200x60) plus a few narrower
// pane widths.
var sizes = [][2]int{{16, 5}, {24, 10}, {36, 20}, {60, 20}, {80, 24}, {120, 40}, {160, 48}, {200, 60}}

type fakeImages struct{ enabled bool }

func (f fakeImages) Enabled() bool { return f.enabled }
func (f fakeImages) Inline() bool  { return f.enabled }
func (f fakeImages) Size(ref ui.ImageRef, maxCols, maxRows int) (int, int) {
	w, h := ref.Attachment.Width, ref.Attachment.Height
	if w <= 0 || h <= 0 {
		w, h = 4, 3
	}
	return termimg.FitCells(w, h, max(maxCols, 1), max(maxRows, 1), termimg.CellSize{Width: 1, Height: 2}, true)
}
func (f fakeImages) Rows(_ ui.ImageRef, cols, rows int) ([]string, bool) {
	out := make([]string, rows)
	for i := range out {
		out[i] = strings.Repeat("▓", cols)
	}
	return out, true
}

type ctxOpt func(*ui.Context)

func withTheme(name string) ctxOpt {
	return func(c *ui.Context) {
		th, _ := theme.Get(name)
		c.Styles = theme.NewStyles(th)
		c.Theme = th
	}
}

func withImages(on bool) ctxOpt {
	return func(c *ui.Context) { c.Images = fakeImages{on} }
}

func newCtx(t testing.TB, opts ...ctxOpt) (ui.Context, *fake.Service) {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { svc.Close() })
	th, _ := theme.Get("charm")
	ctx := ui.Context{
		Svc:    svc,
		Styles: theme.NewStyles(th),
		Icons:  theme.Icons(false),
		Theme:  th,
		Config: config.Default(),
		Keys:   keymap.New(),
		Now:    func() time.Time { return testNow },
		Self:   svc.Self(),
		Chat:   func() core.ChatID { return "" },
	}
	for _, o := range opts {
		o(&ctx)
	}
	return ctx, svc
}

// execCmd executes cmd (expanding batches) and returns the produced
// messages.
func execCmd(t testing.TB, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, execCmd(t, c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// feedOverlay delivers msgs to o (re-running resulting commands that stay
// internal) and returns the overlay plus messages addressed to the App.
func feedOverlay(t testing.TB, o ui.Overlay, cmd tea.Cmd) (ui.Overlay, []tea.Msg) {
	t.Helper()
	var external []tea.Msg
	queue := execCmd(t, cmd)
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if isAppMsg(m) {
			external = append(external, m)
			continue
		}
		if o == nil {
			continue
		}
		var c tea.Cmd
		o, c = o.Update(m)
		queue = append(queue, execCmd(t, c)...)
	}
	return o, external
}

func isAppMsg(m tea.Msg) bool {
	switch m.(type) {
	case ui.RunCommandMsg, ui.OpenChatMsg, ui.PushOverlayMsg, ui.ToastMsg, ui.CloseOverlayMsg, RelinkMsg, tea.QuitMsg:
		return true
	}
	return false
}

func act(a keymap.Action) ui.ActionMsg { return ui.ActionMsg{Action: a, Count: 1} }

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

// checkBox asserts the view has exactly h lines no wider than w.
func checkBox(t testing.TB, name, view string, w, h int) {
	t.Helper()
	if h <= 0 || w <= 0 {
		return
	}
	lines := strings.Split(view, "\n")
	if len(lines) != h {
		t.Errorf("%s %dx%d: %d lines, want %d", name, w, h, len(lines), h)
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("%s %dx%d: line %d is %d cells wide: %q", name, w, h, i, lw, ansi.Strip(l))
		}
	}
}

// checkMax asserts at most h lines no wider than w.
func checkMax(t testing.TB, name, view string, w, h int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > h {
		t.Errorf("%s %dx%d: %d lines", name, w, h, len(lines))
	}
	for i, l := range lines {
		if lw := ansi.StringWidth(l); lw > w {
			t.Errorf("%s %dx%d: line %d is %d cells wide: %q", name, w, h, i, lw, ansi.Strip(l))
		}
	}
}

func plain(s string) string { return ansi.Strip(s) }

func findChat(t testing.TB, svc *fake.Service, title string) core.Chat {
	t.Helper()
	for _, arch := range []bool{false, true} {
		cs, _ := svc.Chats(context.Background(), core.ChatFilter{IncludeEmpty: true, Archived: arch, IncludeBlocked: true})
		for _, c := range cs {
			if strings.Contains(c.Title, title) {
				return c
			}
		}
	}
	t.Fatalf("chat %q not found", title)
	return core.Chat{}
}

func allChats(t testing.TB, svc *fake.Service) []core.Chat {
	t.Helper()
	cs, err := svc.Chats(context.Background(), core.ChatFilter{IncludeEmpty: true, IncludeBlocked: true})
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func messages(t testing.TB, svc *fake.Service, chat core.ChatID) []core.Message {
	t.Helper()
	ms, err := svc.Messages(context.Background(), chat, core.MessageID{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}
