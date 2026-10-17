package media

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/termimg"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// errSvc fails every call the media views make.
type errSvc struct{ core.Service }

var errBoom = errors.New("boom\x1b]0;pwned\x07")

func (errSvc) Messages(context.Context, core.ChatID, core.MessageID, int) ([]core.Message, error) {
	return nil, errBoom
}
func (errSvc) Chat(context.Context, core.ChatID) (core.Chat, error) { return core.Chat{}, errBoom }
func (errSvc) Attachment(context.Context, core.MessageID, string) ([]byte, error) {
	return nil, errBoom
}
func (errSvc) MarkViewOnceOpened(context.Context, core.MessageID) error { return errBoom }

const hostile = "日本語😀 \x1b[2J\x1b]52;c;ZXZpbA==\x07evil‮txt.exe\r\nline2\x00"

func hostileMsg(att core.Attachment) core.Message {
	return core.Message{
		ID:          core.MessageID{Chat: "u:x", Author: "u:x", Timestamp: 1_700_000_000_000},
		SenderName:  hostile,
		Body:        hostile,
		SentAt:      testNow,
		Attachments: []core.Attachment{att},
		Preview:     &core.LinkPreview{URL: "https://example.com/" + hostile, Title: hostile},
	}
}

func assertClean(t *testing.T, what, out string) {
	t.Helper()
	plain := ansi.Strip(out)
	if strings.ContainsAny(plain, "\x07\x00\r‮") || strings.Contains(out, "\x1b]") || strings.Contains(out, "\x1b[2J") {
		t.Errorf("%s: unsanitized output %q", what, out)
	}
}

func TestHostileTextRendering(t *testing.T) {
	ctx := newCtx(t, nil, &fakeImages{})
	photo := core.Attachment{ID: "a1", ContentType: "image/png", FileName: hostile, Caption: hostile, Width: 800, Height: 600, Size: 12345}
	voice := core.Attachment{ID: "a2", ContentType: "audio/ogg", FileName: hostile, VoiceNote: true}
	file := core.Attachment{ID: "a3", ContentType: "application/pdf", FileName: hostile, Size: 99}

	v := NewViewerList(ctx, []Item{{Message: hostileMsg(photo), Attachment: photo}}, 0)
	checkView(t, v)
	assertClean(t, "viewer title", v.Title())
	for _, s := range sizes {
		assertClean(t, "viewer", v.View(s[0], s[1]))
	}

	g := NewGallery(ctx, "u:x")
	g.done = true
	m := hostileMsg(file)
	m.Attachments = []core.Attachment{photo, file, voice}
	g.Update(ui.EventMsg{Event: core.MessageAdded{Message: m}})
	for _, tab := range []Tab{TabMedia, TabFiles, TabLinks, TabVoice} {
		g.SetTab(tab)
		if g.Count(tab) == 0 {
			t.Errorf("tab %v empty", tab)
		}
		checkView(t, g)
		for _, s := range sizes {
			assertClean(t, "gallery "+tab.String(), g.View(s[0], s[1]))
		}
	}

	po := NewPlayerOverlay(ctx, hostileMsg(voice), voice)
	checkView(t, po)
	assertClean(t, "player title", po.Title())

	d := NewSaveDialog(ctx, Item{Message: hostileMsg(file), Attachment: file})
	checkView(t, d)
	if strings.ContainsAny(d.input.Value(), "\x1b\x07\r\n‮") {
		t.Errorf("save name not sanitized: %q", d.input.Value())
	}
}

func TestSafeURL(t *testing.T) {
	ok := []string{"https://example.com/a?b=c", "http://x.y", "mailto:a@b.c", "HTTPS://EXAMPLE.COM"}
	bad := []string{"file:///etc/passwd", "-a Calculator", "javascript:alert(1)", "sgnl://x", "https://", "/tmp/x", "", "ssh://h"}
	for _, u := range ok {
		if _, good := safeURL(u); !good {
			t.Errorf("%q rejected", u)
		}
	}
	for _, u := range bad {
		if _, good := safeURL(u); good {
			t.Errorf("%q accepted", u)
		}
	}
	if u, _ := safeURL("https://ex\x1bample.com/\x07x"); strings.ContainsAny(u, "\x1b\x07") {
		t.Errorf("control chars kept: %q", u)
	}
	t.Cleanup(restoreHooks())
	called := false
	startDetached = func(string, ...string) error { called = true; return nil }
	ctx := newCtx(t, nil, nil)
	run(openURL(ctx, "file:///etc/passwd"))
	if called {
		t.Error("file URL handed to the opener")
	}
}

