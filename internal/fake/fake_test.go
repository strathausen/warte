package fake

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/gif"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"warte/internal/core"
)

// testNow is the fixed instant tests run at.
var testNow = time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)

// fakeClock is a manually advanced clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newTestService returns a started service with a fake clock and instant
// latency unless opts override them. It is closed on cleanup.
func newTestService(t *testing.T, opts Options) (*Service, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: testNow}
	if opts.Clock == nil {
		opts.Clock = clk.Now
	}
	if opts.Latency == 0 {
		opts.Latency = -1
	}
	s := New(opts)
	t.Cleanup(func() { s.Close() })
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return s, clk
}

// waitEvent reads events until pred matches or the timeout elapses.
func waitEvent(t *testing.T, s *Service, timeout time.Duration, pred func(core.Event) bool) core.Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("events channel closed")
			}
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
			return nil
		}
	}
}

// drain discards pending events for a moment.
func drain(s *Service) {
	for {
		select {
		case <-s.Events():
		case <-time.After(20 * time.Millisecond):
			return
		}
	}
}

func allMessages(t *testing.T, s *Service, chat core.ChatID) []core.Message {
	t.Helper()
	ms, err := s.Messages(context.Background(), chat, core.MessageID{}, 10_000)
	if err != nil {
		t.Fatalf("Messages(%s): %v", chat, err)
	}
	return ms
}

func TestImplementsInterfaces(t *testing.T) {
	var _ core.Service = New(Options{Seed: 1, Clock: func() time.Time { return testNow }})
	var _ core.Linker = &Linker{}
}

func TestDeterministic(t *testing.T) {
	ctx := context.Background()
	clock := func() time.Time { return testNow }
	a, b := New(Options{Seed: 7, Clock: clock}), New(Options{Seed: 7, Clock: clock})
	defer a.Close()
	defer b.Close()
	ca, _ := a.Chats(ctx, core.ChatFilter{IncludeBlocked: true, IncludeEmpty: true})
	cb, _ := b.Chats(ctx, core.ChatFilter{IncludeBlocked: true, IncludeEmpty: true})
	if !reflect.DeepEqual(ca, cb) {
		t.Fatal("chats differ between services with the same seed")
	}
	for _, c := range ca {
		if !reflect.DeepEqual(allMessages(t, a, c.ID), allMessages(t, b, c.ID)) {
			t.Fatalf("messages of %s differ for the same seed", c.Title)
		}
	}
	c := New(Options{Seed: 8, Clock: clock})
	defer c.Close()
	if reflect.DeepEqual(allMessages(t, a, ChatHike), allMessages(t, c, ChatHike)) {
		t.Error("different seeds produced identical timelines")
	}
}

