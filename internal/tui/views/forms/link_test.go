package forms

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/fake"
)

// driveLink runs the model's commands until pred holds or the deadline
// passes; quit reports whether tea.Quit was returned.
func driveLink(t *testing.T, m *Link, cmd tea.Cmd, pred func() bool) (quit bool) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	deadline := time.Now().Add(5 * time.Second)
	for len(queue) > 0 && time.Now().Before(deadline) && !pred() {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		var msg tea.Msg
		select {
		case msg = <-done:
		case <-time.After(2 * time.Second):
			continue
		}
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
			continue
		case tea.QuitMsg:
			return true
		case nil:
			continue
		}
		_, next := m.Update(msg)
		queue = append(queue, next)
	}
	return false
}

func TestLinkModelSuccess(t *testing.T) {
	m := LinkModel(&fake.Linker{Delay: 50 * time.Millisecond, Seed: 7}, "warte test").WithReduceMotion(true)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	driveLink(t, m, m.Init(), func() bool { return m.state == linkWaiting })
	if m.state != linkWaiting || m.qr == nil {
		t.Fatalf("state %d, qr %v", m.state, m.qr != nil)
	}
	out := ansi.Strip(m.Render(100, 40))
	if !strings.Contains(out, LinkInstructions) {
		t.Errorf("no instructions:\n%s", out)
	}
	if !strings.Contains(out, "▀") {
		t.Errorf("no QR code:\n%s", out)
	}
	if !strings.Contains(out, "sgnl://linkdevice") {
		t.Errorf("no URL fallback:\n%s", out)
	}
	// The QR on screen must be the code of the URL (round trip).
	var qrLines []string
	for _, l := range strings.Split(m.Render(100, 40), "\n") {
		if strings.ContainsAny(ansi.Strip(l), "▀▄█") {
			qrLines = append(qrLines, strings.TrimSpace(ansi.Strip(l)))
		}
	}
	if len(qrLines) < m.qr.Size()/2 {
		t.Fatalf("QR has %d lines for %d modules", len(qrLines), m.qr.Size())
	}

	for _, sz := range sizes {
		checkBounds(t, m.Render(sz[0], sz[1]), sz[0], sz[1])
	}
}

func TestLinkModelFlowToDone(t *testing.T) {
	m := LinkModel(&fake.Linker{Delay: 30 * time.Millisecond}, "")
	m.ContinueAfter = 10 * time.Millisecond
	m.WithReduceMotion(true)
	quit := driveLink(t, m, m.Init(), func() bool { return false })
	if m.state != linkDone {
		t.Fatalf("state %d err %v", m.state, m.err)
	}
	if !quit {
		t.Error("did not quit after success")
	}
	self, err := m.Result()
	if err != nil || self.ACI != fake.SelfACI {
		t.Fatalf("Result = %v, %v", self, err)
	}
	if out := ansi.Strip(m.Render(80, 24)); !strings.Contains(out, "Linked") {
		t.Errorf("done screen:\n%s", out)
	}
}

func TestLinkModelErrorRetry(t *testing.T) {
	l := &fake.Linker{Delay: -1, Err: errors.New("phone said no")}
	m := LinkModel(l, "x").WithReduceMotion(true)
	driveLink(t, m, m.Init(), func() bool { return m.state == linkFailed })
	if m.state != linkFailed {
		t.Fatalf("state %d", m.state)
	}
	out := ansi.Strip(m.Render(80, 24))
	if !strings.Contains(out, "phone said no") || !strings.Contains(out, "try again") {
		t.Errorf("error screen:\n%s", out)
	}
	if _, err := m.Result(); err == nil || err.Error() != "phone said no" {
		t.Errorf("Result err = %v", err)
	}
	l.Err = nil
	_, cmd := m.Update(keyMsg("r"))
	if m.state != linkStarting {
		t.Fatalf("retry state %d", m.state)
	}
	driveLink(t, m, cmd, func() bool { return m.state == linkDone })
	if m.state != linkDone {
		t.Fatalf("after retry state %d err %v", m.state, m.err)
	}
}

func TestLinkModelQuit(t *testing.T) {
	m := LinkModel(&fake.Linker{Delay: time.Hour}, "x").WithReduceMotion(true)
	driveLink(t, m, m.Init(), func() bool { return m.state == linkWaiting })
	_, cmd := m.Update(keyMsg("q"))
	if cmd == nil {
		t.Fatal("q returned no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
	if _, err := m.Result(); !errors.Is(err, ErrLinkCanceled) {
		t.Errorf("Result err = %v", err)
	}
}

func TestLinkModelSmallTerminal(t *testing.T) {
	m := LinkModel(&fake.Linker{Delay: time.Hour}, "x").WithReduceMotion(true)
	driveLink(t, m, m.Init(), func() bool { return m.state == linkWaiting })
	for _, sz := range [][2]int{{30, 10}, {60, 20}, {1, 1}, {0, 0}} {
		out := m.Render(sz[0], sz[1])
		checkBounds(t, out, max(sz[0], 0), max(sz[1], 1))
	}
	out := ansi.Strip(m.Render(60, 20))
	if !strings.Contains(out, "too small") || !strings.Contains(out, "sgnl://") {
		t.Errorf("small screen should explain and show the URL:\n%s", out)
	}
	v := m.View()
	if !v.AltScreen {
		t.Error("link screen should use the alt screen")
	}
}

func TestLinkedNameMasksNumber(t *testing.T) {
	if got := LinkedName(core.Contact{E164: "+4915112345642", ACI: "a"}); got != "+49 ••• ••• 42" {
		t.Errorf("number only: %q", got)
	}
	if got := LinkedName(core.Contact{ProfileName: "Ada", E164: "+4915112345642"}); got != "Ada" {
		t.Errorf("profile name: %q", got)
	}
	if got := LinkedName(core.Contact{Username: "ada.42", E164: "+4915112345642"}); got != "ada.42" {
		t.Errorf("username: %q", got)
	}
	if got := LinkedName(core.Contact{ACI: "a"}); got != "" {
		t.Errorf("nothing known: %q", got)
	}
	if got := MaskPhone("+1234"); strings.ContainsAny(got, "1234") {
		t.Errorf("short number leaks: %q", got)
	}

	m := LinkModel(&fake.Linker{}, "warte test").WithReduceMotion(true)
	m.state = linkDone
	m.self = core.Contact{E164: "+4915112345642", ACI: "a"}
	out := ansi.Strip(m.Render(100, 30))
	if strings.Contains(out, "15112345642") || !strings.Contains(out, "+49 ••• ••• 42") {
		t.Errorf("done screen:\n%s", out)
	}
}
