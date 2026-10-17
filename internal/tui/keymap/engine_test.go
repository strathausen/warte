package keymap

import (
	"reflect"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestEngine(t *testing.T, opts ...Option) (*Engine, *fakeClock) {
	t.Helper()
	c := &fakeClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	return New(append([]Option{WithClock(c.now)}, opts...)...), c
}

// feed feeds keys and returns every result produced, flattened.
func feed(e *Engine, ctx Context, keys ...string) []Result {
	var out []Result
	for _, k := range keys {
		out = append(out, e.Feed(k, ctx).All()...)
	}
	return out
}

// last returns the final result of feeding keys.
func last(e *Engine, ctx Context, keys ...string) Result {
	rs := feed(e, ctx, keys...)
	return rs[len(rs)-1]
}

func TestFeedSequencesAndCounts(t *testing.T) {
	tests := []struct {
		name      string
		mode      Mode
		ctx       Context
		keys      []string
		action    Action
		count     int
		hasCount  bool
		wantKeys  string
		unmatched bool
	}{
		{name: "j", ctx: ChatList, keys: []string{"j"}, action: ActCursorDown, count: 1},
		{name: "down arrow", ctx: ChatList, keys: []string{"down"}, action: ActCursorDown, count: 1},
		{name: "5j", ctx: Conversation, keys: []string{"5", "j"}, action: ActCursorDown, count: 5, hasCount: true, wantKeys: "5j"},
		{name: "10G", ctx: Conversation, keys: []string{"1", "0", "G"}, action: ActCursorBottom, count: 10, hasCount: true, wantKeys: "10G"},
		{name: "gg", ctx: Conversation, keys: []string{"g", "g"}, action: ActCursorTop, count: 1, wantKeys: "gg"},
		{name: "3gg", ctx: Conversation, keys: []string{"3", "g", "g"}, action: ActCursorTop, count: 3, hasCount: true},
		{name: "gt", ctx: ChatList, keys: []string{"g", "t"}, action: ActFolderNext, count: 1},
		{name: "gT", ctx: ChatList, keys: []string{"g", "T"}, action: ActFolderPrev, count: 1},
		{name: "dd chat list", ctx: ChatList, keys: []string{"d", "d"}, action: ActChatDelete, count: 1},
		{name: "dd conversation", ctx: Conversation, keys: []string{"d", "d"}, action: ActMessageDelete, count: 1},
		{name: "3dd", ctx: Conversation, keys: []string{"3", "d", "d"}, action: ActMessageDelete, count: 3, hasCount: true},
		{name: "dD", ctx: Conversation, keys: []string{"d", "D"}, action: ActMessageDeleteEveryone, count: 1},
		{name: "yy", ctx: Conversation, keys: []string{"y", "y"}, action: ActMessageCopy, count: 1},
		{name: "yl", ctx: Conversation, keys: []string{"y", "l"}, action: ActMessageCopyLink, count: 1},
		{name: "]u", ctx: ChatList, keys: []string{"]", "u"}, action: ActChatNextUnread, count: 1},
		{name: "[u", ctx: Conversation, keys: []string{"[", "u"}, action: ActChatPrevUnread, count: 1},
		{name: "zz", ctx: Conversation, keys: []string{"z", "z"}, action: ActCenter, count: 1},
		{name: "zm", ctx: ChatList, keys: []string{"z", "m"}, action: ActChatMute, count: 1},
		{name: "za", ctx: ChatList, keys: []string{"z", "a"}, action: ActChatArchive, count: 1},
		{name: "zp", ctx: ChatList, keys: []string{"z", "p"}, action: ActChatPin, count: 1},
		{name: "ctrl+w h", ctx: Conversation, keys: []string{"ctrl+w", "h"}, action: ActFocusLeft, count: 1, wantKeys: "ctrl+w h"},
		{name: "ctrl+w l", ctx: ChatList, keys: []string{"ctrl+w", "l"}, action: ActFocusRight, count: 1},
		{name: "leader leader", ctx: ChatList, keys: []string{"space", "space"}, action: ActFinderChats, count: 1, wantKeys: "space space"},
		{name: "leader /", ctx: ChatList, keys: []string{" ", "/"}, action: ActSearchGlobal, count: 1},
		{name: "leader s", ctx: Conversation, keys: []string{"space", "s"}, action: ActStories, count: 1},
		{name: "ZZ", ctx: ChatList, keys: []string{"Z", "Z"}, action: ActQuit, count: 1},
		{name: "ctrl+d with count", ctx: Conversation, keys: []string{"2", "ctrl+d"}, action: ActHalfPageDown, count: 2, hasCount: true, wantKeys: "2 ctrl+d"},
		{name: "shift+g from tea", ctx: Conversation, keys: []string{"shift+g"}, action: ActCursorBottom, count: 1},
		{name: "0 as key in image viewer", ctx: Overlay(OverlayImage), keys: []string{"0"}, action: ActZoomReset, count: 1},
		{name: "0 continues count", ctx: Overlay(OverlayImage), keys: []string{"2", "0", "j"}, action: ActCursorDown, count: 20, hasCount: true},
		{name: "huge count capped", ctx: Conversation, keys: []string{"9", "9", "9", "9", "9", "9", "9", "9", "j"}, action: ActCursorDown, count: maxCount, hasCount: true},
		{name: "gj unbound", ctx: Conversation, keys: []string{"g", "j"}, unmatched: true, wantKeys: "gj"},
		{name: "x unbound", ctx: ChatList, keys: []string{"x"}, unmatched: true},
		{name: "0 unbound without count", ctx: ChatList, keys: []string{"0"}, unmatched: true},
		{name: "insert: digits are text", mode: Insert, ctx: Conversation, keys: []string{"5"}, unmatched: true},
		{name: "insert: esc", mode: Insert, ctx: Conversation, keys: []string{"esc"}, action: ActModeNormal, count: 1},
		{name: "insert: enter sends", mode: Insert, ctx: Conversation, keys: []string{"enter"}, action: ActComposerSend, count: 1},
		{name: "insert: alt+enter newline", mode: Insert, ctx: Conversation, keys: []string{"alt+enter"}, action: ActComposerNewline, count: 1},
		{name: "insert: shift+enter newline", mode: Insert, ctx: Conversation, keys: []string{"shift+enter"}, action: ActComposerNewline, count: 1},
		{name: "insert: ctrl+e editor", mode: Insert, ctx: Conversation, keys: []string{"ctrl+e"}, action: ActComposerEditor, count: 1},
		{name: "insert: ctrl+c", mode: Insert, ctx: Conversation, keys: []string{"ctrl+c"}, action: ActComposerCancel, count: 1},
		{name: "insert: text", mode: Insert, ctx: Conversation, keys: []string{"j"}, unmatched: true},
		{name: "normal: ctrl+c hint", ctx: Conversation, keys: []string{"ctrl+c"}, action: ActQuitHint, count: 1},
		{name: "visual: 3j", mode: Visual, ctx: Conversation, keys: []string{"3", "j"}, action: ActCursorDown, count: 3, hasCount: true},
		{name: "visual: y", mode: Visual, ctx: Conversation, keys: []string{"y"}, action: ActVisualYank, count: 1},
		{name: "visual: V", mode: Visual, ctx: Conversation, keys: []string{"V"}, action: ActModeVisualLine, count: 1},
		{name: "visual line: d", mode: VisualLine, ctx: Conversation, keys: []string{"d"}, action: ActVisualDelete, count: 1},
		{name: "visual line: V exits", mode: VisualLine, ctx: Conversation, keys: []string{"V"}, action: ActModeNormal, count: 1},
		{name: "command: enter", mode: Command, keys: []string{"enter"}, action: ActCmdExecute, count: 1},
		{name: "command: tab", mode: Command, keys: []string{"tab"}, action: ActCmdComplete, count: 1},
		{name: "command: up", mode: Command, keys: []string{"up"}, action: ActCmdHistoryPrev, count: 1},
		{name: "command: text", mode: Command, keys: []string{"q"}, unmatched: true},
		{name: "command: digit", mode: Command, keys: []string{"1"}, unmatched: true},
		{name: "search: esc", mode: Search, keys: []string{"esc"}, action: ActSearchCancel, count: 1},
		{name: "search: down", mode: Search, keys: []string{"down"}, action: ActSearchHistoryNext, count: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			e.SetMode(tt.mode)
			r := last(e, tt.ctx, tt.keys...)
			if r.Unmatched != tt.unmatched {
				t.Fatalf("Unmatched = %v, want %v (result %+v)", r.Unmatched, tt.unmatched, r)
			}
			if r.Action != tt.action {
				t.Errorf("Action = %q, want %q", r.Action, tt.action)
			}
			if !tt.unmatched {
				if r.Count != tt.count || r.HasCount != tt.hasCount {
					t.Errorf("Count = %d/%v, want %d/%v", r.Count, r.HasCount, tt.count, tt.hasCount)
				}
			}
			if tt.wantKeys != "" && r.Keys != tt.wantKeys {
				t.Errorf("Keys = %q, want %q", r.Keys, tt.wantKeys)
			}
			if r.Pending {
				t.Errorf("result still pending: %+v", r)
			}
			if e.Pending() {
				t.Errorf("engine still pending after %q", tt.keys)
			}
		})
	}
}