func TestServiceErrors(t *testing.T) {
	ctx := newCtx(t, errSvc{}, &fakeImages{})
	g := NewGallery(ctx, "u:x")
	var o ui.Overlay = g
	o, _ = feed(o, g.Init(), typeIs[galleryPageMsg], 3)
	if o == nil || !g.Done() || g.err == "" {
		t.Fatalf("gallery error state: done=%v err=%q", g.Done(), g.err)
	}
	out := g.View(80, 20)
	if !strings.Contains(ansi.Strip(out), "Couldn't load") {
		t.Errorf("no error shown: %q", ansi.Strip(out))
	}
	assertClean(t, "gallery error", out)

	photo := core.Attachment{ID: "a1", ContentType: "image/png", Width: 10, Height: 10}
	m := hostileMsg(photo)
	v := NewViewer(ctx, m, photo)
	feed(v, v.Init(), typeIs[chatMediaMsg], 2)
	if v.Len() != 1 || v.scanning {
		t.Errorf("viewer after scan error: len=%d scanning=%v", v.Len(), v.scanning)
	}
	for _, k := range []string{"o", "y"} {
		_, cmd := v.Update(key(k))
		for _, msg := range run(cmd) {
			if tm, ok := msg.(ui.ToastMsg); ok {
				assertClean(t, "toast "+k, tm.Text)
			}
		}
	}

	vo := m
	vo.ViewOnce = true
	view := NewViewOnce(ctx, vo)
	_, cmd := view.HandleBack()
	toasts := msgsOfType[ui.ToastMsg](run(cmd))
	if len(toasts) != 1 || toasts[0].Level != ui.ToastError {
		t.Errorf("view-once mark error not reported: %v", toasts)
	}

	voice := core.Attachment{ID: "a2", ContentType: "audio/ogg", VoiceNote: true}
	p := NewPlayer(ctx, hostileMsg(voice), voice)
	step(p, toAny(run(p.Play())))
	if p.State() != PlayerFailed {
		t.Errorf("player state %v", p.State())
	}
	assertClean(t, "player", p.View(60))

	d := NewSaveDialog(ctx, Item{Message: m, Attachment: photo})
	d.input.SetValue(t.TempDir() + "/x.png")
	_, cmd = d.Update(key("enter"))
	for _, msg := range run(cmd) {
		d.Update(msg)
	}
	if d.level != ui.ToastError || d.saving {
		t.Errorf("save error state: %v %q", d.level, d.status)
	}
	assertClean(t, "save status", d.View(60, 5))
}

func TestNilServiceCommands(t *testing.T) {
	ctx := newCtx(t, nil, &fakeImages{})
	photo := core.Attachment{ID: "a1", ContentType: "image/png", Width: 10, Height: 10}
	it := Item{Message: hostileMsg(photo), Attachment: photo}
	for name, cmd := range map[string]func() []any{
		"open": func() []any { return toAny(run(openExternal(ctx, it))) },
		"copy": func() []any { return toAny(run(copyCmd(ctx, it))) },
	} {
		if msgs := cmd(); len(msgs) != 1 {
			t.Errorf("%s: %v", name, msgs)
		}
	}
	d := NewSaveDialog(ctx, it)
	d.input.SetValue(t.TempDir() + "/x.png")
	_, cmd := d.Update(key("enter"))
	for _, msg := range run(cmd) {
		d.Update(msg)
	}
	if d.level != ui.ToastError {
		t.Errorf("save without service: %q", d.status)
	}
}

