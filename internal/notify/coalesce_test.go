package notify

import (
	"reflect"
	"testing"
	"time"

	"warte/internal/core"
)

func TestCompose(t *testing.T) {
	tests := []struct {
		name      string
		level     core.NotificationLevel
		sender    string
		chatTitle string
		body      string
		isGroup   bool
		want      Notification
	}{
		{"name+msg direct", core.NotifyNameAndMessage, "Alice", "Alice", "hi", false, Notification{Title: "Alice", Body: "hi"}},
		{"name+msg group", core.NotifyNameAndMessage, "Alice", "Family", "hi", true, Notification{Title: "Alice @ Family", Body: "hi"}},
		{"name+msg group no title", core.NotifyNameAndMessage, "Alice", "", "hi", true, Notification{Title: "Alice", Body: "hi"}},
		{"name+msg empty body", core.NotifyNameAndMessage, "Alice", "", " \n", false, Notification{Title: "Alice", Body: TextAttachment}},
		{"name+msg unknown sender", core.NotifyNameAndMessage, "\x1b", "", "hi", false, Notification{Title: "Unknown", Body: "hi"}},
		{"name only direct", core.NotifyNameOnly, "Alice", "Alice", "secret", false, Notification{Title: "New message from Alice"}},
		{"name only group", core.NotifyNameOnly, "Alice", "Family", "secret", true, Notification{Title: "New message from Alice in Family"}},
		{"none direct", core.NotifyNone, "Alice", "Alice", "secret", false, Notification{Title: "New message"}},
		{"none group", core.NotifyNone, "Alice", "Family", "secret", true, Notification{Title: "New message"}},
		{"unknown level is private", core.NotificationLevel(99), "Alice", "", "secret", false, Notification{Title: "New message"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compose(tt.level, tt.sender, tt.chatTitle, tt.body, tt.isGroup)
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestComposeBatch(t *testing.T) {
	tests := []struct {
		name    string
		level   core.NotificationLevel
		isGroup bool
		count   int
		want    Notification
	}{
		{"direct", core.NotifyNameAndMessage, false, 3, Notification{Title: "Alice", Body: "3 new messages"}},
		{"group", core.NotifyNameAndMessage, true, 2, Notification{Title: "Family", Body: "2 new messages"}},
		{"name only", core.NotifyNameOnly, false, 5, Notification{Title: "Alice", Body: "5 new messages"}},
		{"none", core.NotifyNone, true, 4, Notification{Title: "4 new messages"}},
		{"single name+msg hides body", core.NotifyNameAndMessage, false, 1, Notification{Title: "New message from Alice"}},
		{"single none", core.NotifyNone, false, 1, Notification{Title: "New message"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeBatch(tt.level, "Alice", "Family", tt.isGroup, tt.count)
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestCoalescer(level core.NotificationLevel) (*Coalescer, *fakeClock) {
	clk := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	c := NewCoalescer(level)
	c.Now = clk.Now
	return c, clk
}

func alice(body string) Incoming {
	return Incoming{ChatID: "a", Sender: "Alice", ChatTitle: "Alice", Body: body}
}

func TestCoalescerBurst(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)

	n, ok := c.Add(alice("one"))
	if !ok || n != (Notification{Title: "Alice", Body: "one", ChatID: "a"}) {
		t.Fatalf("first = %+v, %v", n, ok)
	}
	clk.Advance(500 * time.Millisecond)
	if _, ok := c.Add(alice("two")); ok {
		t.Fatal("second message in window should be held")
	}
	clk.Advance(500 * time.Millisecond)
	if _, ok := c.Add(alice("three")); ok {
		t.Fatal("third message in window should be held")
	}
	if got := c.Flush(); len(got) != 0 {
		t.Fatalf("early flush = %+v", got)
	}
	d, ok := c.Deadline()
	if want := clk.t.Add(500 * time.Millisecond); !ok || !d.Equal(want) {
		t.Fatalf("deadline = %v, %v; want %v", d, ok, want)
	}
	clk.Advance(500 * time.Millisecond)
	got := c.Flush()
	want := []Notification{{Title: "Alice", Body: "3 new messages", ChatID: "a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flush = %+v, want %+v", got, want)
	}
	// The burst continues for another window; a quiet window ends it.
	if c.Pending() != 1 {
		t.Fatalf("pending = %d", c.Pending())
	}
	clk.Advance(DefaultCoalesceWindow)
	if got := c.Flush(); len(got) != 0 {
		t.Fatalf("quiet flush = %+v", got)
	}
	if c.Pending() != 0 {
		t.Fatalf("pending after quiet window = %d", c.Pending())
	}
	if _, ok := c.Deadline(); ok {
		t.Fatal("deadline with nothing pending")
	}
	// A new burst starts with an immediate notification.
	if n, ok := c.Add(alice("again")); !ok || n.Body != "again" {
		t.Fatalf("new burst = %+v, %v", n, ok)
	}
}

func TestCoalescerCumulativeCount(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)
	c.Add(alice("1"))
	c.Add(alice("2"))
	clk.Advance(DefaultCoalesceWindow)
	if got := c.Flush(); len(got) != 1 || got[0].Body != "2 new messages" {
		t.Fatalf("first flush = %+v", got)
	}
	c.Add(alice("3"))
	c.Add(alice("4"))
	clk.Advance(DefaultCoalesceWindow)
	if got := c.Flush(); len(got) != 1 || got[0].Body != "4 new messages" {
		t.Fatalf("second flush = %+v", got)
	}
}

func TestCoalescerLateAddWithoutFlush(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)
	c.Add(alice("1"))
	c.Add(alice("2"))
	clk.Advance(2 * time.Second) // window expired, Flush never ran
	n, ok := c.Add(alice("3"))
	if !ok || n != (Notification{Title: "Alice", Body: "3 new messages", ChatID: "a"}) {
		t.Fatalf("late add = %+v, %v", n, ok)
	}
	// Expired burst with nothing unflushed: a fresh burst starts.
	clk.Advance(2 * time.Second)
	if n, ok := c.Add(alice("4")); !ok || n.Body != "4" {
		t.Fatalf("fresh = %+v, %v", n, ok)
	}
}

func TestCoalescerPerChat(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)
	bob := func(body string) Incoming {
		return Incoming{ChatID: "g", Sender: "Bob", ChatTitle: "Family", Body: body, IsGroup: true}
	}
	if _, ok := c.Add(alice("a1")); !ok {
		t.Fatal("alice first")
	}
	clk.Advance(100 * time.Millisecond)
	if n, ok := c.Add(bob("b1")); !ok || n.Title != "Bob @ Family" {
		t.Fatalf("bob first = %+v, %v", n, ok)
	}
	c.Add(alice("a2"))
	c.Add(bob("b2"))
	c.Add(bob("b3"))
	clk.Advance(DefaultCoalesceWindow)
	got := c.Flush()
	want := []Notification{
		{Title: "Alice", Body: "2 new messages", ChatID: "a"},
		{Title: "Family", Body: "3 new messages", ChatID: "g"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flush = %+v, want %+v", got, want)
	}
}

func TestCoalescerFocusSuppression(t *testing.T) {
	tests := []struct {
		name     string
		focused  bool
		active   string
		chat     string
		wantShow bool
	}{
		{"focused on chat", true, "a", "a", false},
		{"focused other chat", true, "b", "a", true},
		{"blurred on chat", false, "a", "a", true},
		{"focused no chat", true, "", "a", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newTestCoalescer(core.NotifyNameAndMessage)
			c.SetActiveChat(tt.active)
			c.SetFocused(tt.focused)
			if c.Suppressed(tt.chat) == tt.wantShow {
				t.Fatalf("Suppressed = %v", !tt.wantShow)
			}
			_, ok := c.Add(Incoming{ChatID: tt.chat, Sender: "X", Body: "y"})
			if ok != tt.wantShow {
				t.Fatalf("shown = %v, want %v", ok, tt.wantShow)
			}
		})
	}
}

func TestCoalescerFocusDropsPending(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	if !c.Focused() {
		t.Fatal("coalescer should start focused")
	}
	c.SetActiveChat("a")
	c.SetFocused(false)
	c.Add(alice("1"))
	c.Add(alice("2"))
	c.SetFocused(true) // user comes back and is looking at chat a
	if c.Pending() != 0 {
		t.Fatalf("pending = %d", c.Pending())
	}
	clk.Advance(DefaultCoalesceWindow)
	if got := c.Flush(); len(got) != 0 {
		t.Fatalf("flush = %+v", got)
	}

	// Switching to a chat while focused drops its burst too.
	c.SetActiveChat("")
	c.Add(alice("3"))
	c.Add(alice("4"))
	c.SetActiveChat("a")
	if c.Pending() != 0 {
		t.Fatalf("pending after switching = %d", c.Pending())
	}

	// Clear forgets a burst explicitly.
	c.SetActiveChat("")
	c.Add(alice("5"))
	c.Clear("a")
	if c.Pending() != 0 {
		t.Fatalf("pending after Clear = %d", c.Pending())
	}
}

func TestCoalescerPrivacyAndFlags(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNone)
	c.SetFocused(false)
	in := alice("secret")
	in.Silent = true
	in.Urgency = UrgencyLow
	n, ok := c.Add(in)
	if !ok || n != (Notification{Title: "New message", ChatID: "a", Silent: true, Urgency: UrgencyLow}) {
		t.Fatalf("first = %+v", n)
	}
	in2 := alice("secret2")
	in2.Silent = true
	c.Add(in2)
	in3 := alice("secret3")
	in3.Urgency = UrgencyCritical
	c.Add(in3)
	clk.Advance(DefaultCoalesceWindow)
	got := c.Flush()
	want := []Notification{{Title: "3 new messages", ChatID: "a", Silent: false, Urgency: UrgencyCritical}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flush = %+v, want %+v", got, want)
	}

	c.SetLevel(core.NotifyNameOnly)
	c.Clear("a")
	if n, _ := c.Add(alice("x")); n.Title != "New message from Alice" || n.Body != "" {
		t.Fatalf("name only = %+v", n)
	}
}

func TestCoalescerDisabled(t *testing.T) {
	c, _ := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)
	c.Add(alice("1"))
	c.SetEnabled(false)
	if c.Pending() != 0 {
		t.Fatal("disable should drop pending")
	}
	if _, ok := c.Add(alice("2")); ok {
		t.Fatal("disabled coalescer notified")
	}
	c.SetEnabled(true)
	if _, ok := c.Add(alice("3")); !ok {
		t.Fatal("re-enabled coalescer did not notify")
	}
}

func TestCoalescerSchedule(t *testing.T) {
	c, clk := newTestCoalescer(core.NotifyNameAndMessage)
	c.SetFocused(false)
	if c.Schedule() != nil {
		t.Fatal("schedule with nothing pending")
	}
	c.Add(alice("1"))
	clk.Advance(DefaultCoalesceWindow + time.Second) // overdue: fires immediately
	cmd := c.Schedule()
	if cmd == nil {
		t.Fatal("nil schedule")
	}
	if _, ok := cmd().(FlushMsg); !ok {
		t.Fatal("schedule did not produce FlushMsg")
	}
}

func TestCoalescerDefaults(t *testing.T) {
	c := &Coalescer{}
	if c.window() != DefaultCoalesceWindow {
		t.Fatal("default window")
	}
	if c.now().IsZero() {
		t.Fatal("default clock")
	}
	c.SetFocused(false)
	if _, ok := c.Add(alice("x")); !ok {
		t.Fatal("zero-value coalescer should work")
	}
}

func TestMaxUrgency(t *testing.T) {
	tests := []struct{ a, b, want Urgency }{
		{UrgencyLow, UrgencyNormal, UrgencyNormal},
		{UrgencyNormal, UrgencyLow, UrgencyNormal},
		{UrgencyCritical, UrgencyNormal, UrgencyCritical},
		{UrgencyLow, UrgencyCritical, UrgencyCritical},
	}
	for _, tt := range tests {
		if got := maxUrgency(tt.a, tt.b); got != tt.want {
			t.Errorf("maxUrgency(%v,%v) = %v", tt.a, tt.b, got)
		}
	}
}
