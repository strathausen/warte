// Package fake provides an in-memory implementation of core.Service and
// core.Linker, populated with rich, deterministic demo data.
//
// It backs "warte --demo" and the TUI tests: every feature the frontend can
// render (replies, reactions, edits, mentions, formatting, media, stickers,
// polls, stories, calls, disappearing messages, groups, folders, …) is present
// in the generated data, and every action mutates the in-memory state and
// emits the same events a real backend would.
//
// All generated content is a pure function of Options.Seed and Options.Clock,
// so two services created with the same options hold identical data.
package fake

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"warte/internal/core"
)

// Default timings used when the corresponding Options field is zero.
const (
	DefaultLatency         = 400 * time.Millisecond
	DefaultLiveMinInterval = 8 * time.Second
	DefaultLiveMaxInterval = 30 * time.Second
	DefaultTypingDelay     = 2500 * time.Millisecond
	DefaultCallRing        = 12 * time.Second
	defaultMessagesLimit   = 50
	defaultSearchLimit     = 100
	janitorInterval        = time.Second
)

// ErrClosed is returned by actions invoked after Close.
var ErrClosed = errors.New("fake: service closed")

// Options configures a fake Service.
type Options struct {
	// Seed makes all generated data and live activity deterministic.
	Seed int64
	// Clock returns the current time. Nil uses time.Now. Demo data is laid out
	// relative to the clock's value at New.
	Clock func() time.Time
	// Live enables simulated incoming activity (typing, messages, reactions,
	// calls) and witty auto-replies in 1:1 chats once Start is called.
	Live bool
	// Latency is the simulated network delay between delivery-status steps
	// (Pending→Sent→Delivered→Read) and for attachment downloads. Zero uses
	// DefaultLatency; a negative value makes every step immediate.
	Latency time.Duration

	// LiveMinInterval and LiveMaxInterval bound the random pause between live
	// activities. Zero uses DefaultLiveMinInterval/DefaultLiveMaxInterval.
	LiveMinInterval time.Duration
	LiveMaxInterval time.Duration
	// TypingDelay is how long a simulated contact "types" before a live
	// message or auto-reply arrives. Zero uses DefaultTypingDelay; negative
	// means no delay.
	TypingDelay time.Duration
	// CallRing is how long a simulated incoming call rings before it is
	// recorded as missed. Zero uses DefaultCallRing; negative means no delay.
	CallRing time.Duration
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = time.Now
	}
	o.Latency = orDefault(o.Latency, DefaultLatency)
	o.TypingDelay = orDefault(o.TypingDelay, DefaultTypingDelay)
	o.CallRing = orDefault(o.CallRing, DefaultCallRing)
	if o.LiveMinInterval <= 0 {
		o.LiveMinInterval = DefaultLiveMinInterval
	}
	if o.LiveMaxInterval <= 0 {
		o.LiveMaxInterval = DefaultLiveMaxInterval
	}
	if o.LiveMaxInterval < o.LiveMinInterval {
		o.LiveMaxInterval = o.LiveMinInterval
	}
	return o
}

// orDefault maps zero to def and negative values to zero (immediate).
func orDefault(d, def time.Duration) time.Duration {
	switch {
	case d == 0:
		return def
	case d < 0:
		return 0
	}
	return d
}

// chatState is the internal record for one conversation. The derived fields of
// chat (LastMessage, Unread, MentionsUnread, Members' names, Contact) are
// computed when the chat is exported.
type chatState struct {
	chat     core.Chat
	msgs     []*core.Message // oldest → newest by ID.Timestamp
	readUpTo uint64          // incoming messages with Timestamp <= readUpTo are read
	created  time.Time
	left     bool // self left the group
	reported bool
	pinUntil map[core.MessageID]time.Time
}

type folderState struct {
	folder  core.Folder
	members map[core.ChatID]bool // nil: membership by filter only
}

