package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

// bigService serves a synthetic history of n messages for one chat.
type bigService struct {
	*fake.Service
	chat core.ChatID
	msgs []core.Message
}

func newBigService(n int) *bigService {
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	b := &bigService{Service: svc, chat: fake.ChatDev}
	start := testNow.Add(-time.Duration(n) * time.Minute)
	words := []string{"lorem", "ipsum", "**dolor**", "sit", "amet", "https://example.com/x", "consectetur", "🎉", "adipiscing", "elit"}
	for i := 0; i < n; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		author := fake.DevACI
		out := i%3 == 0
		if out {
			author = fake.SelfACI
		}
		body := ""
		for k := 0; k < 3+i%17; k++ {
			body += words[(i+k)%len(words)] + " "
		}
		m := core.Message{
			ID:         core.MessageID{Chat: b.chat, Author: author, Timestamp: uint64(at.UnixMilli())},
			SenderName: "Dev Patel",
			Outgoing:   out,
			SentAt:     at,
			ReceivedAt: at,
			Body:       body,
		}
		if out {
			m.Status = core.StatusRead
			m.SenderName = ""
		}
		if i%11 == 0 {
			m.Reactions = []core.Reaction{{Emoji: "👍", From: fake.AliceACI, FromName: "Alice"}}
		}
		b.msgs = append(b.msgs, m)
	}
	return b
}

func (b *bigService) Messages(ctx context.Context, chat core.ChatID, before core.MessageID, limit int) ([]core.Message, error) {
	if chat != b.chat {
		return b.Service.Messages(ctx, chat, before, limit)
	}
	end := len(b.msgs)
	if !before.IsZero() {
		for i, m := range b.msgs {
			if m.ID == before {
				end = i
				break
			}
		}
	}
	start := max(end-limit, 0)
	return append([]core.Message(nil), b.msgs[start:end]...), nil
}

func TestManyMessagesStaysFast(t *testing.T) {
	big := newBigService(5000)
	t.Cleanup(func() { _ = big.Close() })
	cfg := config.Default()
	none := false
	blocks := termimg.HalfBlocks
	a := New(big, cfg, Options{Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &none, Images: &blocks, SkipStart: true, Notifier: nopNotifier{}})
	a.noPump = true
	t.Cleanup(a.Close)
	h := &harness{t: t, a: a, svc: big.Service}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 48})
	h.send(OpenChatForTest(big.chat))
	if len(a.conv.msgs) != pageSize {
		t.Fatalf("loaded %d messages, want one page", len(a.conv.msgs))
	}

	// Typing latency: keystrokes in the composer with a long history.
	h.keys("i")
	start := time.Now()
	const n = 200
	for i := 0; i < n; i++ {
		a.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	}
	limit := 20 * time.Millisecond
	if raceEnabled {
		limit *= 10
	}
	if per := time.Since(start) / n; per > limit {
		t.Errorf("typing: %v per keystroke", per)
	}
	h.keys("<esc>")

	// Navigation: moving through history loads pages lazily.
	start = time.Now()
	for i := 0; i < 400; i++ {
		_, cmd := a.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
		h.run(cmd, 0)
	}
	if per := time.Since(start) / 400; per > limit {
		t.Errorf("k: %v per keystroke", per)
	}
	if len(a.conv.msgs) <= pageSize {
		t.Fatalf("history not lazily loaded: %d", len(a.conv.msgs))
	}

	// gg loads everything; G returns.
	start = time.Now()
	h.keys("gg")
	if a.conv.sel != 0 || len(a.conv.msgs) != 5000 {
		t.Fatalf("gg: sel=%d loaded=%d", a.conv.sel, len(a.conv.msgs))
	}
	h.keys("G")
	if a.conv.sel != 4999 {
		t.Fatalf("G: sel=%d", a.conv.sel)
	}
	if d := time.Since(start); d > 150*limit {
		t.Errorf("gg/G took %v", d)
	}
	checkFrame(t, h.view(), 160, 48, "big history")
	// Rendering only touches visible messages.
	if len(a.conv.cache) > 2000 {
		t.Errorf("cache holds %d blocks; virtualisation broken?", len(a.conv.cache))
	}
}

// OpenChatForTest returns the message that opens a chat.
func OpenChatForTest(id core.ChatID) tea.Msg { return ui.OpenChatMsg{Chat: id} }

// benchApp returns an App with a 5,000-message chat fully loaded at 160x48.
func benchApp(b *testing.B) (*App, *harness) {
	big := newBigService(5000)
	b.Cleanup(func() { _ = big.Close() })
	none := false
	blocks := termimg.HalfBlocks
	a := New(big, config.Default(), Options{Now: func() time.Time { return testNow }, Getenv: func(string) string { return "" },
		NerdFonts: &none, Images: &blocks, SkipStart: true, Notifier: nopNotifier{}})
	a.noPump = true
	b.Cleanup(a.Close)
	h := &harness{t: b, a: a, svc: big.Service}
	h.send(tea.WindowSizeMsg{Width: 160, Height: 48})
	h.send(OpenChatForTest(big.chat))
	h.keys("gg")
	h.keys("G")
	return a, h
}

// BenchmarkRenderWarm measures a full frame render with cached message
// blocks (the common case: a key press that moves the selection).
func BenchmarkRenderWarm(b *testing.B) {
	a, _ := benchApp(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.render()
	}
}

// BenchmarkRenderCold measures a frame render after the block cache was
// dropped (resize / theme switch).
func BenchmarkRenderCold(b *testing.B) {
	a, _ := benchApp(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.conv.invalidate()
		_ = a.render()
	}
}

// BenchmarkKeyMove measures Update+render for j/k in the conversation.
func BenchmarkKeyMove(b *testing.B) {
	a, _ := benchApp(b)
	k := tea.KeyPressMsg{Code: 'k', Text: "k"}
	j := tea.KeyPressMsg{Code: 'j', Text: "j"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			a.Update(k)
		} else {
			a.Update(j)
		}
	}
}
