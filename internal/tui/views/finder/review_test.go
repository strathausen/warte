package finder

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// stubSvc is a core.Service whose methods used by the finder are scripted;
// any other method panics (nil embedded interface).
type stubSvc struct {
	core.Service
	chats    []core.Chat
	contacts []core.Contact
	hits     []core.SearchHit
	around   []core.Message
	err      error // returned by every scripted method when set
	queries  []core.SearchQuery
	chatsN   int
}

func (s *stubSvc) Self() core.Contact {
	return core.Contact{ACI: "me", ProfileName: "Me", IsSelf: true}
}
func (s *stubSvc) Chats(context.Context, core.ChatFilter) ([]core.Chat, error) {
	s.chatsN++
	return s.chats, s.err
}
func (s *stubSvc) Contacts(context.Context) ([]core.Contact, error) { return s.contacts, s.err }
func (s *stubSvc) Search(_ context.Context, q core.SearchQuery) ([]core.SearchHit, error) {
	s.queries = append(s.queries, q)
	return s.hits, s.err
}
func (s *stubSvc) MessagesAround(context.Context, core.MessageID, int) ([]core.Message, error) {
	return s.around, s.err
}
func (s *stubSvc) ResolveRecipient(context.Context, string) (core.ChatID, error) {
	return "", s.err
}
func (s *stubSvc) Forward(context.Context, core.MessageID, []core.ChatID) error { return s.err }

const evil = "\x1b]0;pwned\x07\x1b[31mRED‮\r\n\tX"

func hostileData() *stubSvc {
	at := testNow.Add(-time.Hour)
	mid := core.MessageID{Chat: "g:cjk", Author: "a1", Timestamp: uint64(at.UnixMilli())}
	body := "日本語のテキスト 🎉👩‍👩‍👧 pizza " + evil + strings.Repeat(" 長い", 60)
	return &stubSvc{
		chats: []core.Chat{
			{ID: "g:cjk", Kind: core.ChatGroup, Title: "読書クラブ 📚👨‍👩‍👧‍👦 " + evil, Unread: 1234, MentionsUnread: 2, Pinned: true,
				LastMessage: &core.MessagePreview{Sender: "李" + evil, Text: body, At: at}, Draft: ""},
			{ID: "u:a1", Kind: core.ChatDirect, Title: strings.Repeat("🙂", 80),
				Contact:     &core.Contact{ACI: "a1", Username: "x" + evil + ".01", E164: "+4917" + evil},
				LastMessage: &core.MessagePreview{Outgoing: true, Text: evil, At: at}, Draft: "draft " + evil},
		},
		contacts: []core.Contact{{ACI: "a2", ProfileName: "Zoë 张伟 " + evil, Username: "zoe.99"}},
		hits: []core.SearchHit{{
			Message:   core.Message{ID: mid, SenderName: "李" + evil, SentAt: at, Body: body},
			ChatTitle: "読書クラブ " + evil,
			Snippet:   body,
			Matches:   [][2]int{{0, 3}, {4, 2}, {len("日本語のテキスト 🎉👩‍👩‍👧 "), len("日本語のテキスト 🎉👩‍👩‍👧 pizza")}, {-1, 900}},
		}},
		around: []core.Message{
			{ID: core.MessageID{Chat: "g:cjk", Author: "a2", Timestamp: 1}, SenderName: evil, Body: evil + "前"},
			{ID: mid, SenderName: "李", Body: body},
			{ID: core.MessageID{Chat: "g:cjk", Timestamp: 3}, Kind: core.KindSystem, SystemText: evil},
		},
	}
}

func stubCtx(t *testing.T, svc core.Service) ui.Context {
	ctx, _ := testCtx(t)
	ctx.Svc = svc
	ctx.Self = core.Contact{ACI: "me"}
	return ctx
}

func assertClean(t *testing.T, name, out string) {
	t.Helper()
	// Strip our own SGR styling; anything left that is a control char came
	// from untrusted data.
	plain := ansi.Strip(out)
	for _, r := range plain {
		if (r < 0x20 && r != '\n') || r == 0x7f || r == '‮' {
			t.Fatalf("%s: control rune %U leaked into view: %q", name, r, plain)
		}
	}
	if strings.Contains(out, "\x1b]") || strings.Contains(out, "\x07") {
		t.Fatalf("%s: OSC sequence leaked: %q", name, out)
	}
}

func TestReviewHostileWidths(t *testing.T) {
	svc := hostileData()
	ctx := stubCtx(t, svc)

	c := push(t, NewChatSwitcher(ctx))
	checkView(t, c)
	assertClean(t, "chats", c.View(72, 24))
	c = typeText(t, c, "x")
	checkView(t, c)
	assertClean(t, "chats typed", c.View(72, 24))

	s := push(t, NewSearchQuery(ctx, "pizza"))
	if len(s.(*Search).Hits()) != 1 {
		t.Fatalf("hits = %d", len(s.(*Search).Hits()))
	}
	checkView(t, s)
	assertClean(t, "search", s.View(150, 40))

	p := push(t, NewForwardPicker(ctx, core.MessageID{Chat: "g:cjk", Timestamp: 1}))
	checkView(t, p)
	assertClean(t, "forward", p.View(80, 20))
}