// Service is an in-memory core.Service. Create it with New.
type Service struct {
	opts  Options
	clock func() time.Time

	mu         sync.RWMutex
	rng        *rand.Rand
	self       core.Contact
	selfGiven  string
	selfFamily string
	contacts   map[core.ACI]*core.Contact
	chats      map[core.ChatID]*chatState
	stories    []*core.Story
	packs      []*core.StickerPack
	folders    []*folderState
	settings   core.Settings
	blobs      map[string][]byte // attachment ID → bytes
	avatars    map[string][]byte // avatar ID → bytes
	stickers   map[stickerKey][]byte
	conn       core.ConnState
	lastTS     uint64
	started    bool
	loggedOut  bool
	replyIndex int
	edits      map[core.MessageID]time.Time

	events *eventQueue

	life      sync.Mutex
	closed    bool
	done      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

type stickerKey struct {
	pack string
	id   uint32
}

var (
	_ core.Service = (*Service)(nil)
	_ core.Linker  = (*Linker)(nil)
)

// New builds a Service populated with demo data. It starts the event pump but
// no simulated activity; call Start for that.
func New(opts Options) *Service {
	opts = opts.withDefaults()
	s := &Service{
		opts:     opts,
		clock:    opts.Clock,
		rng:      rand.New(rand.NewPCG(uint64(opts.Seed), uint64(opts.Seed)^0x9e3779b97f4a7c15)),
		contacts: map[core.ACI]*core.Contact{},
		chats:    map[core.ChatID]*chatState{},
		blobs:    map[string][]byte{},
		avatars:  map[string][]byte{},
		stickers: map[stickerKey][]byte{},
		settings: core.Settings{
			ReadReceipts:        true,
			TypingIndicators:    true,
			LinkPreviews:        true,
			SealedSenderNotice:  false,
			NotificationLevel:   core.NotifyNameAndMessage,
			PhoneNumberSharing:  false,
			DiscoverableByPhone: true,
		},
		done: make(chan struct{}),
	}
	s.events = newEventQueue(s.done)
	prewarmDemoMedia()
	s.seed()
	return s
}

// Start connects the fake service: it emits connection and sync events and,
// if Options.Live is set, starts simulated activity. Calling it again is a
// no-op.
func (s *Service) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.isClosed() {
		return ErrClosed
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.loggedOut {
		s.mu.Unlock()
		return core.ErrNotLoggedIn
	}
	s.started = true
	s.conn = core.ConnConnecting
	nChats, nContacts := len(s.chats), len(s.contacts)
	s.mu.Unlock()

	s.emit(core.ConnChanged{State: core.ConnConnecting})
	s.emit(core.SyncProgress{Stage: "contacts", Done: nContacts, Total: nContacts})
	s.emit(core.SyncProgress{Stage: "groups", Done: nChats, Total: nChats})

	s.mu.Lock()
	s.conn = core.ConnConnected
	s.mu.Unlock()
	s.emit(core.ConnChanged{State: core.ConnConnected})

	s.spawn(s.janitor)
	if s.opts.Live {
		s.spawn(s.live)
	}
	return nil
}

// Close stops all background goroutines and the event pump, then closes the
// Events channel. It is safe to call more than once.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.life.Lock()
		s.closed = true
		close(s.done)
		s.life.Unlock()
		s.wg.Wait()
		s.events.wait()
		s.mu.Lock()
		s.conn = core.ConnDisconnected
		s.mu.Unlock()
	})
	return nil
}

// Events returns the event stream. It is closed after Close.
func (s *Service) Events() <-chan core.Event { return s.events.out }

// Self returns the account's own contact.
func (s *Service) Self() core.Contact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.self
}

// ConnState reports the simulated connection state.
func (s *Service) ConnState() core.ConnState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn
}

func (s *Service) isClosed() bool {
	s.life.Lock()
	defer s.life.Unlock()
	return s.closed
}

// spawn runs fn in a goroutine tracked by Close. It is a no-op after Close.
func (s *Service) spawn(fn func()) {
	s.life.Lock()
	defer s.life.Unlock()
	if s.closed {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn()
	}()
}

// sleep waits for d and reports false if the service was closed meanwhile.
func (s *Service) sleep(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-s.done:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-s.done:
		return false
	}
}

func (s *Service) emit(ev core.Event) { s.events.push(ev) }

