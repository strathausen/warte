package fake

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"warte/internal/core"
)

func fastLive(seed int64) Options {
	return Options{
		Seed: seed, Live: true,
		LiveMinInterval: time.Millisecond, LiveMaxInterval: 3 * time.Millisecond,
		TypingDelay: time.Millisecond, CallRing: time.Millisecond, Latency: time.Millisecond,
	}
}

func TestLiveEmitsActivity(t *testing.T) {
	s, _ := newTestService(t, fastLive(11))
	ctx := context.Background()
	// Give the react action a target.
	if _, err := s.Send(ctx, ChatHike, core.Outgoing{Body: "who's up for the sauna?"}); err != nil {
		t.Fatal(err)
	}
	var typing, incoming, ringing, ended, missed, reacted bool
	deadline := time.After(10 * time.Second)
	for !(typing && incoming && ringing && ended && missed && reacted) {
		select {
		case ev := <-s.Events():
			switch e := ev.(type) {
			case core.TypingChanged:
				typing = typing || (e.Typing && e.Who != SelfACI && e.Name != "")
			case core.MessageAdded:
				m := e.Message
				switch {
				case m.Kind == core.KindNormal && !m.Outgoing:
					incoming = true
					if m.Body == "" || m.SenderName == "" {
						t.Fatalf("live message without body/sender: %+v", m)
					}
				case m.Kind == core.KindCall:
					missed = m.Call != nil && m.Call.Missed && m.Call.Incoming && e.Notify
				}
			case core.IncomingCall:
				if e.Ringing {
					ringing = true
				} else {
					ended = true
				}
			case core.MessageUpdated:
				for _, r := range e.Message.Reactions {
					if e.Message.Outgoing && r.From != SelfACI && r.At.Equal(testNow) {
						reacted = true
					}
				}
			}
		case <-deadline:
			t.Fatalf("live activity incomplete: typing=%v incoming=%v ringing=%v ended=%v missed=%v reacted=%v",
				typing, incoming, ringing, ended, missed, reacted)
		}
	}
}

func TestLiveMutedChatsDoNotNotify(t *testing.T) {
	s, _ := newTestService(t, fastLive(5))
	ctx := context.Background()
	forever := muteForever
	for _, id := range liveOrder {
		if err := s.UpdateChat(ctx, id, core.ChatUpdate{MutedUntil: &forever}); err != nil {
			t.Fatal(err)
		}
	}
	got := 0
	for got < 5 {
		ev := waitEvent(t, s, 5*time.Second, func(ev core.Event) bool {
			m, ok := ev.(core.MessageAdded)
			return ok && !m.Message.Outgoing && m.Message.Kind != core.KindSystem
		}).(core.MessageAdded)
		if ev.Notify {
			t.Fatalf("muted chat notified: %s", ev.Message.ID.Chat)
		}
		got++
	}
}

func TestAutoReply(t *testing.T) {
	opts := fastLive(2)
	opts.LiveMinInterval, opts.LiveMaxInterval = time.Hour, time.Hour // only auto-replies
	s, _ := newTestService(t, opts)
	ctx := context.Background()
	drain(s)
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "guess what"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s, 2*time.Second, func(ev core.Event) bool {
		ty, ok := ev.(core.TypingChanged)
		return ok && ty.Chat == ChatAlice && ty.Who == AliceACI && ty.Typing
	})
	ev := waitEvent(t, s, 2*time.Second, func(ev core.Event) bool {
		m, ok := ev.(core.MessageAdded)
		return ok && m.Message.ID.Chat == ChatAlice && !m.Message.Outgoing
	}).(core.MessageAdded)
	known := false
	for _, w := range wittyReplies {
		body, _, _ := parseMarkup(w, SelfACI)
		known = known || body == ev.Message.Body
	}
	if !known || !ev.Notify || ev.Message.SenderName != "Alice Chen" {
		t.Fatalf("auto reply: %+v", ev)
	}
	c, _ := s.Chat(ctx, ChatAlice)
	if c.Unread != 1 || c.LastMessage.Text != ev.Message.Body {
		t.Errorf("chat after reply: unread=%d last=%q", c.Unread, c.LastMessage.Text)
	}
	// Group messages get no auto reply, Note to Self neither.
	drain(s)
	if _, err := s.Send(ctx, ChatNoteToSelf, core.Outgoing{Body: "memo"}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case ev := <-s.Events():
			if m, ok := ev.(core.MessageAdded); ok && !m.Message.Outgoing {
				t.Fatalf("unexpected reply: %+v", m.Message)
			}
		case <-timeout:
			return
		}
	}
}