func TestDemoDataCoverage(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	chats, err := s.Chats(ctx, core.ChatFilter{IncludeBlocked: true})
	if err != nil {
		t.Fatal(err)
	}
	archived, _ := s.Chats(ctx, core.ChatFilter{Archived: true})
	if n := len(chats) + len(archived); n < 14 {
		t.Errorf("want ~14 chats, got %d", n)
	}
	var have struct {
		noteToSelf, announce, selfAdmin, muted, pinned, request, blocked, archived bool
		groups                                                                     int
	}
	for _, c := range append(chats, archived...) {
		have.noteToSelf = have.noteToSelf || c.Kind == core.ChatNoteToSelf
		have.announce = have.announce || c.OnlyAdminsSend
		have.selfAdmin = have.selfAdmin || (c.Kind == core.ChatGroup && c.SelfIsAdmin)
		have.muted = have.muted || c.Muted(testNow)
		have.pinned = have.pinned || c.Pinned
		have.request = have.request || c.MessageRequest
		have.blocked = have.blocked || c.Blocked
		have.archived = have.archived || c.Archived
		if c.Kind == core.ChatGroup {
			have.groups++
		}
	}
	if !have.noteToSelf || !have.announce || !have.selfAdmin || !have.muted || !have.pinned ||
		!have.request || !have.blocked || !have.archived || have.groups < 3 {
		t.Errorf("missing chat varieties: %+v", have)
	}

	var feat = map[string]bool{}
	for _, c := range append(chats, archived...) {
		for _, m := range allMessages(t, s, c.ID) {
			mark := func(name string, ok bool) {
				if ok {
					feat[name] = true
				}
			}
			mark("quote", m.Quote != nil)
			mark("reaction", len(m.Reactions) > 0)
			mark("edit", m.Edited && len(m.EditHistory) > 0)
			mark("deleted", m.RemoteDeleted)
			mark("mention", m.MentionsSelf)
			mark("preview", m.Preview != nil)
			mark("sticker", m.Sticker != nil)
			mark("poll", m.Poll != nil)
			mark("pinned", m.Pinned)
			mark("viewonce", m.ViewOnce)
			mark("expiring", m.ExpiresIn > 0)
			mark("call", m.Kind == core.KindCall)
			mark("system", m.Kind == core.KindSystem)
			mark("failed", m.Status == core.StatusFailed)
			mark("contact", m.Contact != nil)
			for _, r := range m.Ranges {
				mark("style"+string(rune('0'+r.Style)), true)
			}
			for _, a := range m.Attachments {
				mark("image", a.IsImage() && !a.GIF)
				mark("gif", a.GIF)
				mark("voice", a.VoiceNote)
				mark("video", a.IsVideo())
				mark("pdf", a.ContentType == "application/pdf")
			}
		}
	}
	for _, f := range []string{"quote", "reaction", "edit", "deleted", "mention", "preview", "sticker", "poll",
		"pinned", "viewonce", "expiring", "call", "system", "failed", "contact", "image", "gif", "voice", "video", "pdf",
		"style1", "style2", "style3", "style4", "style5", "style6"} {
		if !feat[f] {
			t.Errorf("demo data lacks %s", f)
		}
	}

	stories, _ := s.Stories(ctx)
	var text, media bool
	for _, st := range stories {
		text = text || st.Text != nil
		media = media || st.Media != nil
	}
	if !text || !media {
		t.Errorf("stories: text=%v media=%v", text, media)
	}
	calls, _ := s.CallLog(ctx)
	if len(calls) < 5 {
		t.Errorf("call log has %d entries", len(calls))
	}
	packs, _ := s.StickerPacks(ctx)
	if len(packs) == 0 || len(packs[0].Stickers) < 8 {
		t.Errorf("sticker packs: %+v", packs)
	}
	folders, _ := s.Folders(ctx)
	if len(folders) < 2 {
		t.Errorf("folders: %+v", folders)
	}
}

// TestRangesAreValidUTF16 checks every body range against UTF-16 lengths and
// that mentions point at U+FFFC.
func TestRangesAreValidUTF16(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	chats, _ := s.Chats(context.Background(), core.ChatFilter{IncludeBlocked: true})
	for _, c := range chats {
		for _, m := range allMessages(t, s, c.ID) {
			u := utf16.Encode([]rune(m.Body))
			for _, r := range m.Ranges {
				if r.Start < 0 || r.Length <= 0 || r.Start+r.Length > len(u) {
					t.Fatalf("%s: range %+v out of bounds for %q", m.ID, r, m.Body)
				}
				if r.Style == core.StyleMention && (r.Length != 1 || u[r.Start] != 0xFFFC || r.MentionACI == "") {
					t.Fatalf("%s: bad mention %+v in %q", m.ID, r, m.Body)
				}
			}
		}
	}
}

func TestParseMarkup(t *testing.T) {
	tests := []struct {
		in       string
		body     string
		ranges   []core.BodyRange
		mentions bool
	}{
		{"plain", "plain", nil, false},
		{"**b** x", "b x", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleBold}}, false},
		{"🏔️ __it__", "🏔️ it", []core.BodyRange{{Start: 4, Length: 2, Style: core.StyleItalic}}, false},
		{"a ~~s~~ `m` ||x||", "a s m x", []core.BodyRange{
			{Start: 2, Length: 1, Style: core.StyleStrikethrough},
			{Start: 4, Length: 1, Style: core.StyleMonospace},
			{Start: 6, Length: 1, Style: core.StyleSpoiler},
		}, false},
		{"hi @{" + string(SelfACI) + "}!", "hi ￼!", []core.BodyRange{{Start: 3, Length: 1, Style: core.StyleMention, MentionACI: SelfACI}}, true},
		{"@{" + string(BobACI) + "}", "￼", []core.BodyRange{{Start: 0, Length: 1, Style: core.StyleMention, MentionACI: BobACI}}, false},
		{"**unclosed", "unclosed", nil, false},
	}
	for _, tt := range tests {
		body, ranges, ms := parseMarkup(tt.in, SelfACI)
		if body != tt.body || !reflect.DeepEqual(ranges, tt.ranges) || ms != tt.mentions {
			t.Errorf("parseMarkup(%q) = %q, %+v, %v; want %q, %+v, %v", tt.in, body, ranges, ms, tt.body, tt.ranges, tt.mentions)
		}
	}
}