func TestGalleryUpdateAndPrepend(t *testing.T) {
	g, _ := galleryFor(t, hasAtt(isPhoto))
	n := g.Count(TabMedia)
	first := g.media[0].Message
	first.RemoteDeleted = true
	g.Update(ui.EventMsg{Event: core.MessageUpdated{Message: first}})
	if g.Count(TabMedia) >= n {
		t.Error("remote-deleted message still listed")
	}

	// New messages arriving into an empty tab select the newest.
	ctx := newCtx(t, nil, &fakeImages{})
	e := NewGallery(ctx, "u:x")
	e.done = true
	for i := range 3 {
		ph := core.Attachment{ID: "p", ContentType: "image/png"}
		m := hostileMsg(ph)
		m.ID.Timestamp += uint64(i)
		e.Update(ui.EventMsg{Event: core.MessageAdded{Message: m}})
	}
	// The first entry stays selected while newer ones are prepended.
	if e.Count(TabMedia) != 3 || e.Selected() != 2 {
		t.Errorf("count %d selected %d", e.Count(TabMedia), e.Selected())
	}
	e.Update(act(keymap.ActCursorBottom))
	ph := core.Attachment{ID: "p", ContentType: "image/png"}
	m := hostileMsg(ph)
	m.ID.Timestamp += 10
	e.Update(ui.EventMsg{Event: core.MessageAdded{Message: m}})
	if e.Selected() != 3 {
		t.Errorf("selection did not follow its entry: %d", e.Selected())
	}
}

func TestViewerRemoteDeleteUpdate(t *testing.T) {
	ctx := newCtx(t, nil, &fakeImages{})
	a := core.Attachment{ID: "p", ContentType: "image/png"}
	m1, m2 := hostileMsg(a), hostileMsg(a)
	m2.ID.Timestamp++
	v := NewViewerList(ctx, []Item{{m1, a}, {m2, a}}, 1)
	m2.RemoteDeleted = true
	v.Update(ui.EventMsg{Event: core.MessageUpdated{Message: m2}})
	if v.Len() != 1 {
		t.Errorf("len %d", v.Len())
	}
}

func TestStopAllAndSinglePlayback(t *testing.T) {
	p1, procs, _ := newPlayerEnv(t)
	p2 := NewPlayer(p1.ctx, p1.it.Message, p1.it.Attachment)
	step(p1, toAny(run(p1.Play())))
	step(p1, toAny(run(p1.Play()))) // load → start
	if p1.State() != PlayerPlaying {
		t.Fatalf("p1 %v", p1.State())
	}
	first := p1.proc
	step(p2, toAny(run(p2.Play())))
	step(p2, toAny(run(p2.Play())))
	if p2.State() != PlayerPlaying || len(*procs) < 2 {
		t.Fatalf("p2 %v", p2.State())
	}
	if !(*procs)[0].killed {
		t.Error("first player still running after the second started")
	}
	// The first player learns its process was stopped from elsewhere.
	step(p1, toAny(run(waitExit(p1.seq, first))))
	if p1.State() != PlayerStopped {
		t.Errorf("p1 after takeover: %v", p1.State())
	}
	last := (*procs)[len(*procs)-1]
	StopAll()
	if !last.killed {
		t.Error("StopAll left a player running")
	}
	liveMu.Lock()
	n := len(liveProcs)
	liveMu.Unlock()
	if n != 0 {
		t.Errorf("%d live processes after StopAll", n)
	}
}

func TestViewerCloseReleasesKitty(t *testing.T) {
	ctx := newCtx(t, nil, &fakeImages{kitty: true})
	a := core.Attachment{ID: "g", ContentType: "image/gif", GIF: true, Width: 100, Height: 100}
	v := NewViewerList(ctx, []Item{{hostileMsg(a), a}}, 0)
	v.View(80, 24)
	v.Update(key("a"))
	id, _ := kittyID(termimg.KittyEncoder{}.Placeholder(300, 2, 1)[0])
	if !v.kittyControl[id] {
		t.Fatalf("animation not stopped: %v", v.kittyControl)
	}
	_, cmd := v.HandleBack()
	if cmd == nil || v.kittyControl[id] {
		t.Error("kitty animation left stopped after close")
	}
}
