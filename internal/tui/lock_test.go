package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/notify"
	"warte/internal/termimg"
	"warte/internal/tui/ui"
)

func init() {
	// Cheap argon2id for tests (the harness drops commands slower than
	// 150ms).
	lockArgon.Time, lockArgon.Memory, lockArgon.Threads = 1, 64, 1
}

type fixedVerifier string

func (v fixedVerifier) Verify(pw string) bool { return pw == string(v) }

func (r *recNotifier) all() []notify.Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Notification(nil), r.got...)
}

// withClock makes the App's clock read *now.
func withClock(now *time.Time) harnessOpt {
	return func(_ *fake.Options, _ *config.Config, o *Options) {
		o.Now = func() time.Time { return *now }
	}
}

func withOptions(f func(*Options)) harnessOpt {
	return func(_ *fake.Options, _ *config.Config, o *Options) { f(o) }
}

// chatTitles returns the titles of the listed chats (to check that nothing
// of them is drawn while locked).
func chatTitles(h *harness) []string {
	var out []string
	for _, c := range h.a.list.chats {
		if t := strings.TrimSpace(c.Title); len(t) >= 4 {
			out = append(out, t)
		}
	}
	return out
}

func assertNoContent(t *testing.T, h *harness, titles []string) {
	t.Helper()
	out := h.plain()
	for _, title := range titles {
		if strings.Contains(out, title) {
			t.Fatalf("locked screen shows %q:\n%s", title, out)
		}
	}
	if got := h.a.View().WindowTitle; got != config.AppName {
		t.Fatalf("window title %q while locked", got)
	}
}

