package keymap

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// insertJK returns an engine with a vim-style "jk" escape mapping in Insert.
func insertJK(t *testing.T) (*Engine, *fakeClock) {
	t.Helper()
	c := &fakeClock{t: time.Unix(1000, 0)}
	e := New(WithClock(c.now), WithTimeout(300*time.Millisecond))
	mustBind(t, e, Insert, Global, "jk", ActModeNormal)
	e.SetMode(Insert)
	return e, c
}

func TestInsertPartialMappingFiresOnFullSequence(t *testing.T) {
	e, _ := insertJK(t)
	r := e.Feed("j", Conversation)
	if !r.Pending || !r.Expires || r.Ambiguous {
		t.Fatalf("j: got %+v, want pending+expires", r)
	}
	if _, ok := e.Deadline(); !ok {
		t.Fatal("Deadline should be set for expiring insert prefix")
	}
	r = e.Feed("k", Conversation)
	if r.Action != ActModeNormal {
		t.Fatalf("jk: got %+v", r)
	}
}

func TestInsertPartialMappingThenBoundKey(t *testing.T) {
	// "j" then enter must insert "j" and then send, not insert "j<enter>".
	e, _ := insertJK(t)
	e.Feed("j", Conversation)
	rs := e.Feed("enter", Conversation).All()
	if len(rs) != 2 {
		t.Fatalf("got %d results: %+v", len(rs), rs)
	}
	if !rs[0].Unmatched || !reflect.DeepEqual(rs[0].Seq, []string{"j"}) {
		t.Fatalf("first: %+v", rs[0])
	}
	if rs[1].Action != ActComposerSend {
		t.Fatalf("second: %+v", rs[1])
	}
}

func TestInsertPartialMappingReprocessesRest(t *testing.T) {
	// "jj" then "k": first j is text, second j restarts the mapping.
	e, _ := insertJK(t)
	e.Feed("j", Conversation)
	rs := e.Feed("j", Conversation).All()
	if len(rs) != 2 || !rs[0].Unmatched || rs[0].Keys != "j" || !rs[1].Pending {
		t.Fatalf("jj: %+v", rs)
	}
	r := e.Feed("k", Conversation)
	if r.Action != ActModeNormal {
		t.Fatalf("jjk: %+v", r)
	}
	// "jx": both become text.
	e.Feed("j", Conversation)
	rs = e.Feed("x", Conversation).All()
	if len(rs) != 2 || !rs[0].Unmatched || !rs[1].Unmatched ||
		rs[0].Keys != "j" || rs[1].Keys != "x" {
		t.Fatalf("jx: %+v", rs)
	}
}

func TestInsertPartialMappingTimesOutAsText(t *testing.T) {
	e, c := insertJK(t)
	e.Feed("j", Conversation)
	if r := e.Flush(); !r.Pending {
		t.Fatalf("early flush: %+v", r)
	}
	c.advance(300 * time.Millisecond)
	r := e.Flush()
	if !r.Unmatched || !reflect.DeepEqual(r.Seq, []string{"j"}) {
		t.Fatalf("flush after timeout: %+v", r)
	}
	if e.Pending() {
		t.Fatal("still pending after flush")
	}

	// Without a Flush, the next key resolves the stale prefix first.
	e.Feed("j", Conversation)
	c.advance(time.Second)
	rs := e.Feed("k", Conversation).All()
	if len(rs) != 2 || !rs[0].Unmatched || rs[0].Keys != "j" || !rs[1].Unmatched || rs[1].Keys != "k" {
		t.Fatalf("stale prefix: %+v", rs)
	}

	// Resolve ignores the timeout.
	e.Feed("j", Conversation)
	if r := e.Resolve(); !r.Unmatched || r.Keys != "j" {
		t.Fatalf("resolve: %+v", r)
	}
}

func TestNormalPlainPrefixDoesNotExpire(t *testing.T) {
	e, c := newTestEngine(t)
	r := e.Feed("g", Conversation)
	if !r.Pending || r.Expires || r.Ambiguous {
		t.Fatalf("g: %+v", r)
	}
	c.advance(time.Hour)
	if r := e.Flush(); !r.Pending {
		t.Fatalf("flush: %+v", r)
	}
	if r := e.Feed("g", Conversation); r.Action != ActCursorTop {
		t.Fatalf("gg: %+v", r)
	}
	// Normal mode keeps vim's behaviour of discarding a failed sequence.
	e.Feed("g", Conversation)
	if r := e.Feed("j", Conversation); !r.Unmatched || r.Next != nil || r.Keys != "gj" {
		t.Fatalf("gj: %+v", r)
	}
}

func TestAmbiguousSetsExpires(t *testing.T) {
	c := &fakeClock{t: time.Unix(1000, 0)}
	e := New(WithoutDefaults(), WithClock(c.now))
	mustBind(t, e, Normal, Global, "g", ActChatInfo)
	mustBind(t, e, Normal, Global, "gg", ActCursorTop)
	if r := e.Feed("g", Global); !r.Ambiguous || !r.Expires {
		t.Fatalf("%+v", r)
	}
}

func TestDisplayKeysEscapesControlCharacters(t *testing.T) {
	got := DisplayKeys([]string{"\x1b[31m", "a", "\x07"})
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("raw control bytes leaked: %q", got)
	}
	if !strings.Contains(got, `\x1b[31m`) || !strings.Contains(got, `\a`) {
		t.Fatalf("unexpected rendering %q", got)
	}
	if got := DisplayKeys([]string{"\xff"}); got != `\xff` {
		t.Fatalf("invalid utf8: %q", got)
	}
	// Ordinary keys are untouched.
	if got := DisplayKeys([]string{"é", "ctrl+w", "h"}); got != "é ctrl+w h" {
		t.Fatalf("%q", got)
	}
	// Results built from hostile input are safe to print as well.
	e, _ := newTestEngine(t)
	r := e.Feed("\x1b]0;pwned\x07", Conversation)
	if strings.ContainsAny(r.Keys, "\x1b\x07") {
		t.Fatalf("Result.Keys leaked control bytes: %q", r.Keys)
	}
}

func TestFeedNeverPanicsOnOddInput(t *testing.T) {
	e, _ := newTestEngine(t)
	inputs := []string{"", "+", "ctrl+", "+a", "alt++", "\x00", "\xff\xfe", strings.Repeat("9", 50), "ctrl+alt+shift+meta+hyper+super+x"}
	for _, m := range AllModes {
		e.SetMode(m)
		for _, ctx := range []Context{Global, ChatList, Conversation, Sidebar, Overlay("x")} {
			for _, in := range inputs {
				_ = e.Feed(in, ctx)
			}
			_ = e.Flush()
		}
	}
	e.SetMode(Normal)
	// A huge count is clamped.
	for range 30 {
		e.Feed("9", Conversation)
	}
	if r := e.Feed("j", Conversation); r.Count != maxCount {
		t.Fatalf("count = %d", r.Count)
	}
}