func TestUnmatchedInsertReturnsKeys(t *testing.T) {
	e, _ := newTestEngine(t)
	e.SetMode(Insert)
	r := e.Feed("é", Conversation)
	if !r.Unmatched || !reflect.DeepEqual(r.Seq, []string{"é"}) {
		t.Fatalf("got %+v, want unmatched with Seq [é]", r)
	}
	r = e.Feed("", Conversation)
	if !r.Unmatched {
		t.Fatalf("empty key: got %+v", r)
	}
}

func TestPendingShowcmd(t *testing.T) {
	e, _ := newTestEngine(t)
	steps := []struct {
		key       string
		pending   bool
		keys      string
		action    Action
		ambiguous bool
	}{
		{key: "1", pending: true, keys: "1"},
		{key: "2", pending: true, keys: "12"},
		{key: `"`, pending: true, keys: `12"`},
		{key: "a", pending: true, keys: `12"a`},
		{key: "y", pending: true, keys: `12"ay`},
		{key: "y", keys: `12"ayy`, action: ActMessageCopy},
	}
	for i, s := range steps {
		r := e.Feed(s.key, Conversation)
		if r.Pending != s.pending || r.Keys != s.keys || r.Action != s.action || r.Ambiguous != s.ambiguous {
			t.Fatalf("step %d (%q): got %+v", i, s.key, r)
		}
		if s.pending && e.PendingKeys() != s.keys {
			t.Fatalf("step %d: PendingKeys = %q, want %q", i, e.PendingKeys(), s.keys)
		}
	}
	if e.PendingKeys() != "" || e.Pending() {
		t.Fatalf("engine should be idle")
	}
}