func TestLockSetPasswordThenUnlockWithBackoff(t *testing.T) {
	now := testNow
	h := newHarness(t, 120, 40, withClock(&now))
	titles := chatTitles(h)
	if len(titles) == 0 {
		t.Fatal("no chats")
	}
	h.keys(":lock<enter>")
	if h.a.lock.stage != lockSetNew {
		t.Fatalf("stage %d, want set-new", h.a.lock.stage)
	}
	if out := h.plain(); !strings.Contains(out, "Set a lock password") {
		t.Fatalf("no set prompt:\n%s", out)
	}
	assertNoContent(t, h, titles)

	// Too short, then a mismatch: back to the first step.
	h.keys("ab<enter>")
	if !strings.Contains(h.plain(), "at least 4") {
		t.Fatalf("no length hint:\n%s", h.plain())
	}
	h.keys("<bs><bs>abcd<enter>abce<enter>")
	if h.a.lock.stage != lockSetNew || !strings.Contains(h.plain(), "did not match") {
		t.Fatalf("mismatch: stage %d\n%s", h.a.lock.stage, h.plain())
	}
	h.keys("abcd<enter>abcd<enter>")
	if !h.a.isLocked() {
		t.Fatalf("not locked after setting the password (stage %d, msg %q)", h.a.lock.stage, h.a.lock.msg)
	}
	fi, err := os.Stat(h.a.opts.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %v", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(h.a.opts.LockPath)
	if !strings.HasPrefix(string(b), "argon2id$v=19$") || strings.Contains(string(b), "abcd") {
		t.Errorf("lock file %q", b)
	}
	out := h.plain()
	if !strings.Contains(out, "Locked — enter passphrase") || !strings.Contains(out, "warte") {
		t.Fatalf("lock screen:\n%s", out)
	}
	if strings.Contains(out, "abcd") {
		t.Fatal("passphrase echoed")
	}
	assertNoContent(t, h, titles)

	// Keys do nothing else while locked.
	sel := h.a.list.sel
	h.keys("j")
	h.keys("<bs>")
	if h.a.list.sel != sel || h.a.keys.Mode().String() == "command" {
		t.Fatal("keys reached the UI while locked")
	}

	// Wrong passphrase: backoff.
	h.keys("nope<enter>")
	if !h.a.isLocked() || !strings.Contains(h.plain(), "Wrong passphrase") {
		t.Fatalf("after wrong passphrase:\n%s", h.plain())
	}
	h.keys("abcd<enter>")
	if !h.a.isLocked() || !strings.Contains(h.plain(), "Try again in") {
		t.Fatalf("backoff not enforced:\n%s", h.plain())
	}
	// A wrong attempt during the wait is not even checked.
	h.keys("nope<enter>")
	if h.a.lock.failures != 1 {
		t.Fatalf("failures %d during backoff", h.a.lock.failures)
	}
	now = now.Add(2 * time.Second)
	h.keys("nope<enter>")
	if h.a.lock.failures != 2 {
		t.Fatalf("failures %d, want 2", h.a.lock.failures)
	}
	if w := h.a.lock.until.Sub(now); w != 2*time.Second {
		t.Fatalf("second backoff %v, want 2s", w)
	}
	now = now.Add(3 * time.Second)
	h.keys("abcd<enter>")
	if h.a.locked() {
		t.Fatalf("still locked:\n%s", h.plain())
	}
	out = h.plain()
	found := false
	for _, title := range titles {
		if strings.Contains(out, title) {
			found = true
		}
	}
	if !found {
		t.Fatalf("UI not restored:\n%s", out)
	}

	// The next :lock asks for the stored password right away.
	h.keys(":lock<enter>")
	if !h.a.isLocked() {
		t.Fatalf("stage %d after second :lock", h.a.lock.stage)
	}
}

func TestLockSetCanBeCanceled(t *testing.T) {
	h := newHarness(t, 100, 30)
	h.keys(":lock<enter>abcd<esc>")
	if h.a.locked() {
		t.Fatal("esc did not cancel setting a password")
	}
	if _, err := os.Stat(h.a.opts.LockPath); err == nil {
		t.Fatal("lock file written")
	}
}

func TestLockVerifierLeaderAndNotifications(t *testing.T) {
	rec := &recNotifier{}
	h := newHarness(t, 120, 40, withOptions(func(o *Options) {
		o.LockVerifier = fixedVerifier("hunter22")
		o.Notifier = rec
	}), withConfig(func(c *config.Config) {
		c.Notifications = config.NotifyNameAndMessage
		c.NotifyMethod = "auto"
	}))
	titles := chatTitles(h)
	h.keys("<space>l")
	if !h.a.isLocked() {
		t.Fatalf("<leader>l did not lock (stage %d)", h.a.lock.stage)
	}
	assertNoContent(t, h, titles)

	// A message arrives in another chat: the notification says nothing.
	var other core.Chat
	for _, c := range h.a.list.chats {
		if !h.a.conv.open || c.ID != h.a.conv.chat.ID {
			other = c
			break
		}
	}
	h.send(eventMsg{ev: core.MessageAdded{Notify: true, Message: core.Message{
		ID:         core.MessageID{Chat: other.ID, Author: "x", Timestamp: uint64(testNow.UnixMilli()) + 5},
		Kind:       core.KindNormal,
		SenderName: "Mallory Secretname",
		Body:       "the eagle lands at noon",
		SentAt:     testNow,
	}}})
	got := rec.all()
	if len(got) == 0 {
		t.Fatal("no notification while locked")
	}
	for _, n := range got {
		if n.Title != config.AppName || n.Body != "New message" {
			t.Fatalf("notification while locked: %+v", n)
		}
	}
	assertNoContent(t, h, append(titles, "eagle", "Mallory"))

	h.keys("hunter22<enter>")
	if h.a.locked() {
		t.Fatal("verifier did not unlock")
	}
}

func TestLockIdle(t *testing.T) {
	now := testNow
	h := newHarness(t, 100, 30, withClock(&now), withConfig(func(c *config.Config) {
		c.ScreenLock = config.ScreenLock{Enabled: true, IdleMinutes: 2}
	}), withOptions(func(o *Options) { o.LockVerifier = fixedVerifier("pw12") }))
	h.a.noteActivity()
	now = now.Add(90 * time.Second)
	h.send(lockIdleTickMsg{})
	if h.a.locked() {
		t.Fatal("locked before the idle timeout")
	}
	h.keys("j") // activity resets the timer
	now = now.Add(90 * time.Second)
	h.send(lockIdleTickMsg{})
	if h.a.locked() {
		t.Fatal("activity did not reset the idle timer")
	}
	now = now.Add(31 * time.Second)
	h.send(lockIdleTickMsg{})
	if !h.a.isLocked() {
		t.Fatal("not locked after the idle timeout")
	}
}

func TestLockIdleWithoutPasswordWarns(t *testing.T) {
	now := testNow
	h := newHarness(t, 100, 30, withClock(&now), withConfig(func(c *config.Config) {
		c.ScreenLock = config.ScreenLock{Enabled: true, IdleMinutes: 1}
	}))
	now = now.Add(2 * time.Minute)
	h.send(lockIdleTickMsg{})
	if h.a.locked() {
		t.Fatal("locked without any way to unlock")
	}
	if !h.a.lock.warned {
		t.Fatal("no warning")
	}
}

// rawOf collects the tea.Raw payloads a command produces.
func rawOf(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(100 * time.Millisecond):
		return ""
	}
	switch m := msg.(type) {
	case tea.BatchMsg:
		var b strings.Builder
		for _, c := range m {
			b.WriteString(rawOf(c))
		}
		return b.String()
	case tea.RawMsg:
		s, _ := m.Msg.(string)
		return s
	}
	return ""
}

