package tui

import (
	"bytes"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
)

// TestProgramEndToEnd runs the App inside a real Bubble Tea program (event
// pump included): startup, navigation, sending, quitting.
func TestProgramEndToEnd(t *testing.T) {
	svc := fake.New(fake.Options{Seed: 1, Latency: 20 * time.Millisecond})
	t.Cleanup(func() { _ = svc.Close() })
	cfg := config.Default()
	cfg.Theme = "charm"
	none := false
	blocks := termimg.HalfBlocks
	app := New(svc, cfg, Options{Getenv: func(string) string { return "" }, NerdFonts: &none, Images: &blocks, Notifier: nopNotifier{}})
	t.Cleanup(app.Close)

	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 40))
	waitOut := func(want string) {
		t.Helper()
		teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(want)) },
			teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(20*time.Millisecond))
	}
	waitOut("Alice Chen")
	tm.Type("j")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitOut("members")
	tm.Type("ping from teatest")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	waitOut("ping from teatest")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	tm.Type(":q")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))

	// The message reached the backend.
	msgs, err := svc.Messages(t.Context(), fake.ChatHike, core.MessageID{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if last := msgs[len(msgs)-1]; last.Body != "ping from teatest" || !last.Outgoing {
		t.Fatalf("last message = %q", last.Body)
	}
}