func (s *Service) emitAll(evs []core.Event) {
	for _, ev := range evs {
		s.events.push(ev)
	}
}

// now returns the clock's time.
func (s *Service) now() time.Time { return s.clock() }

// nextTSLocked returns a unique, monotonically increasing millisecond
// timestamp not earlier than t.
func (s *Service) nextTSLocked(t time.Time) uint64 {
	ts := uint64(t.UnixMilli())
	if ts <= s.lastTS {
		ts = s.lastTS + 1
	}
	s.lastTS = ts
	return ts
}

// janitor removes expired disappearing messages and lapsed message pins.
func (s *Service) janitor() {
	for s.sleep(janitorInterval) {
		s.reap()
	}
}

// reap deletes expired messages and unpins messages whose pin duration ran
// out, emitting the resulting events. It returns them for tests.
func (s *Service) reap() []core.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	evs := s.reapLocked()
	s.emitAll(evs)
	return evs
}

func (s *Service) reapLocked() []core.Event {
	now := s.now()
	var evs []core.Event
	for _, cs := range s.chats {
		changed := false
		kept := cs.msgs[:0]
		for _, m := range cs.msgs {
			if exp := m.ExpiresAt(); !exp.IsZero() && !now.Before(exp) {
				delete(cs.pinUntil, m.ID)
				s.dropBlobsLocked(m)
				evs = append(evs, core.MessageRemoved{ID: m.ID})
				changed = true
				continue
			}
			kept = append(kept, m)
		}
		for i := len(kept); i < len(cs.msgs); i++ {
			cs.msgs[i] = nil
		}
		cs.msgs = kept
		for id, until := range cs.pinUntil {
			if now.Before(until) {
				continue
			}
			delete(cs.pinUntil, id)
			if m := cs.find(id); m != nil && m.Pinned {
				m.Pinned = false
				evs = append(evs, core.MessageUpdated{Message: s.exportMsgLocked(m)})
			}
		}
		if changed {
			evs = append(evs, core.ChatUpdated{Chat: s.exportChatLocked(cs)})
		}
	}
	return evs
}

func (s *Service) dropBlobsLocked(m *core.Message) {
	for _, a := range m.Attachments {
		if !s.attachmentSharedLocked(a.ID, m.ID) {
			delete(s.blobs, a.ID)
		}
	}
}

// attachmentSharedLocked reports whether any message other than except still
// references the attachment (forwards reuse attachment IDs).
func (s *Service) attachmentSharedLocked(id string, except core.MessageID) bool {
	for _, cs := range s.chats {
		for _, m := range cs.msgs {
			if m.ID == except {
				continue
			}
			for _, a := range m.Attachments {
				if a.ID == id {
					return true
				}
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Event queue

// eventQueue is an unbounded FIFO feeding a buffered channel, so producers
// never block and never drop events regardless of how slowly the consumer
// reads.
type eventQueue struct {
	mu      sync.Mutex
	q       []core.Event
	notify  chan struct{}
	out     chan core.Event
	done    <-chan struct{}
	stopped chan struct{}
}

func newEventQueue(done <-chan struct{}) *eventQueue {
	e := &eventQueue{
		notify:  make(chan struct{}, 1),
		out:     make(chan core.Event, 64),
		done:    done,
		stopped: make(chan struct{}),
	}
	go e.run()
	return e
}

func (e *eventQueue) push(ev core.Event) {
	select {
	case <-e.done:
		return
	default:
	}
	e.mu.Lock()
	e.q = append(e.q, ev)
	e.mu.Unlock()
	select {
	case e.notify <- struct{}{}:
	default:
	}
}

func (e *eventQueue) run() {
	defer close(e.stopped)
	defer close(e.out)
	for {
		e.mu.Lock()
		if len(e.q) == 0 {
			e.mu.Unlock()
			select {
			case <-e.notify:
				continue
			case <-e.done:
				return
			}
		}
		ev := e.q[0]
		e.q[0] = nil
		e.q = e.q[1:]
		e.mu.Unlock()
		select {
		case e.out <- ev:
		case <-e.done:
			return
		}
	}
}

func (e *eventQueue) wait() { <-e.stopped }