func TestLockDeletesKittyImages(t *testing.T) {
	kitty := termimg.Kitty
	h := newHarness(t, 120, 40, withOptions(func(o *Options) {
		o.Images = &kitty
		o.LockVerifier = fixedVerifier("pw12")
	}))
	if h.a.images.alloc == nil {
		t.Fatal("no kitty allocator")
	}
	h.a.images.alloc.Acquire("img@4x2")
	gen := h.a.images.gen
	raw := rawOf(h.a.lockScreen())
	if !strings.Contains(raw, "\x1b_G") || !strings.Contains(raw, "a=d") {
		t.Fatalf("no kitty delete on lock: %q", raw)
	}
	if h.a.images.alloc.Len() != 0 || h.a.images.gen == gen {
		t.Fatal("allocator not reset")
	}
	// A load requested before the lock must not be transmitted.
	stale := imageLoadedMsg{key: "img", size: sizeKey{4, 2}, gen: gen, raw: "\x1b_Gsecret\x1b\\", rows: []string{"xxxx", "xxxx"}}
	_, cmd := h.a.Update(stale)
	if r := rawOf(cmd); strings.Contains(r, "secret") {
		t.Fatal("stale image transmitted while locked")
	}
	if e := h.a.images.entries["img"]; e != nil && len(e.rendered) != 0 {
		t.Fatal("stale image rows cached")
	}
	// A stale load that acquired its id after the reset frees it, and the
	// images it evicted are drawn (and transmitted) again later.
	h.a.images.entries["old"] = &imgEntry{rendered: map[sizeKey][]string{{4, 2}: {"oooo", "oooo"}}, pending: map[sizeKey]bool{}}
	h.a.images.alloc.Acquire("late@4x2")
	h.a.Update(imageLoadedMsg{key: "late", size: sizeKey{4, 2}, gen: gen, evicted: []string{"old@4x2"}})
	if _, ok := h.a.images.alloc.Lookup("late@4x2"); ok {
		t.Error("stale load kept its kitty id")
	}
	if len(h.a.images.entries["old"].rendered) != 0 {
		t.Error("rows of an evicted image still cached")
	}
}

// lockSpy records the messages an overlay receives.
type lockSpy struct{ got []tea.Msg }

func (o *lockSpy) Init() tea.Cmd                          { return nil }
func (o *lockSpy) Update(m tea.Msg) (ui.Overlay, tea.Cmd) { o.got = append(o.got, m); return o, nil }
func (o *lockSpy) View(w, h int) string                   { return "spy" }
func (o *lockSpy) Title() string                          { return "spy" }
func (o *lockSpy) KeyContext() string                     { return "spy" }
func (o *lockSpy) Size(w, h int) (int, int)               { return 10, 3 }

// Locking tells the top overlay, so players and story viewers pause.
func TestLockNotifiesOverlay(t *testing.T) {
	h := newHarness(t, 120, 40, withOptions(func(o *Options) { o.LockVerifier = fixedVerifier("pw12") }))
	spy := &lockSpy{}
	h.send(ui.PushOverlayMsg{Overlay: spy})
	h.run(h.a.lockScreen(), 0)
	for _, m := range spy.got {
		if _, ok := m.(ui.ScreenLockMsg); ok {
			return
		}
	}
	t.Fatalf("overlay got no ScreenLockMsg: %v", spy.got)
}

func TestLockBackoff(t *testing.T) {
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for n, w := range want {
		if got := lockBackoff(n); got != w {
			t.Errorf("lockBackoff(%d) = %v, want %v", n, got, w)
		}
	}
	if got := lockBackoff(40); got != lockMaxBackoff {
		t.Errorf("cap: %v", got)
	}
}

func TestLockPasswordFile(t *testing.T) {
	l := lockPassword{path: t.TempDir() + "/lock"}
	if l.IsSet() || l.Verify("") {
		t.Fatal("unset password verifies")
	}
	if err := l.Set("correct horse"); err != nil {
		t.Fatal(err)
	}
	if !l.IsSet() || !l.Verify("correct horse") || l.Verify("correct horsE") || l.Verify("") {
		t.Fatal("verify")
	}
	if err := os.WriteFile(l.path, []byte("argon2id$v=19$t=1,m=64,p=1$!!$!!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l.IsSet() || l.Verify("correct horse") {
		t.Fatal("corrupt file accepted")
	}
}
