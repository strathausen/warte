package media

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// galleryFor opens the gallery of the chat containing a message that
// matches pred and loads it completely.
func galleryFor(t *testing.T, pred func(core.Message) bool) (*Gallery, ui.Context) {
	t.Helper()
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	m := findMsg(t, svc, pred)
	g := NewGallery(ctx, m.ID.Chat)
	g.View(100, 30)
	cmd := g.Init()
	for i := 0; i < 100 && cmd != nil; i++ {
		var msgs = run(cmd)
		cmd = nil
		for _, msg := range msgs {
			_, cmd = g.Update(msg)
		}
	}
	return g, ctx
}

func TestGalleryLoadsAndRenders(t *testing.T) {
	g, _ := galleryFor(t, hasAtt(isPhoto))
	if g.Count(TabMedia) == 0 {
		t.Fatal("no media loaded")
	}
	if g.chatTitle == "" || !strings.Contains(g.Title(), "Media ·") {
		t.Errorf("title: %q", g.Title())
	}
	// Newest first.
	for i := 1; i < len(g.media); i++ {
		if g.media[i].Message.ID.Timestamp > g.media[i-1].Message.ID.Timestamp {
			t.Fatalf("media not newest first at %d", i)
		}
	}
	for tab := range tabCount {
		g.SetTab(tab)
		checkView(t, g)
	}
	out := ansi.Strip(g.View(100, 30))
	for _, want := range []string{"Media", "Files", "Links", "Voice", "╭"} {
		g.SetTab(TabMedia)
		out = ansi.Strip(g.View(100, 30))
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGalleryEmptyAndLoading(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	g := NewGallery(ctx, "u:nobody")
	checkView(t, g)
	if !strings.Contains(ansi.Strip(g.View(60, 20)), "Searching") {
		t.Errorf("loading state not shown")
	}
	msgs := run(g.Init())
	for _, m := range msgs {
		g.Update(m)
	}
	if !g.Done() {
		t.Fatal("unknown chat should finish (with an error)")
	}
	checkView(t, g)
	for _, a := range []keymap.Action{keymap.ActCursorDown, keymap.ActCursorRight, keymap.ActItemSelect, keymap.ActCursorBottom, keymap.ActPageDown} {
		g.Update(act(a))
	}
	for _, k := range []string{"o", "s", "y", "m", "]"} {
		g.Update(key(k))
	}
	g.Update(ui.EventMsg{Event: core.MessageRemoved{ID: core.MessageID{Timestamp: 1}}})
	// No service at all.
	none := NewGallery(ui.Context{Styles: ctx.Styles, Icons: ctx.Icons}, "x")
	if none.Init() != nil {
		t.Error("no service: no load")
	}
	checkView(t, none)
}

func TestGalleryNavigationAndOpen(t *testing.T) {
	g, _ := galleryFor(t, hasAtt(isPhoto))
	g.View(100, 30)
	cols := g.gridCols
	if cols < 2 {
		t.Fatalf("grid has %d columns at width 100", cols)
	}
	g.Update(act(keymap.ActCursorRight))
	if g.Selected() != min(1, g.Count(TabMedia)-1) {
		t.Errorf("l: sel %d", g.Selected())
	}
	g.Update(act(keymap.ActCursorDown))
	if want := min(1+cols, g.Count(TabMedia)-1); g.Selected() != want {
		t.Errorf("j: sel %d want %d", g.Selected(), want)
	}
	g.Update(act(keymap.ActCursorTop))
	if g.Selected() != 0 {
		t.Errorf("gg: %d", g.Selected())
	}
	// Enter opens the viewer on the same item, navigating the gallery list.
	_, cmd := g.Update(act(keymap.ActItemSelect))
	push := msgsOfType[ui.PushOverlayMsg](run(cmd))
	if len(push) != 1 {
		t.Fatalf("enter: %v", push)
	}
	v, ok := push[0].Overlay.(*Viewer)
	if !ok {
		t.Fatalf("enter pushed %T", push[0].Overlay)
	}
	cur, _ := v.Current()
	if cur.key() != g.media[0].key() || v.Len() != g.Count(TabMedia) {
		t.Errorf("viewer opened on wrong item")
	}
	if v.Init() != nil {
		t.Errorf("list viewer should not rescan the chat")
	}
	// Tabs.
	g.Update(act(keymap.ActFocusNext))
	if g.Tab() != TabFiles {
		t.Errorf("tab: %v", g.Tab())
	}
	g.Update(act(keymap.ActFocusPrev))
	g.Update(act(keymap.ActFocusPrev))
	if g.Tab() != TabVoice {
		t.Errorf("shift-tab wraps: %v", g.Tab())
	}
	g.Update(key("]"))
	if g.Tab() != TabMedia {
		t.Errorf("]: %v", g.Tab())
	}
	// "m" jumps to the message.
	o, cmd := g.Update(key("m"))
	if o != nil {
		t.Error("m should close the gallery")
	}
	if oc := msgsOfType[ui.OpenChatMsg](run(cmd)); len(oc) != 1 || oc[0].Focus == nil {
		t.Errorf("m: %v", oc)
	}
	// Mouse: click a tab.
	g2, _ := galleryFor(t, hasAtt(isPhoto))
	g2.View(100, 30)
	g2.Update(clickAt(width(" "+g2.tabLabel(TabMedia)+" ")+2, 0))
	if g2.Tab() != TabFiles {
		t.Errorf("click tab: %v", g2.Tab())
	}
}

func TestGalleryFilesLinksVoice(t *testing.T) {
	g, _ := galleryFor(t, hasAtt(func(a core.Attachment) bool { return a.ContentType == "application/pdf" }))
	g.SetTab(TabFiles)
	if g.Count(TabFiles) == 0 {
		t.Fatal("no files")
	}
	out := ansi.Strip(g.View(100, 30))
	if !strings.Contains(out, ".pdf") {
		t.Errorf("file name missing:\n%s", out)
	}
	_, cmd := g.Update(act(keymap.ActItemSelect))
	msgs := run(cmd)
	if len(msgsOfType[ui.CloseOverlayMsg](msgs)) != 1 || len(msgsOfType[ui.OpenChatMsg](msgs)) != 1 {
		t.Errorf("enter on a file jumps to the message: %v", msgs)
	}

	gv, _ := galleryFor(t, hasAtt(func(a core.Attachment) bool { return a.VoiceNote }))
	gv.SetTab(TabVoice)
	if gv.Count(TabVoice) == 0 {
		t.Fatal("no voice notes")
	}
	_, cmd = gv.Update(act(keymap.ActItemSelect))
	push := msgsOfType[ui.PushOverlayMsg](run(cmd))
	if len(push) != 1 {
		t.Fatalf("enter on voice: %v", push)
	}
	if _, ok := push[0].Overlay.(*PlayerOverlay); !ok {
		t.Errorf("voice opens %T", push[0].Overlay)
	}

	gl, ctx := galleryFor(t, func(m core.Message) bool {
		return m.Preview != nil || strings.Contains(m.Body, "https://")
	})
	gl.SetTab(TabLinks)
	if gl.Count(TabLinks) == 0 {
		t.Fatal("no links")
	}
	checkView(t, gl)
	var opened []string
	t.Cleanup(restoreHooks())
	startDetached = func(name string, args ...string) error { opened = append([]string{name}, args...); return nil }
	ctx.Config.OpenWith["url"] = "browser"
	gl.ctx = ctx
	_, cmd = gl.Update(key("o"))
	run(cmd)
	if len(opened) != 2 || opened[0] != "browser" || !strings.Contains(opened[1], "://") {
		t.Errorf("o on link: %v", opened)
	}
}

func TestGalleryEvents(t *testing.T) {
	g, _ := galleryFor(t, hasAtt(isPhoto))
	n := g.Count(TabMedia)
	first := g.media[0]
	g.Update(ui.EventMsg{Event: core.MessageRemoved{ID: first.Message.ID}})
	if g.Count(TabMedia) >= n {
		t.Fatal("removed message still listed")
	}
	added := first.Message
	added.ID.Timestamp += 1_000_000
	g.Update(ui.EventMsg{Event: core.MessageAdded{Message: added}})
	if g.Count(TabMedia) != n || g.media[0].Message.ID != added.ID {
		t.Fatal("new message not prepended")
	}
	// Another chat's message is ignored.
	other := added
	other.ID.Chat = "u:someone-else"
	other.ID.Timestamp++
	g.Update(ui.EventMsg{Event: core.MessageAdded{Message: other}})
	if g.Count(TabMedia) != n {
		t.Fatal("foreign message added")
	}
}

func TestParseTab(t *testing.T) {
	for in, want := range map[string]Tab{"": TabMedia, "f": TabFiles, "links": TabLinks, "VOICE": TabVoice, "audio": TabVoice} {
		got, ok := parseTab(in)
		if in != "" && !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseTab("bogus"); ok {
		t.Error("bogus parsed")
	}
}
