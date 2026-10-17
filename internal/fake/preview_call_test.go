package fake

import (
	"testing"

	"warte/internal/core"
)

// TestCallPreviewHasNoSender: call lines already name the caller, so the
// chat list must not prefix them with the sender ("Alice: 📞 Alice …").
func TestCallPreviewHasNoSender(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	call := findMsg(t, s, ChatAlice, func(m core.Message) bool { return m.Kind == core.KindCall && !m.Outgoing })
	text := findMsg(t, s, ChatAlice, func(m core.Message) bool { return m.Kind == core.KindNormal && !m.Outgoing })
	s.mu.Lock()
	cp, tp := s.previewLocked(&call), s.previewLocked(&text)
	s.mu.Unlock()
	if cp.Sender != "" {
		t.Errorf("call preview sender = %q, want none (text %q)", cp.Sender, cp.Text)
	}
	if tp.Sender == "" {
		t.Error("normal preview lost its sender")
	}
}