func TestMediaBytes(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	chats, _ := s.Chats(ctx, core.ChatFilter{IncludeBlocked: true})
	archived, _ := s.Chats(ctx, core.ChatFilter{Archived: true})
	n := 0
	for _, c := range append(chats, archived...) {
		if c.AvatarID != "" {
			if data, err := s.Avatar(ctx, c.AvatarID); err != nil || !isPNG(data) {
				t.Errorf("avatar %s: err=%v png=%v", c.AvatarID, err, isPNG(data))
			}
		}
		for _, m := range allMessages(t, s, c.ID) {
			var atts []core.Attachment
			atts = append(atts, m.Attachments...)
			if m.Sticker != nil {
				atts = append(atts, m.Sticker.Attachment)
			}
			if m.Preview != nil && m.Preview.Image != nil {
				atts = append(atts, *m.Preview.Image)
			}
			for _, a := range atts {
				data, err := s.Attachment(ctx, m.ID, a.ID)
				if err != nil {
					t.Fatalf("Attachment(%s, %s): %v", m.ID, a.ID, err)
				}
				n++
				if a.Size != 0 && int64(len(data)) != a.Size {
					t.Errorf("%s: size %d, metadata says %d", a.ID, len(data), a.Size)
				}
				switch {
				case a.GIF:
					g, err := gif.DecodeAll(bytes.NewReader(data))
					if err != nil || len(g.Image) < 2 {
						t.Errorf("%s: not an animated gif: %v", a.ID, err)
					}
				case a.IsImage():
					cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
					if err != nil || cfg.Width != a.Width || cfg.Height != a.Height {
						t.Errorf("%s: decode %v, %dx%d vs %dx%d", a.ID, err, cfg.Width, cfg.Height, a.Width, a.Height)
					}
					if a.BlurHash == "" && !a.Borderless {
						t.Errorf("%s: missing blurhash", a.ID)
					}
				case a.VoiceNote:
					if !bytes.HasPrefix(data, []byte("OggS")) {
						t.Errorf("%s: voice note is not ogg", a.ID)
					}
				case a.ContentType == "application/pdf":
					if !bytes.HasPrefix(data, []byte("%PDF-1.4")) || !bytes.Contains(data, []byte("%%EOF")) {
						t.Errorf("%s: not a pdf", a.ID)
					}
				}
			}
		}
	}
	if n < 20 {
		t.Errorf("only %d attachments in demo data", n)
	}
	if _, err := s.Attachment(ctx, core.MessageID{Chat: ChatAlice, Author: AliceACI, Timestamp: 1}, "x"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown attachment: %v", err)
	}
	packs, _ := s.StickerPacks(ctx)
	for _, p := range packs {
		for _, st := range p.Stickers {
			if data, err := s.StickerImage(ctx, p.ID, st.StickerID); err != nil || !isPNG(data) {
				t.Errorf("sticker %s/%d: %v", p.ID, st.StickerID, err)
			}
		}
	}
	if _, err := s.StickerImage(ctx, "nope", 0); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown sticker: %v", err)
	}
	if _, err := s.Avatar(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("unknown avatar: %v", err)
	}
	stories, _ := s.Stories(ctx)
	for _, st := range stories {
		if st.Media != nil {
			if _, err := s.Attachment(ctx, st.ID, st.Media.ID); err != nil {
				t.Errorf("story media: %v", err)
			}
		}
	}
}

func isPNG(b []byte) bool { return bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) }

