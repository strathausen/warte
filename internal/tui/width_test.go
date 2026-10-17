package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"warte/internal/config"
	"warte/internal/fake"
	"warte/internal/termimg"
)

// TestRendererUsesGraphemeWidths runs the App in a real program on a
// terminal that does not answer the mode 2027 query. Lipgloss lays rows out
// with grapheme widths, so the renderer must measure the same way: with
// wcwidth a ZWJ family counts 6 cells instead of 2, and a row of them was
// cut off (with the pane border) after a third of the text.
func TestRendererUsesGraphemeWidths(t *testing.T) {
	svc := fake.New(fake.Options{Seed: 1, Latency: 20 * time.Millisecond})
	t.Cleanup(func() { _ = svc.Close() })
	cfg := config.Default()
	cfg.Theme = "charm"
	none := false
	blocks := termimg.HalfBlocks
	app := New(svc, cfg, Options{Getenv: func(string) string { return "" }, NerdFonts: &none, Images: &blocks, Notifier: nopNotifier{}})
	t.Cleanup(app.Close)

	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	waitOut := func(ok func([]byte) bool) {
		t.Helper()
		var seen bytes.Buffer
		teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
			seen.Write(b)
			return ok(seen.Bytes())
		}, teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	}
	waitOut(func(b []byte) bool {
		return bytes.Contains(b, []byte(ansi.SetModeUnicodeCore)) && bytes.Contains(b, []byte("Alice Chen"))
	})
	family := strings.Repeat("👨‍👩‍👧", 12) + " end"
	tm.Type("i")
	tm.Type(family)
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitOut(func(b []byte) bool {
		return bytes.Contains([]byte(ansi.Strip(string(b))), []byte(family))
	})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Type(":q")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}
