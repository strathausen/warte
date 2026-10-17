package media

import (
	"context"
	"os"
	"reflect"
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

// fakeImages implements ui.Images with half-block-like rows (or kitty
// placeholders) that are always ready.
type fakeImages struct {
	kitty    bool
	notReady bool
	rowsReq  []string // "key@CxR" of every Rows call
}

func (f *fakeImages) Enabled() bool { return true }
func (f *fakeImages) Inline() bool  { return true }
func (f *fakeImages) Size(ref ui.ImageRef, maxCols, maxRows int) (int, int) {
	w, h := ref.Attachment.Width, ref.Attachment.Height
	if w <= 0 || h <= 0 {
		w, h = 4, 3
	}
	c, r := termimg.FitCells(w, h, max(maxCols, 1), max(maxRows, 1), termimg.CellSize{Width: 1, Height: 2}, true)
	return max(c, 1), max(r, 1)
}
func (f *fakeImages) Rows(ref ui.ImageRef, cols, rows int) ([]string, bool) {
	f.rowsReq = append(f.rowsReq, ref.Key())
	if f.kitty {
		return termimg.KittyEncoder{}.Placeholder(300, cols, rows), !f.notReady
	}
	out := make([]string, rows)
	for i := range out {
		out[i] = "\x1b[38;2;10;20;30m\x1b[48;2;40;50;60m" + strings.Repeat("▀", cols) + "\x1b[m"
	}
	return out, !f.notReady
}

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func newSvc(t *testing.T) *fake.Service {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func newCtx(t *testing.T, svc core.Service, imgs ui.Images) ui.Context {
	t.Helper()
	th, _ := theme.Get("charm")
	var selected *core.Message
	return ui.Context{
		Svc:      svc,
		Styles:   theme.NewStyles(th),
		Icons:    theme.Icons(false),
		Theme:    th,
		Config:   config.Default(),
		Images:   imgs,
		Keys:     keymap.New(),
		Now:      func() time.Time { return testNow },
		Chat:     func() core.ChatID { return "" },
		Selected: func() *core.Message { return selected },
	}
}

// allMessages returns every message of every chat.
func allMessages(t *testing.T, svc core.Service) []core.Message {
	t.Helper()
	ctx := context.Background()
	chats, err := svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	arch, _ := svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true, Archived: true})
	chats = append(chats, arch...)
	var out []core.Message
	for _, c := range chats {
		msgs, err := svc.Messages(ctx, c.ID, core.MessageID{}, 10000)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, msgs...)
	}
	return out
}

// findMsg returns the first message satisfying pred.
func findMsg(t *testing.T, svc core.Service, pred func(core.Message) bool) core.Message {
	t.Helper()
	for _, m := range allMessages(t, svc) {
		if pred(m) {
			return m
		}
	}
	t.Fatal("no matching message in fake data")
	return core.Message{}
}

func firstAtt(m core.Message, pred func(core.Attachment) bool) core.Attachment {
	for _, a := range m.Attachments {
		if pred(a) {
			return a
		}
	}
	return core.Attachment{}
}

func hasAtt(pred func(core.Attachment) bool) func(core.Message) bool {
	return func(m core.Message) bool {
		if m.ViewOnce || m.RemoteDeleted {
			return false
		}
		for _, a := range m.Attachments {
			if pred(a) {
				return true
			}
		}
		return false
	}
}

func isPhoto(a core.Attachment) bool { return a.IsImage() && !a.GIF && a.ContentType != "image/gif" }
func isGIF(a core.Attachment) bool   { return a.GIF || a.ContentType == "image/gif" }

// run executes cmd (following batches) and returns the produced messages.
// Commands that block longer than the timeout (ticks, process waits) are
// abandoned.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(2 * time.Second):
		return nil
	}
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// feed delivers msgs to o and keeps processing the resulting commands'
// messages that are of the given types (to avoid ticks looping forever).
func feed(o ui.Overlay, cmd tea.Cmd, keep func(tea.Msg) bool, rounds int) (ui.Overlay, []tea.Msg) {
	var other []tea.Msg
	queue := run(cmd)
	for i := 0; i < rounds && len(queue) > 0 && o != nil; i++ {
		var next []tea.Msg
		for _, m := range queue {
			if !keep(m) {
				other = append(other, m)
				continue
			}
			var c tea.Cmd
			o, c = o.Update(m)
			next = append(next, run(c)...)
			if o == nil {
				break
			}
		}
		queue = next
	}
	return o, append(other, queue...)
}

func typeIs[T any](m tea.Msg) bool { _, ok := m.(T); return ok }

func act(a keymap.Action) ui.ActionMsg { return ui.ActionMsg{Action: a, Count: 1} }

func key(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

var sizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}, {30, 6}, {12, 3}}

// checkView asserts the overlay renders within w x h at every size.
func checkView(t *testing.T, o ui.Overlay) {
	t.Helper()
	for _, s := range sizes {
		cw, ch := o.Size(s[0]-10, s[1]-6)
		cw, ch = max(min(cw, s[0]-10), 1), max(min(ch, s[1]-6), 1)
		out := o.View(cw, ch)
		lines := strings.Split(out, "\n")
		if len(lines) > ch {
			t.Errorf("%T at %dx%d: %d lines > %d", o, cw, ch, len(lines), ch)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > cw {
				t.Errorf("%T at %dx%d: line %d is %d cells wide: %q", o, cw, ch, i, w, l)
			}
		}
		if strings.ContainsAny(ansi.Strip(out), "\x07\x00") {
			t.Errorf("%T: control characters in output", o)
		}
		_ = o.Title()
		if f, ok := o.(ui.Footer); ok {
			_ = f.Footer()
		}
	}
}

func msgsOfType[T any](msgs []tea.Msg) []T {
	var out []T
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			out = append(out, v)
		}
	}
	return out
}

func typeName(m tea.Msg) string { return reflect.TypeOf(m).String() }

type timeDuration = time.Duration

func (fakeImages) kittyRows(id uint32) []string { return termimg.KittyEncoder{}.Placeholder(id, 3, 2) }

// restoreHooks returns a function restoring the package's test seams.
func restoreHooks() func() {
	sd, ra, ci, dd, lp, sa := startDetached, removeAfter, copyImage, downloadsDir, lookPath, StartAudio
	return func() {
		startDetached, removeAfter, copyImage, downloadsDir, lookPath, StartAudio = sd, ra, ci, dd, lp, sa
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := st.Mode().Perm(); got != want {
		t.Errorf("%s: mode %v, want %v", path, got, want)
	}
}

func clickAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}