func TestReviewServiceErrors(t *testing.T) {
	svc := hostileData()
	svc.err = errors.New("db locked " + evil)
	ctx := stubCtx(t, svc)

	c := push(t, NewChatSwitcher(ctx))
	if v := plain(c, 72, 20); !strings.Contains(v, "db locked") {
		t.Errorf("chat switcher should show the load error:\n%s", v)
	}
	assertClean(t, "chats err", c.View(72, 20))

	s := push(t, NewSearch(ctx))
	s = typeText(t, s, "pizza")
	time.Sleep(SearchDebounce + 50*time.Millisecond)
	if v := plain(s, 120, 30); !strings.Contains(v, "db locked") {
		t.Errorf("search should show the error:\n%s", v)
	}
	assertClean(t, "search err", s.View(120, 30))

	p := push(t, NewForwardPicker(ctx, core.MessageID{Chat: "g:cjk", Timestamp: 1}))
	if v := plain(p, 80, 20); !strings.Contains(v, "db locked") {
		t.Errorf("forward picker should show the error:\n%s", v)
	}
}

// A failed reload (e.g. after a ChatUpdated event) must not wipe the list.
func TestReviewReloadErrorKeepsEntries(t *testing.T) {
	svc := hostileData()
	ctx := stubCtx(t, svc)
	c := push(t, NewChatSwitcher(ctx)).(*Chats)
	n := len(c.results)
	if n == 0 {
		t.Fatal("no results")
	}
	svc.err = errors.New("boom")
	o, cmd := c.Update(ui.EventMsg{Event: core.ChatUpdated{}})
	o, _ = drain(t, o, cmd)
	if got := len(o.(*Chats).results); got != n {
		t.Errorf("results after failed reload = %d, want %d", got, n)
	}
}

// Bursts of chat events coalesce into at most one extra reload.
func TestReviewReloadCoalesces(t *testing.T) {
	svc := hostileData()
	ctx := stubCtx(t, svc)
	c := push(t, NewChatSwitcher(ctx)).(*Chats)
	before := svc.chatsN
	var cmds []tea.Cmd
	var o ui.Overlay = c
	for range 50 {
		var cmd tea.Cmd
		o, cmd = o.Update(ui.EventMsg{Event: core.ChatUpdated{}})
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		o, _ = drain(t, o, cmd)
	}
	// Each load calls Chats twice (active + archived).
	if loads := (svc.chatsN - before) / 2; loads > 2 {
		t.Errorf("50 events caused %d reloads, want <= 2", loads)
	}
}

// has: values outside core.SearchQuery's contract are reported, never sent.
func TestReviewHasTypesMatchContract(t *testing.T) {
	for _, v := range []string{"sticker", "poll"} {
		q := ParseQuery("has:"+v, testNow)
		if q.Has != "" || len(q.Errors) != 1 {
			t.Errorf("has:%s parsed as %+v; the store rejects it", v, q)
		}
	}
	svc := hostileData()
	ctx := stubCtx(t, svc)
	s := push(t, NewSearchQuery(ctx, "has:sticker"))
	if len(svc.queries) != 0 {
		t.Errorf("unsupported has: reached the backend: %+v", svc.queries)
	}
	if v := plain(s, 120, 30); !strings.Contains(v, "has:sticker") {
		t.Errorf("filter error not shown:\n%s", v)
	}
}

// A query consisting only of a bad filter shows the error, not the help.
func TestReviewErrorOnlyQuery(t *testing.T) {
	svc := hostileData()
	ctx := stubCtx(t, svc)
	s := push(t, NewSearch(ctx))
	s = typeText(t, s, "before:soon")
	o, cmd := s.Update(searchDebounceMsg{s: s.(*Search), seq: s.(*Search).seq})
	o, _ = drain(t, o, cmd)
	v := plain(o, 120, 30)
	if !strings.Contains(v, "before:soon") || strings.Contains(v, "Filters can go anywhere") {
		t.Errorf("bad filter should be reported:\n%s", v)
	}
}

// from:/in: typed before contacts and chats are loaded must wait for them
// instead of reporting "no contact".
func TestReviewFromWaitsForData(t *testing.T) {
	svc := hostileData()
	ctx := stubCtx(t, svc)
	s := NewSearch(ctx)
	init := s.Init() // not run yet: data not loaded
	var o ui.Overlay = s
	for _, r := range "from:zoe" {
		o, _ = o.Update(key(string(r)))
	}
	o, cmd := o.Update(searchDebounceMsg{s: s, seq: s.seq})
	o, _ = drain(t, o, cmd)
	if len(s.problems) != 0 {
		t.Fatalf("problems before data loaded: %v", s.problems)
	}
	o, _ = drain(t, o, init)
	if len(svc.queries) != 1 || svc.queries[0].From != "a2" {
		t.Errorf("queries = %+v, want one from a2", svc.queries)
	}
	_ = o
}

// Picker highlights must survive items whose filter text needed sanitizing.
func TestReviewPickerHighlightSanitized(t *testing.T) {
	ctx, _ := testCtx(t)
	p := NewPicker(ctx, PickerOptions[string]{
		Items:  []string{"ab\x01cd"},
		Filter: func(s string) string { return s },
	})
	p.SetQuery("cd")
	out := p.View(40, 6)
	if !strings.Contains(out, "\x1b[") || strings.Contains(ansi.Strip(out), "\x1b") {
		t.Fatalf("unexpected view %q", out)
	}
	r := Row{Matches: p.matches[0].idx, Styles: ctx.Styles}
	if got := r.Highlight("ab\x01cd"); got == r.Base().Render("abcd") {
		t.Errorf("matches dropped for sanitized text: %q", got)
	}
}
