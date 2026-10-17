package stories

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// errSvc is a core.Service whose story calls fail.
type errSvc struct {
	core.Service
	self    core.Contact
	err     error
	posted  []core.OutgoingStory
	failFor func(core.OutgoingStory) bool
}

func (s *errSvc) Self() core.Contact { return s.self }
func (s *errSvc) Stories(context.Context) ([]core.Story, error) {
	return nil, s.err
}
func (s *errSvc) Chat(context.Context, core.ChatID) (core.Chat, error) {
	return core.Chat{}, s.err
}
func (s *errSvc) Chats(context.Context, core.ChatFilter) ([]core.Chat, error) {
	return nil, s.err
}
func (s *errSvc) MarkStoryViewed(context.Context, core.MessageID) error { return s.err }
func (s *errSvc) ReplyToStory(context.Context, core.MessageID, string, string) error {
	return s.err
}
func (s *errSvc) PostStory(_ context.Context, st core.OutgoingStory) error {
	s.posted = append(s.posted, st)
	if s.failFor != nil {
		if s.failFor(st) {
			return s.err
		}
		return nil
	}
	return s.err
}

// hostileGroups are stories with wide, combining, control and escape text.
func hostileGroups() []authorGroup {
	at := testNow.Add(-time.Hour)
	evil := "\x1b[31mred\x1b]0;pwn\x07\r\n\tx‮"
	mk := func(a core.ACI, name string, ss ...core.Story) authorGroup {
		for i := range ss {
			ss[i].ID = core.MessageID{Author: a, Timestamp: uint64(i + 1)}
			ss[i].At = at
			ss[i].AuthorName = name
		}
		return authorGroup{Author: a, Name: name, Stories: ss}
	}
	long := strings.Repeat("漢字かな🎉👩‍👩‍👧‍👦", 80)
	return []authorGroup{
		mk("a1", "山田 太郎 👩‍👩‍👧‍👦 "+evil+strings.Repeat("長い名前", 30),
			core.Story{Text: &core.TextStory{Text: "日本語🎉", Background: 0xffff0000}},
			core.Story{Text: &core.TextStory{Text: long, Gradient: []uint32{0xff000000, 0x80ffffff}, URL: "https://例え.jp/" + evil + strings.Repeat("x", 300)}},
			core.Story{Text: &core.TextStory{Text: evil + strings.Repeat("W", 500)}},
			core.Story{Media: &core.Attachment{ContentType: "video/mp4", FileName: evil + "動画.mp4"}, Caption: long + evil},
			core.Story{Text: &core.TextStory{Text: "👍🏽👍🏽👍🏽 🇩🇪🇩🇪 é́́"}},
		),
		mk("a2", evil, core.Story{Media: &core.Attachment{ContentType: "image/png", FileName: evil, Width: 4000, Height: 10}, Caption: evil, Group: "g1"}),
	}
}

func TestHostileContentFitsAndIsSanitized(t *testing.T) {
	for _, th := range []string{"charm", "mono"} {
		ctx, _ := newCtx(t, th)
		groups := hostileGroups()
		titles := map[core.ChatID]string{"g1": "グループ\x1b[2J" + strings.Repeat("名", 50)}
		l := NewList(ctx)
		var o ui.Overlay = l
		o, _ = o.Update(storiesLoadedMsg{groups: groups, titles: titles})
		post := NewPost(ctx)
		var po ui.Overlay = post
		po, _ = po.Update(groupsLoadedMsg{p: post, chats: []core.Chat{{ID: "g1", Kind: core.ChatGroup, Title: titles["g1"]}}})
		po = typeText(t, po, "漢字🎉👩‍👩‍👧‍👦 wide")
		for _, sz := range sizes {
			W, H := sz[0], sz[1]
			out := checkView(t, th+"/hostile-list", o, W, H)
			checkClean(t, "list", out)
			checkClean(t, "post", checkView(t, th+"/hostile-post", po, W, H))
			for gi := range groups {
				v := newViewer(ctx, groups, titles, gi)
				for si := range v.groups[gi].Stories {
					v.si = si
					raw := v.View(min(W-10, 120), H-6)
					checkClean(t, "viewer", ansi.Strip(raw))
					checkView(t, th+"/hostile-viewer", v, W, H)
				}
			}
		}
	}
}

// checkClean fails when stripped output still contains control characters
// (other than newlines) that could only come from untrusted text.
func checkClean(t *testing.T, name, s string) {
	t.Helper()
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == '‮' {
			t.Errorf("%s: control rune %U in output", name, r)
			return
		}
	}
	if strings.Contains(s, "]0;pwn") {
		t.Errorf("%s: OSC payload leaked", name)
	}
}

