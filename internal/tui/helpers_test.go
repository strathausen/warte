package tui

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/notify"
	"warte/internal/termimg"
)

// testNow is the fixed clock used by tests.
var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)

type harness struct {
	t   testing.TB
	a   *App
	svc *fake.Service
}

type harnessOpt func(*fake.Options, *config.Config, *Options)

func withLatency(d time.Duration) harnessOpt {
	return func(o *fake.Options, _ *config.Config, _ *Options) { o.Latency = d }
}

func withConfig(f func(*config.Config)) harnessOpt {
	return func(_ *fake.Options, c *config.Config, _ *Options) { f(c) }
}

func withLive() harnessOpt {
	return func(o *fake.Options, _ *config.Config, _ *Options) {
		o.Live = true
		o.LiveMinInterval = 20 * time.Millisecond
		o.LiveMaxInterval = 60 * time.Millisecond
		o.TypingDelay = -1
		o.CallRing = -1
		o.Clock = nil // live activity needs a moving clock
	}
}

// newHarness builds an App over a fake service at the given size. Events
// are pumped explicitly with pump/waitFor.
func newHarness(t *testing.T, w, h int, opts ...harnessOpt) *harness {
	t.Helper()
	fo := fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }}
	cfg := config.Default()
	cfg.Theme = "charm"
	cfg.Images = config.ImagesBlocks
	none := false
	blocks := termimg.HalfBlocks
	o := Options{
		Now:       func() time.Time { return testNow },
		Getenv:    func(string) string { return "" },
		NerdFonts: &none,
		Images:    &blocks,
		SkipStart: true,
		Notifier:  nopNotifier{},
		LockPath:  filepath.Join(t.TempDir(), "lock"),
		TempDir:   t.TempDir(),
	}
	for _, f := range opts {
		f(&fo, &cfg, &o)
	}
	if fo.Clock == nil {
		o.Now = nil
	}
	svc := fake.New(fo)
	t.Cleanup(func() { _ = svc.Close() })
	a := New(svc, cfg, o)
	a.noPump = true
	t.Cleanup(a.Close)
	h2 := &harness{t: t, a: a, svc: svc}
	h2.send(tea.WindowSizeMsg{Width: w, Height: h})
	return h2
}

type nopNotifier struct{}

func (nopNotifier) Notify(notify.Notification) error { return nil }

func stripANSI(s string) string { return ansi.Strip(s) }

// send delivers msg and runs the resulting commands (briefly).
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.a.Update(msg)
	h.run(cmd, 0)
}

// run executes cmd and feeds its messages back, skipping slow commands
// (ticks) so tests stay fast.
func (h *harness) run(cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 40 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(150 * time.Millisecond):
		return
	}
	if msg == nil {
		return
	}
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			h.run(c, depth+1)
		}
		return
	case animTickMsg, toastExpire, flushKeysMsg, typingCheckMsg:
		return
	}
	// tea.Sequence produces an unexported slice of commands.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := 0; i < v.Len(); i++ {
			h.run(v.Index(i).Interface().(tea.Cmd), depth+1)
		}
		return
	}
	if isQuitMsg(msg) {
		return
	}
	_, next := h.a.Update(msg)
	h.run(next, depth+1)
}

func isQuitMsg(msg tea.Msg) bool {
	_, ok := msg.(tea.QuitMsg)
	return ok
}

// keys types a sequence of keys: single characters, or names in angle
// brackets like "<enter>", "<esc>", "<C-c>", "<tab>", "<up>".
func (h *harness) keys(seq string) {
	h.t.Helper()
	for _, k := range parseTestKeys(seq) {
		h.send(k)
	}
}

func parseTestKeys(seq string) []tea.KeyPressMsg {
	var out []tea.KeyPressMsg
	for len(seq) > 0 {
		if seq[0] == '<' {
			if end := strings.IndexByte(seq, '>'); end > 0 {
				out = append(out, namedKey(seq[1:end]))
				seq = seq[end+1:]
				continue
			}
		}
		r := []rune(seq)[0]
		seq = seq[len(string(r)):]
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func namedKey(name string) tea.KeyPressMsg {
	switch strings.ToLower(name) {
	case "enter", "cr":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "bs":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	if strings.HasPrefix(strings.ToLower(name), "c-") {
		return tea.KeyPressMsg{Code: rune(name[2]), Mod: tea.ModCtrl}
	}
	if strings.HasPrefix(strings.ToLower(name), "a-") {
		r := rune(name[2])
		if strings.EqualFold(name[2:], "cr") {
			r = tea.KeyEnter
		}
		return tea.KeyPressMsg{Code: r, Mod: tea.ModAlt}
	}
	panic("unknown key " + name)
}

// pump delivers every event the service has queued.
func (h *harness) pump() {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev := <-h.svc.Events():
			h.send(eventMsg{ev: ev})
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}

// waitFor pumps events until cond holds or the timeout passes.
func (h *harness) waitFor(timeout time.Duration, cond func() bool) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case ev := <-h.svc.Events():
			h.send(eventMsg{ev: ev})
		case <-time.After(20 * time.Millisecond):
		}
	}
	return cond()
}

func (h *harness) view() string {
	return h.a.View().Content
}

func (h *harness) plain() string { return stripANSI(h.view()) }

func (h *harness) lastMsg() core.Message {
	c := h.a.conv
	if len(c.msgs) == 0 {
		h.t.Fatal("no messages")
	}
	return c.msgs[len(c.msgs)-1].m
}

// checkFrame asserts the frame is exactly w x h cells.
func checkFrame(t *testing.T, frame string, w, h int, label string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != h {
		t.Fatalf("%s: %d lines, want %d", label, len(lines), h)
	}
	for i, l := range lines {
		if got := width(l); got != w {
			t.Fatalf("%s: line %d is %d cells wide, want %d:\n%q", label, i, got, w, stripANSI(l))
		}
	}
}