func TestReturnedValuesAreCopies(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx := context.Background()
	ms := allMessages(t, s, ChatHike)
	for i := range ms {
		ms[i].Body = "mutated"
		for j := range ms[i].Reactions {
			ms[i].Reactions[j].Emoji = "X"
		}
		if ms[i].Poll != nil {
			ms[i].Poll.Options[0].Voters = nil
		}
		for k := range ms[i].PerRecipient {
			ms[i].PerRecipient[k] = core.StatusFailed
		}
	}
	for _, m := range allMessages(t, s, ChatHike) {
		if m.Body == "mutated" {
			t.Fatal("mutating a returned message changed internal state")
		}
		for _, r := range m.Reactions {
			if r.Emoji == "X" {
				t.Fatal("reactions alias internal state")
			}
		}
		if m.Poll != nil && len(m.Poll.Options[0].Voters) == 0 && m.Poll.Question == "Which Saturday works?" {
			t.Fatal("poll voters alias internal state")
		}
		for _, st := range m.PerRecipient {
			if st == core.StatusFailed {
				t.Fatal("PerRecipient aliases internal state")
			}
		}
	}
	c, _ := s.Chat(ctx, ChatHike)
	c.Members[0].Name = "mutated"
	c2, _ := s.Chat(ctx, ChatHike)
	if c2.Members[0].Name == "mutated" {
		t.Fatal("chat members alias internal state")
	}
	data, _ := s.Avatar(ctx, c.AvatarID)
	data[0] = 0
	again, _ := s.Avatar(ctx, c.AvatarID)
	if !isPNG(again) {
		t.Fatal("avatar bytes alias internal state")
	}
}

func TestLifecycle(t *testing.T) {
	base := runtime.NumGoroutine()
	s := New(Options{Seed: 1, Clock: func() time.Time { return testNow }, Live: true,
		LiveMinInterval: time.Millisecond, LiveMaxInterval: 2 * time.Millisecond, Latency: -1, TypingDelay: -1, CallRing: -1})
	if got := s.ConnState(); got != core.ConnDisconnected {
		t.Errorf("before Start: %v", got)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	waitEvent(t, s, time.Second, func(ev core.Event) bool {
		c, ok := ev.(core.ConnChanged)
		return ok && c.State == core.ConnConnected
	})
	if got := s.ConnState(); got != core.ConnConnected {
		t.Errorf("after Start: %v", got)
	}
	if s.Self().ACI != SelfACI || !s.Self().IsSelf {
		t.Errorf("Self: %+v", s.Self())
	}
	// Generate some activity, including a send whose progression is running.
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	for range s.Events() { // must terminate: channel is closed after Close
	}
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "late"}); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close: %v", err)
	}
	if err := s.Start(ctx); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close: %v", err)
	}
	assertGoroutines(t, base)
}

// assertGoroutines waits for the goroutine count to fall back to base.
func assertGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.NumGoroutine()
		if n <= base {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			buf = buf[:runtime.Stack(buf, true)]
			t.Fatalf("goroutine leak: %d running, baseline %d\n%s", n, base, firstLines(string(buf), 60))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func TestEventQueueNeverDrops(t *testing.T) {
	done := make(chan struct{})
	q := newEventQueue(done)
	const n = 5000
	for i := 0; i < n; i++ {
		q.push(core.SyncProgress{Done: i})
	}
	for i := 0; i < n; i++ {
		ev := <-q.out
		if got := ev.(core.SyncProgress).Done; got != i {
			t.Fatalf("event %d out of order: %d", i, got)
		}
	}
	close(done)
	q.wait()
	if _, ok := <-q.out; ok {
		t.Fatal("out not closed")
	}
	q.push(core.StoriesChanged{}) // must not panic or block after stop
}

func TestContextCancelled(t *testing.T) {
	s, _ := newTestService(t, Options{Seed: 1})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Chats(ctx, core.ChatFilter{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Chats: %v", err)
	}
	if _, err := s.Send(ctx, ChatAlice, core.Outgoing{Body: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Send: %v", err)
	}
	if err := s.MarkRead(ctx, ChatAlice, core.MessageID{}); !errors.Is(err, context.Canceled) {
		t.Errorf("MarkRead: %v", err)
	}
}