func TestServiceErrors(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	svc := &errSvc{self: ctx.Self, err: errors.New("boom")}
	ctx.Svc = svc

	// List shows the error, does not panic, can still open the composer.
	l := NewList(ctx)
	o, _ := feed(t, l, run(l.Init())[0])
	if out := checkView(t, "list-err", o, 80, 24); !strings.Contains(out, "boom") {
		t.Errorf("list error not shown:\n%s", out)
	}

	// Viewer that loads itself toasts the error.
	v := NewViewer(ctx)
	vo, msgs := feed(t, v, run(v.Init())[0])
	if vo == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("viewer load error: %#v", msgs)
	}
	checkView(t, "viewer-err", vo, 80, 24)

	// Reply / reaction errors toast and keep the viewer open.
	v2 := newViewer(ctx, hostileGroups(), nil, 0)
	v2.Init()
	vo, msgs = feed(t, v2, key("1"))
	if vo == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("reaction error: %#v", msgs)
	}

	// Groups load error toasts a warning; posting errors keep the form.
	p := NewPost(ctx)
	var po ui.Overlay = p
	for _, m := range run(p.Init()) {
		var got []tea.Msg
		po, got = feed(t, po, m)
		msgs = append(msgs, got...)
	}
	po = typeText(t, po, "hi")
	po, msgs = feed(t, po, key("enter"))
	if po == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("post error: %#v", msgs)
	}
	if po.(*Post).posting {
		t.Fatal("still posting after error")
	}
}

// A partial failure (My Story ok, groups failed) must not repost to the
// audience that already succeeded when the user retries.
func TestPostPartialFailure(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	svc := &errSvc{self: ctx.Self, err: errors.New("group send failed"),
		failFor: func(s core.OutgoingStory) bool { return len(s.To) > 0 }}
	ctx.Svc = svc
	p := NewPost(ctx)
	var o ui.Overlay = p
	o, _ = o.Update(groupsLoadedMsg{p: p, chats: []core.Chat{{ID: "g1", Kind: core.ChatGroup, Title: "G"}}})
	p.audience[1].on = true
	o = typeText(t, o, "hello")
	o, msgs := feed(t, o, key("enter"))
	if o == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("partial failure: %#v", msgs)
	}
	if p.audience[0].on || !p.audience[1].on {
		t.Fatalf("succeeded audience should be cleared: %+v", p.audience)
	}
	svc.posted = nil
	svc.failFor = func(core.OutgoingStory) bool { return false }
	o, _ = feed(t, o, key("enter"))
	if o != nil {
		t.Fatal("retry should succeed and close")
	}
	if len(svc.posted) != 1 || len(svc.posted[0].To) != 1 {
		t.Fatalf("retry reposted to My Story: %+v", svc.posted)
	}
}

// Closing the viewer (or posting) reloads the stories so the list below
// it, which gets no events while covered, catches up.
func TestCloseReloadsStoriesForList(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	v := newViewer(ctx, l.groups, l.titles, 0)
	v.Init()
	o, cmd := v.Update(key("q"))
	if o != nil {
		t.Fatal("q closes")
	}
	found := false
	for _, m := range run(cmd) {
		if _, ok := m.(storiesLoadedMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("closing the viewer should reload stories")
	}
}

func TestLoadMediaQuickTimeFallback(t *testing.T) {
	dir := t.TempDir()
	mov := filepath.Join(dir, "clip.MOV")
	if err := os.WriteFile(mov, []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x00\x00qt  "), 0o600); err != nil {
		t.Fatal(err)
	}
	att, err := LoadMedia(mov)
	if err != nil || att.ContentType != "video/quicktime" {
		t.Fatalf("mov: %v %q", err, att.ContentType)
	}
	txt := filepath.Join(dir, "notes.mov")
	if err := os.WriteFile(txt, []byte("just text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMedia(txt); err == nil {
		t.Fatal("text with a .mov name must be rejected")
	}
}

// The screen lock pauses auto-advance, so no story advances (or is marked
// viewed) while nobody can see the screen.
func TestScreenLockPausesViewer(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	v := newViewer(ctx, l.groups, l.titles, 0)
	v.Init()
	gen := v.gen
	o, _ := v.Update(ui.ScreenLockMsg{})
	if o == nil || !v.paused || v.gen == gen {
		t.Fatalf("not paused by the screen lock (paused %v)", v.paused)
	}
	gi, si := v.gi, v.si
	if _, cmd := v.Update(tickMsg{v: v, gen: gen, step: time.Hour}); cmd != nil || v.gi != gi || v.si != si {
		t.Fatal("a pending tick advanced the paused viewer")
	}
}