func TestLiveDeterministic(t *testing.T) {
	collect := func() []string {
		opts := fastLive(42)
		opts.Latency = -1
		s, _ := newTestService(t, opts)
		defer s.Close()
		var bodies []string
		for len(bodies) < 6 {
			ev := waitEvent(t, s, 5*time.Second, func(ev core.Event) bool {
				m, ok := ev.(core.MessageAdded)
				return ok && !m.Message.Outgoing
			}).(core.MessageAdded)
			bodies = append(bodies, string(ev.Message.ID.Chat)+":"+ev.Message.Body)
		}
		return bodies
	}
	a, b := collect(), collect()
	if !slices.Equal(a, b) {
		t.Fatalf("live activity differs for the same seed:\n%v\n%v", a, b)
	}
}

func TestLiveCloseStopsGoroutines(t *testing.T) {
	base := runtime.NumGoroutine()
	for i := 0; i < 3; i++ {
		opts := fastLive(int64(i))
		opts.Clock = func() time.Time { return testNow }
		s := New(opts)
		if err := s.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3; j++ {
			_, _ = s.Send(context.Background(), ChatAlice, core.Outgoing{Body: "ping"})
		}
		time.Sleep(20 * time.Millisecond) // let activity get going without a reader
		closed := make(chan struct{})
		go func() { s.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("Close hung")
		}
	}
	assertGoroutines(t, base)
}

func TestLinker(t *testing.T) {
	ctx := context.Background()
	l := &Linker{Delay: 3 * time.Millisecond, Seed: 1}
	ch, err := l.Link(ctx, "warte on laptop")
	if err != nil {
		t.Fatal(err)
	}
	var evs []core.LinkEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	if len(evs) != 4 {
		t.Fatalf("events: %#v", evs)
	}
	qr, ok := evs[0].(core.LinkQR)
	if !ok {
		t.Fatalf("first event %T", evs[0])
	}
	re := regexp.MustCompile(`^sgnl://linkdevice\?uuid=[A-Za-z0-9_%-]+&pub_key=[A-Za-z0-9%]+$`)
	if !re.MatchString(qr.URL) {
		t.Fatalf("QR URL: %s", qr.URL)
	}
	u, _ := url.Parse(qr.URL)
	if pk := u.Query().Get("pub_key"); !strings.HasPrefix(pk, "B") || len(pk) != 44 {
		t.Errorf("pub_key should be a 33-byte 0x05-prefixed key: %q", pk)
	}
	for _, ev := range evs[1:3] {
		if p, ok := ev.(core.LinkProgress); !ok || p.Stage == "" {
			t.Fatalf("want progress, got %#v", ev)
		}
	}
	done, ok := evs[3].(core.LinkDone)
	if !ok || done.Self.ACI != SelfACI || !done.Self.IsSelf {
		t.Fatalf("done: %#v", evs[3])
	}
	ch2, _ := (&Linker{Delay: -1, Seed: 1}).Link(ctx, "x")
	if first := <-ch2; first.(core.LinkQR).URL != qr.URL {
		t.Error("QR URL not deterministic")
	}
	for range ch2 {
	}
	ch3, _ := (&Linker{Delay: -1, Seed: 2}).Link(ctx, "x")
	if first := <-ch3; first.(core.LinkQR).URL == qr.URL {
		t.Error("different seeds gave the same URL")
	}
	for range ch3 {
	}
}

func TestLinkerErrors(t *testing.T) {
	boom := errors.New("phone said no")
	ch, _ := (&Linker{Delay: -1, Err: boom}).Link(context.Background(), "x")
	var last core.LinkEvent
	for ev := range ch {
		last = ev
	}
	if le, ok := last.(core.LinkError); !ok || !errors.Is(le.Err, boom) {
		t.Fatalf("want LinkError(boom), got %#v", last)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, _ = (&Linker{Delay: time.Hour}).Link(ctx, "x")
	if _, ok := (<-ch).(core.LinkQR); !ok {
		t.Fatal("no QR")
	}
	cancel()
	last = nil
	for ev := range ch { // must close promptly after cancel
		last = ev
	}
	if le, ok := last.(core.LinkError); !ok || !errors.Is(le.Err, context.Canceled) {
		t.Fatalf("want LinkError(canceled), got %#v", last)
	}

	if _, err := (&Linker{}).Link(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("Link with cancelled ctx: %v", err)
	}
}
