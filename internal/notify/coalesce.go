package notify

import (
	"sort"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
)

// DefaultCoalesceWindow is the burst window used by NewCoalescer.
const DefaultCoalesceWindow = 1500 * time.Millisecond

// Incoming describes one received message that may warrant a notification.
type Incoming struct {
	ChatID    string
	Sender    string
	ChatTitle string
	Body      string
	IsGroup   bool
	Silent    bool
	Urgency   Urgency
}

// burst tracks recent notifications for one chat.
type burst struct {
	deadline  time.Time
	total     int // messages since the burst began
	unflushed int // messages not yet reflected in a notification
	last      Incoming
	silent    bool // every unflushed message is silent
	urgency   Urgency
}

// Coalescer rate-limits notifications per chat and suppresses them while the
// user is looking at the chat.
//
// The first message of a burst is notified immediately. Further messages
// for the same chat within Window are held back; when the window ends
// (see Flush and Deadline) a single summary such as "Alice: 3 new messages"
// is produced, carrying the same ChatID so that capable notifiers replace
// the earlier notification. A burst ends once a whole window passes without
// new messages.
//
// Notifications are suppressed when the terminal has focus and the chat is
// the active one. Terminal focus is fed from tea.FocusMsg / tea.BlurMsg via
// SetFocused; it starts out true, so terminals that never report focus
// behave as if the user were always looking.
//
// A Coalescer is safe for concurrent use.
type Coalescer struct {
	// Window is the burst length. Zero means DefaultCoalesceWindow.
	Window time.Duration
	// Level is the privacy level used to compose notifications.
	Level core.NotificationLevel
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu       sync.Mutex
	unfocus  bool // terminal reported blur
	active   string
	bursts   map[string]*burst
	disabled bool
}

// NewCoalescer returns a Coalescer with the default window.
func NewCoalescer(level core.NotificationLevel) *Coalescer {
	return &Coalescer{Window: DefaultCoalesceWindow, Level: level}
}

// FlushMsg is delivered by the command returned from Schedule; the TUI
// should call Flush when it receives it.
type FlushMsg struct{}

func (c *Coalescer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Coalescer) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return DefaultCoalesceWindow
}

// SetLevel changes the privacy level for subsequent notifications.
func (c *Coalescer) SetLevel(level core.NotificationLevel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Level = level
}

// SetEnabled enables or disables all notifications. Disabling drops any
// pending bursts.
func (c *Coalescer) SetEnabled(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = !on
	if !on {
		c.bursts = nil
	}
}

// SetFocused records whether the terminal has focus. Gaining focus drops a
// pending burst for the active chat.
func (c *Coalescer) SetFocused(focused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unfocus = !focused
	if focused {
		delete(c.bursts, c.active)
	}
}

// Focused reports the last terminal focus state.
func (c *Coalescer) Focused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.unfocus
}

// SetActiveChat records the chat the user currently has open ("" for
// none). If the terminal has focus, a pending burst for it is dropped.
func (c *Coalescer) SetActiveChat(chatID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = chatID
	if !c.unfocus {
		delete(c.bursts, chatID)
	}
}

// Clear forgets the burst for chatID, e.g. after the chat was read on
// another device.
func (c *Coalescer) Clear(chatID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.bursts, chatID)
}

// Suppressed reports whether a message for chatID would currently be
// suppressed because the user is looking at that chat.
func (c *Coalescer) Suppressed(chatID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.suppressed(chatID)
}

func (c *Coalescer) suppressed(chatID string) bool {
	return c.disabled || (!c.unfocus && chatID != "" && chatID == c.active)
}

// Add registers an incoming message. It returns the notification to show
// now, or false if the message is suppressed or held back for a summary.
func (c *Coalescer) Add(in Incoming) (Notification, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.suppressed(in.ChatID) {
		delete(c.bursts, in.ChatID)
		return Notification{}, false
	}
	now := c.now()
	if c.bursts == nil {
		c.bursts = make(map[string]*burst)
	}
	b := c.bursts[in.ChatID]
	if b != nil && now.Before(b.deadline) {
		b.total++
		if b.unflushed == 0 {
			b.silent = in.Silent
			b.urgency = in.Urgency
		} else {
			b.silent = b.silent && in.Silent
			b.urgency = maxUrgency(b.urgency, in.Urgency)
		}
		b.unflushed++
		b.last = in
		return Notification{}, false
	}
	if b != nil && b.unflushed > 0 {
		// The window expired before Flush ran: summarize including this one.
		b.total++
		b.unflushed++
		b.silent = b.silent && in.Silent
		b.urgency = maxUrgency(b.urgency, in.Urgency)
		b.last = in
		n := c.summary(b)
		b.unflushed = 0
		b.deadline = now.Add(c.window())
		return n, true
	}
	c.bursts[in.ChatID] = &burst{deadline: now.Add(c.window()), total: 1, last: in}
	n := Compose(c.Level, in.Sender, in.ChatTitle, in.Body, in.IsGroup)
	n.ChatID = in.ChatID
	n.Silent = in.Silent
	n.Urgency = in.Urgency
	return n, true
}

// Flush returns the summaries for every burst whose window has ended and
// forgets bursts that went quiet. Results are ordered by deadline, then
// chat ID.
func (c *Coalescer) Flush() []Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	type due struct {
		id string
		b  *burst
	}
	var ready []due
	for id, b := range c.bursts {
		if now.Before(b.deadline) {
			continue
		}
		if b.unflushed == 0 {
			delete(c.bursts, id)
			continue
		}
		ready = append(ready, due{id, b})
	}
	sort.Slice(ready, func(i, j int) bool {
		if !ready[i].b.deadline.Equal(ready[j].b.deadline) {
			return ready[i].b.deadline.Before(ready[j].b.deadline)
		}
		return ready[i].id < ready[j].id
	})
	out := make([]Notification, 0, len(ready))
	for _, d := range ready {
		out = append(out, c.summary(d.b))
		d.b.unflushed = 0
		d.b.deadline = now.Add(c.window())
	}
	return out
}

func (c *Coalescer) summary(b *burst) Notification {
	n := ComposeBatch(c.Level, b.last.Sender, b.last.ChatTitle, b.last.IsGroup, b.total)
	n.ChatID = b.last.ChatID
	n.Silent = b.silent
	n.Urgency = b.urgency
	return n
}

// Deadline returns the earliest time Flush has work to do, and false if no
// burst is pending.
func (c *Coalescer) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best time.Time
	found := false
	for _, b := range c.bursts {
		if !found || b.deadline.Before(best) {
			best, found = b.deadline, true
		}
	}
	return best, found
}

// Pending reports the number of chats with an active burst.
func (c *Coalescer) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bursts)
}

// Schedule returns a command that delivers FlushMsg at the next deadline,
// or nil if nothing is pending.
func (c *Coalescer) Schedule() tea.Cmd {
	d, ok := c.Deadline()
	if !ok {
		return nil
	}
	wait := d.Sub(c.now())
	if wait < 0 {
		wait = 0
	}
	return tea.Tick(wait, func(time.Time) tea.Msg { return FlushMsg{} })
}

func maxUrgency(a, b Urgency) Urgency {
	rank := func(u Urgency) int {
		switch u {
		case UrgencyLow:
			return 0
		case UrgencyCritical:
			return 2
		default:
			return 1
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}