func TestRegisters(t *testing.T) {
	tests := []struct {
		name     string
		keys     []string
		reg      rune
		hasReg   bool
		count    int
		hasCount bool
		action   Action
		unmatch  bool
	}{
		{name: `"ayy`, keys: []string{`"`, "a", "y", "y"}, reg: 'a', hasReg: true, count: 1, action: ActMessageCopy},
		{name: `"+yl`, keys: []string{`"`, "+", "y", "l"}, reg: '+', hasReg: true, count: 1, action: ActMessageCopyLink},
		{name: `"Ayy append`, keys: []string{`"`, "A", "y", "y"}, reg: 'A', hasReg: true, count: 1, action: ActMessageCopy},
		{name: `3"a2yy`, keys: []string{"3", `"`, "a", "2", "y", "y"}, reg: 'a', hasReg: true, count: 6, hasCount: true, action: ActMessageCopy},
		{name: `"a3dd`, keys: []string{`"`, "a", "3", "d", "d"}, reg: 'a', hasReg: true, count: 3, hasCount: true, action: ActMessageDelete},
		{name: `invalid register`, keys: []string{`"`, "!"}, unmatch: true},
		{name: `invalid named key`, keys: []string{`"`, "enter"}, unmatch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			r := last(e, Conversation, tt.keys...)
			if r.Unmatched != tt.unmatch {
				t.Fatalf("Unmatched = %v, want %v: %+v", r.Unmatched, tt.unmatch, r)
			}
			if tt.unmatch {
				return
			}
			if r.Action != tt.action || r.Register != tt.reg || r.HasRegister != tt.hasReg ||
				r.Count != tt.count || r.HasCount != tt.hasCount {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestRegisterPrefixYieldsToBinding(t *testing.T) {
	e, _ := newTestEngine(t)
	if err := e.Bind(Normal, Global, `"`, ActHelp); err != nil {
		t.Fatal(err)
	}
	if r := e.Feed(`"`, ChatList); r.Action != ActHelp {
		t.Fatalf(`bound " should fire, got %+v`, r)
	}
}

func TestEscCancels(t *testing.T) {
	tests := []struct {
		name string
		mode Mode
		keys []string
	}{
		{"count", Normal, []string{"5"}},
		{"count and prefix", Normal, []string{"5", "d"}},
		{"register", Normal, []string{`"`}},
		{"register named", Normal, []string{`"`, "a"}},
		{"ctrl+w", Normal, []string{"ctrl+w"}},
		{"leader", Normal, []string{"space"}},
		{"visual prefix", Visual, []string{"g"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := newTestEngine(t)
			e.SetMode(tt.mode)
			feed(e, Conversation, tt.keys...)
			if !e.Pending() {
				t.Fatal("expected pending")
			}
			r := e.Feed("esc", Conversation)
			if !r.Canceled || r.Action != "" || e.Pending() {
				t.Fatalf("esc: got %+v", r)
			}
			r = e.Feed("j", Conversation)
			if r.Action != ActCursorDown || r.HasCount || r.HasRegister {
				t.Fatalf("after cancel: got %+v", r)
			}
		})
	}

	// Esc with nothing pending is an ordinary key.
	e, _ := newTestEngine(t)
	if r := e.Feed("esc", ChatList); r.Action != ActCancel {
		t.Fatalf("idle esc: got %+v", r)
	}
}

func TestContextPrecedence(t *testing.T) {
	tests := []struct {
		ctx  Context
		keys []string
		want Action
	}{
		{ChatList, []string{"/"}, ActChatFilter},
		{Conversation, []string{"/"}, ActSearchForward},
		{Sidebar, []string{"/"}, ActSearchForward},
		{Global, []string{"/"}, ActSearchForward},
		{ChatList, []string{"enter"}, ActChatOpen},
		{Conversation, []string{"enter"}, ActMessageOpen},
		{Sidebar, []string{"enter"}, ActItemSelect},
		{Overlay(OverlayHelp), []string{"q"}, ActBack},
		{Overlay(OverlayHelp), []string{"j"}, ActCursorDown},
		{Overlay(OverlayConfirm), []string{"q"}, ActConfirmNo},
		{Overlay(OverlayConfirm), []string{"esc"}, ActConfirmNo},
		{Overlay(OverlayConfirm), []string{"y"}, ActConfirmYes},
		{Overlay(OverlayImage), []string{"+"}, ActZoomIn},
		{Overlay(OverlayImage), []string{"esc"}, ActBack},
		{Overlay(OverlayStory), []string{"l"}, ActStoryNext},
		{Overlay(OverlayStory), []string{"r"}, ActMessageReply},
		{AnyOverlay, []string{"enter"}, ActItemSelect},
		{Conversation, []string{"+"}, ActMessageReact},
		{Conversation, []string{"o"}, ActMessageOpenAttachment},
		{ChatList, []string{"o"}, ActModeInsert},
		{ChatList, []string{"l"}, ActCursorRight},
		{ChatList, []string{"r"}, ""},
		{Global, []string{"d", "d"}, ""},
	}
	for _, tt := range tests {
		e, _ := newTestEngine(t)
		r := last(e, tt.ctx, tt.keys...)
		if r.Action != tt.want {
			t.Errorf("%s %q: got %q, want %q", tt.ctx, tt.keys, r.Action, tt.want)
		}
	}
}

func TestContextBindingOverridesGlobalPrefix(t *testing.T) {
	c := &fakeClock{t: time.Unix(0, 0)}
	e := New(WithoutDefaults(), WithClock(c.now))
	mustBind(t, e, Normal, Global, "gg", ActCursorTop)
	mustBind(t, e, Normal, ChatList, "g", ActChatInfo)
	// In ChatList "g" is complete and a prefix of the global "gg".
	if r := e.Feed("g", ChatList); !r.Pending || !r.Ambiguous {
		t.Fatalf("got %+v, want ambiguous pending", r)
	}
	if r := e.Feed("g", ChatList); r.Action != ActCursorTop {
		t.Fatalf("got %+v", r)
	}
	// Elsewhere "g" is just a prefix.
	if r := e.Feed("g", Conversation); !r.Pending || r.Ambiguous {
		t.Fatalf("got %+v, want plain pending", r)
	}
}

func TestContextChangeResetsPending(t *testing.T) {
	e, _ := newTestEngine(t)
	e.Feed("d", ChatList)
	if r := e.Feed("D", Conversation); r.Unmatched != true {
		t.Fatalf("D alone in conversation should be unmatched, got %+v", r)
	}
}

func TestSetModeResetsPending(t *testing.T) {
	e, _ := newTestEngine(t)
	e.Feed("5", Conversation)
	e.SetMode(Insert)
	if e.Pending() || e.Mode() != Insert {
		t.Fatal("SetMode should reset pending state")
	}
	e.SetMode(Normal)
	if r := e.Feed("j", Conversation); r.HasCount {
		t.Fatalf("count leaked across mode change: %+v", r)
	}
	e.Feed("g", Conversation)
	e.Reset()
	if e.Pending() {
		t.Fatal("Reset should drop pending")
	}
}

func mustBind(t *testing.T, e *Engine, m Mode, c Context, keys string, a Action) {
	t.Helper()
	if err := e.Bind(m, c, keys, a); err != nil {
		t.Fatalf("Bind(%q): %v", keys, err)
	}
}

func TestAmbiguityTimeout(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *fakeClock) {
		c := &fakeClock{t: time.Unix(1000, 0)}
		e := New(WithoutDefaults(), WithClock(c.now), WithTimeout(500*time.Millisecond))
		mustBind(t, e, Normal, Global, "g", ActChatInfo)
		mustBind(t, e, Normal, Global, "gg", ActCursorTop)
		mustBind(t, e, Normal, Global, "gab", ActMediaGallery)
		mustBind(t, e, Normal, Global, "x", ActHelp)
		return e, c
	}

	t.Run("timeout value", func(t *testing.T) {
		e, _ := setup(t)
		if e.Timeout() != 500*time.Millisecond {
			t.Fatalf("Timeout = %v", e.Timeout())
		}
		if New().Timeout() != DefaultTimeout || DefaultTimeout != 600*time.Millisecond {
			t.Fatal("default timeout should be 600ms")
		}
	})

	t.Run("flush before and after deadline", func(t *testing.T) {
		e, c := setup(t)
		r := e.Feed("g", Global)
		if !r.Pending || !r.Ambiguous || r.Keys != "g" {
			t.Fatalf("got %+v", r)
		}
		if d, ok := e.Deadline(); !ok || !d.Equal(c.t.Add(500*time.Millisecond)) {
			t.Fatalf("Deadline = %v %v", d, ok)
		}
		c.advance(499 * time.Millisecond)
		if r := e.Flush(); !r.Pending || r.Action != "" {
			t.Fatalf("early flush: %+v", r)
		}
		c.advance(time.Millisecond)
		if r := e.Flush(); r.Action != ActChatInfo || r.Pending || r.Keys != "g" {
			t.Fatalf("flush: %+v", r)
		}
		if r := e.Flush(); r.Action != "" || r.Pending || r.Count != 1 {
			t.Fatalf("idle flush: %+v", r)
		}
		if _, ok := e.Deadline(); ok {
			t.Fatal("no deadline expected when idle")
		}
	})

	t.Run("count carried through flush", func(t *testing.T) {
		e, c := setup(t)
		feed(e, Global, "4", "g")
		c.advance(time.Second)
		if r := e.Flush(); r.Action != ActChatInfo || r.Count != 4 || !r.HasCount {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("longer match wins before timeout", func(t *testing.T) {
		e, _ := setup(t)
		if r := last(e, Global, "g", "g"); r.Action != ActCursorTop {
			t.Fatalf("got %+v", r)
		}
	})

	t.Run("non-continuing key fires prefix then itself", func(t *testing.T) {
		e, _ := setup(t)
		e.Feed("g", Global)
		res := e.Feed("x", Global).All()
		if len(res) != 2 || res[0].Action != ActChatInfo || res[1].Action != ActHelp {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("deeper failure replays leftover", func(t *testing.T) {
		e, _ := setup(t)
		feed(e, Global, "g")
		if r := e.Feed("a", Global); !r.Pending || !r.Ambiguous || r.Keys != "ga" {
			t.Fatalf("ga: %+v", r)
		}
		res := e.Feed("x", Global).All()
		want := []struct {
			a         Action
			unmatched bool
		}{{ActChatInfo, false}, {"", true}, {ActHelp, false}}
		if len(res) != len(want) {
			t.Fatalf("got %d results: %+v", len(res), res)
		}
		for i, w := range want {
			if res[i].Action != w.a || res[i].Unmatched != w.unmatched {
				t.Fatalf("result %d: %+v", i, res[i])
			}
		}
		if r := last(e, Global, "g", "a", "b"); r.Action != ActMediaGallery {
			t.Fatalf("gab: %+v", r)
		}
	})

	t.Run("stale timeout resolved on next key", func(t *testing.T) {
		e, c := setup(t)
		e.Feed("g", Global)
		c.advance(time.Second) // tick lost
		res := e.Feed("g", Global).All()
		if len(res) != 2 || res[0].Action != ActChatInfo || !res[1].Pending || !res[1].Ambiguous {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("deadline extends per key", func(t *testing.T) {
		e, c := setup(t)
		e.Feed("g", Global)
		c.advance(400 * time.Millisecond)
		e.Feed("a", Global)
		c.advance(400 * time.Millisecond)
		if r := e.Flush(); !r.Pending {
			t.Fatalf("deadline should restart after each key: %+v", r)
		}
		c.advance(100 * time.Millisecond)
		res := e.Flush().All()
		if len(res) != 2 || res[0].Action != ActChatInfo || !res[1].Unmatched {
			t.Fatalf("got %+v", res)
		}
	})

	t.Run("resolve ignores deadline", func(t *testing.T) {
		e, _ := setup(t)
		e.Feed("g", Global)
		if r := e.Resolve(); r.Action != ActChatInfo {
			t.Fatalf("got %+v", r)
		}
		e.Feed("2", Global)
		if r := e.Resolve(); !r.Pending || r.Keys != "2" {
			t.Fatalf("non-ambiguous resolve: %+v", r)
		}
	})

	t.Run("esc cancels ambiguous", func(t *testing.T) {
		e, c := setup(t)
		e.Feed("g", Global)
		if r := e.Feed("esc", Global); !r.Canceled {
			t.Fatalf("got %+v", r)
		}
		c.advance(time.Second)
		if r := e.Flush(); r.Action != "" || r.Pending {
			t.Fatalf("flush after cancel: %+v", r)
		}
	})

	t.Run("plain prefix never times out", func(t *testing.T) {
		e, c := setup(t)
		mustBind(t, e, Normal, Global, "zz", ActCenter)
		r := e.Feed("z", Global)
		if !r.Pending || r.Ambiguous {
			t.Fatalf("got %+v", r)
		}
		c.advance(time.Hour)
		if r := e.Flush(); !r.Pending {
			t.Fatalf("got %+v", r)
		}
		if r := e.Feed("z", Global); r.Action != ActCenter {
			t.Fatalf("got %+v", r)
		}
	})
}

func TestLeader(t *testing.T) {
	e, _ := newTestEngine(t)
	if e.Leader() != "space" {
		t.Fatalf("default leader = %q", e.Leader())
	}
	if err := e.SetLeader(","); err != nil {
		t.Fatal(err)
	}
	if r := last(e, ChatList, ",", "s"); r.Action != ActStories {
		t.Fatalf(",s: %+v", r)
	}
	if r := last(e, ChatList, ",", ","); r.Action != ActFinderChats {
		t.Fatalf(",,: %+v", r)
	}
	if r := last(e, ChatList, "space", "s"); !r.Unmatched {
		t.Fatalf("space s after remap: %+v", r)
	}
	if got := e.KeysForIn(ActStories, Normal, ChatList); !reflect.DeepEqual(got, []string{",s"}) {
		t.Fatalf("KeysForIn = %q", got)
	}
	for _, in := range []string{" ", "<Space>", "space"} {
		if err := e.SetLeader(in); err != nil || e.Leader() != "space" {
			t.Fatalf("SetLeader(%q) = %v, leader %q", in, err, e.Leader())
		}
	}
	for _, bad := range []string{"", "<Foo>", "ab", "<leader>"} {
		if err := e.SetLeader(bad); err == nil {
			t.Errorf("SetLeader(%q) should fail", bad)
		}
	}
	e2 := New(WithLeader("\\"))
	if r := last(e2, ChatList, "\\", "t"); r.Action != ActThemePicker {
		t.Fatalf("WithLeader: %+v", r)
	}
	e3 := New(WithLeader("<Nope>"))
	if e3.Leader() != "space" {
		t.Fatalf("invalid WithLeader should keep default, got %q", e3.Leader())
	}
}

func TestRepeat(t *testing.T) {
	e, _ := newTestEngine(t)

	r := e.Feed(".", ChatList)
	if r.Action != ActRepeat || r.Repeat {
		t.Fatalf("nothing to repeat: %+v", r)
	}
	if _, ok := e.LastChange(); ok {
		t.Fatal("LastChange should be empty")
	}

	if r := last(e, ChatList, "z", "m"); r.Action != ActChatMute {
		t.Fatalf("zm: %+v", r)
	}
	e.Feed("j", ChatList) // motions are not repeatable
	r = e.Feed(".", ChatList)
	if r.Action != ActChatMute || !r.Repeat || r.Count != 1 || r.Keys != "." {
		t.Fatalf(".: %+v", r)
	}

	r = last(e, Conversation, "2", "d", "d")
	if r.Action != ActMessageDelete || r.Count != 2 {
		t.Fatalf("2dd: %+v", r)
	}
	r = e.Feed(".", Conversation)
	if r.Action != ActMessageDelete || r.Count != 2 || !r.Repeat {
		t.Fatalf(". after 2dd: %+v", r)
	}
	r = last(e, Conversation, "5", ".")
	if r.Action != ActMessageDelete || r.Count != 5 || !r.HasCount || r.Keys != "5." {
		t.Fatalf("5.: %+v", r)
	}
	r = e.Feed(".", Conversation)
	if r.Count != 5 {
		t.Fatalf(". should keep the last count, got %+v", r)
	}

	// Registers on repeat.
	r = last(e, Conversation, `"`, "b", ".")
	if r.Register != 'b' || !r.HasRegister || r.Action != ActMessageDelete {
		t.Fatalf(`"b.: %+v`, r)
	}

	// UI-recorded change.
	e.SetLastChange(Result{Action: ActMessageReact, Pending: true})
	r = e.Feed(".", Conversation)
	if r.Action != ActMessageReact || !r.Repeat || r.Count != 1 || r.Pending {
		t.Fatalf("SetLastChange: %+v", r)
	}
	lc, ok := e.LastChange()
	if !ok || lc.Action != ActMessageReact {
		t.Fatalf("LastChange = %+v %v", lc, ok)
	}
}

func TestResultAllAndChain(t *testing.T) {
	a := Result{Action: ActHelp}
	b := Result{Action: ActQuit}
	c := Result{Action: ActBack}
	ch := chain(chain(a, b), c)
	all := ch.All()
	if len(all) != 3 || all[0].Action != ActHelp || all[1].Action != ActQuit || all[2].Action != ActBack {
		t.Fatalf("All = %+v", all)
	}
	for _, r := range all {
		if r.Next != nil {
			t.Fatal("All should clear Next")
		}
	}
	if !a.Matched() || (Result{Pending: true}).Matched() {
		t.Fatal("Matched")
	}
	// chain must not mutate its inputs.
	if a.Next != nil {
		t.Fatal("chain mutated input")
	}
}
