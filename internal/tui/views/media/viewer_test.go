package media

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

func openViewer(t *testing.T, imgs *fakeImages) (*Viewer, ui.Context, core.Message) {
	t.Helper()
	svc := newSvc(t)
	ctx := newCtx(t, svc, imgs)
	m := findMsg(t, svc, hasAtt(isPhoto))
	v := NewViewer(ctx, m, firstAtt(m, isPhoto))
	o, _ := feed(v, v.Init(), typeIs[chatMediaMsg], 3)
	if o != v {
		t.Fatal("viewer closed during init")
	}
	return v, ctx, m
}

func TestViewerSizesAndEmpty(t *testing.T) {
	imgs := &fakeImages{}
	v, ctx, _ := openViewer(t, imgs)
	checkView(t, v)
	v.Update(act(keymap.ActZoomIn))
	v.Update(act(keymap.ActZoomIn))
	checkView(t, v)

	empty := NewViewerList(ctx, nil, 3)
	checkView(t, empty)
	if out := empty.View(40, 10); !strings.Contains(out, "No media") {
		t.Errorf("empty viewer: %q", out)
	}
	// Keys on an empty viewer must not panic.
	for _, a := range []keymap.Action{keymap.ActCursorLeft, keymap.ActCursorRight, keymap.ActZoomIn, keymap.ActCursorBottom} {
		empty.Update(act(a))
	}
	for _, k := range []string{"o", "y", "s", "n", "p", "m", "a"} {
		empty.Update(key(k))
	}
	// No images at all.
	noImg := NewViewerList(ui.Context{Styles: ctx.Styles, Icons: ctx.Icons}, []Item{{Attachment: core.Attachment{ID: "x", ContentType: "image/png"}}}, 0)
	checkView(t, noImg)
}

func TestViewerNavigation(t *testing.T) {
	v, _, m := openViewer(t, &fakeImages{})
	if v.Len() < 2 {
		t.Fatalf("expected the chat's media to load, got %d items", v.Len())
	}
	cur, _ := v.Current()
	if cur.Message.ID != m.ID {
		t.Fatalf("current item moved after scan: %v", cur.Message.ID)
	}
	v.View(100, 30)
	v.Update(act(keymap.ActCursorTop))
	v.Update(act(keymap.ActCursorRight))
	start := v.Index()
	v.Update(act(keymap.ActCursorLeft))
	v.Update(act(keymap.ActCursorRight))
	if v.Index() != start {
		t.Errorf("h then l: index %d, want %d", v.Index(), start)
	}
	v.Update(act(keymap.ActCursorTop))
	if v.Index() != 0 {
		t.Errorf("gg: index %d", v.Index())
	}
	v.Update(key("n"))
	if v.Index() != 1 {
		t.Errorf("n: index %d", v.Index())
	}
	v.Update(key("p"))
	if v.Index() != 0 {
		t.Errorf("p: index %d", v.Index())
	}
	v.Update(act(keymap.ActCursorBottom))
	if v.Index() != v.Len()-1 {
		t.Errorf("G: index %d of %d", v.Index(), v.Len())
	}
	if !strings.Contains(v.Title(), "/") {
		t.Errorf("title lacks position: %q", v.Title())
	}
	// Items are oldest first.
	for i := 1; i < v.Len(); i++ {
		if v.items[i].Message.ID.Timestamp < v.items[i-1].Message.ID.Timestamp {
			t.Fatalf("items not chronological at %d", i)
		}
	}
}

func TestViewerZoomPan(t *testing.T) {
	imgs := &fakeImages{}
	v, _, _ := openViewer(t, imgs)
	w, h := 100, 30
	v.View(w, h)
	idx := v.Index()
	v.Update(act(keymap.ActZoomIn))
	v.Update(act(keymap.ActZoomIn))
	if v.Zoom() <= 1 {
		t.Fatalf("zoom %v", v.Zoom())
	}
	out := v.View(w, h)
	_, _, zc, zr := v.layout(w, v.imageHeight(h))
	if zc <= w && zr <= v.imageHeight(h) {
		t.Fatalf("zoomed image %dx%d fits the %dx%d viewport", zc, zr, w, h)
	}
	for _, l := range strings.Split(out, "\n") {
		if ansi.StringWidth(l) > w {
			t.Fatalf("zoomed line too wide")
		}
	}
	px, py := v.panX, v.panY
	// h/l pan while zoomed instead of switching images.
	v.Update(act(keymap.ActCursorRight))
	v.Update(act(keymap.ActCursorDown))
	v.View(w, h)
	if v.Index() != idx {
		t.Errorf("l switched image while zoomed")
	}
	if v.panX == px && v.panY == py {
		t.Errorf("pan did not move: %d,%d", v.panX, v.panY)
	}
	v.Update(act(keymap.ActCursorBottom))
	v.View(w, h)
	if v.panY != zr-v.imageHeight(h) && zr > v.imageHeight(h) {
		t.Errorf("G pans to bottom: panY=%d want %d", v.panY, zr-v.imageHeight(h))
	}
	v.Update(act(keymap.ActZoomReset))
	if v.Zoom() != 1 || v.panX != 0 || v.panY != 0 {
		t.Errorf("0 should reset zoom and pan: %v %d %d", v.Zoom(), v.panX, v.panY)
	}
	// Max zoom is clamped.
	for range 20 {
		v.Update(act(keymap.ActZoomIn))
	}
	if v.Zoom() != zoomLevels[len(zoomLevels)-1] {
		t.Errorf("zoom not clamped: %v", v.Zoom())
	}
	checkView(t, v)
}

func TestViewerKittyCrop(t *testing.T) {
	imgs := &fakeImages{kitty: true}
	v, _, _ := openViewer(t, imgs)
	v.Update(act(keymap.ActZoomIn))
	v.Update(act(keymap.ActZoomIn))
	v.View(80, 24)
	v.Update(act(keymap.ActCursorRight))
	out := v.View(80, 24)
	found := false
	for _, l := range strings.Split(out, "\n") {
		if strings.ContainsRune(l, '\U0010EEEE') {
			found = true
			// The id color must survive the crop.
			if !strings.Contains(l, "\x1b[38;") {
				t.Fatalf("cropped placeholder row lost its id color: %q", l)
			}
		}
	}
	if !found {
		t.Fatal("no placeholder rows rendered")
	}
}

func TestViewerFooterAndActions(t *testing.T) {
	v, ctx, m := openViewer(t, &fakeImages{})
	out := ansi.Strip(v.View(100, 30))
	if !strings.Contains(out, senderName(m)) {
		t.Errorf("footer lacks sender %q:\n%s", senderName(m), out)
	}
	// "s" pushes the save dialog.
	_, cmd := v.Update(key("s"))
	msgs := run(cmd)
	if len(msgsOfType[ui.PushOverlayMsg](msgs)) != 1 {
		t.Fatalf("s: %v", msgs)
	}
	if _, ok := msgsOfType[ui.PushOverlayMsg](msgs)[0].Overlay.(*SaveDialog); !ok {
		t.Fatalf("s pushed %T", msgsOfType[ui.PushOverlayMsg](msgs)[0].Overlay)
	}
	// "o" opens externally.
	t.Cleanup(restoreHooks()) // before the seams are replaced
	var opened []string
	startDetached = func(name string, args ...string) error { opened = append([]string{name}, args...); return nil }
	removed := ""
	removeAfter = func(p string, _ timeDuration) { removed = p }
	ctx.Config.OpenWith["image"] = "myviewer --flag"
	v.ctx = ctx
	_, cmd = v.Update(key("o"))
	msgs = run(cmd)
	if len(opened) != 3 || opened[0] != "myviewer" || opened[2] != removed {
		t.Fatalf("o: opened %v removed %q msgs %v", opened, removed, msgs)
	}
	assertMode(t, removed, 0o600)
	_ = os.Remove(removed)
	// "m" jumps to the message.
	o, cmd := v.Update(key("m"))
	if o != nil {
		t.Errorf("m should close the viewer")
	}
	jm := msgsOfType[ui.OpenChatMsg](run(cmd))
	if len(jm) != 1 || jm[0].Focus == nil || jm[0].Focus.Chat != jm[0].Chat {
		t.Errorf("m: %v", jm)
	}
	// Enter closes.
	v2, _, _ := openViewer(t, &fakeImages{})
	if o, _ := v2.Update(act(keymap.ActItemSelect)); o != nil {
		t.Errorf("enter should close")
	}
}

func TestViewerEvents(t *testing.T) {
	v, _, m := openViewer(t, &fakeImages{})
	n := v.Len()
	v.Update(ui.EventMsg{Event: core.MessageRemoved{ID: m.ID}})
	if v.Len() >= n {
		t.Fatalf("removed message still listed: %d", v.Len())
	}
	cur, ok := v.Current()
	if !ok || cur.Message.ID == m.ID {
		t.Fatalf("current item not moved off the removed message")
	}
	single := NewViewerList(v.ctx, []Item{cur}, 0)
	o, cmd := single.Update(ui.EventMsg{Event: core.MessageRemoved{ID: cur.Message.ID}})
	if o != nil {
		t.Fatalf("viewer with no items left should close")
	}
	if len(msgsOfType[ui.ToastMsg](run(cmd))) != 1 {
		t.Errorf("expected a toast")
	}
}

func TestViewOnce(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	m := findMsg(t, svc, func(m core.Message) bool {
		return m.ViewOnce && !m.ViewOnceOpened && !m.Outgoing && len(m.Attachments) > 0
	})
	v := NewViewOnce(ctx, m)
	if cmd := v.Init(); cmd != nil {
		t.Errorf("view-once must not scan the chat")
	}
	checkView(t, v)
	for _, k := range []string{"y", "s", "o"} {
		_, cmd := v.Update(key(k))
		ts := msgsOfType[ui.ToastMsg](run(cmd))
		if len(ts) != 1 || ts[0].Level != ui.ToastWarning {
			t.Errorf("%s on view-once: %v", k, ts)
		}
	}
	// Navigation is disabled.
	v.Update(key("n"))
	if v.Index() != 0 || v.Len() != 1 {
		t.Errorf("view-once navigated")
	}
	if got, _ := svc.Message(context.Background(), m.ID); got.ViewOnceOpened {
		t.Fatal("marked before close")
	}
	handled, cmd := v.HandleBack()
	if !handled {
		t.Fatal("view-once must handle back to mark the message")
	}
	msgs := run(cmd)
	if len(msgsOfType[ui.CloseOverlayMsg](msgs)) != 1 {
		t.Errorf("back should close: %v", msgs)
	}
	got, err := svc.Message(context.Background(), m.ID)
	if err != nil || !got.ViewOnceOpened {
		t.Fatalf("not marked opened after close: %v %v", got.ViewOnceOpened, err)
	}
	// Marked only once.
	if _, c := v.Update(act(keymap.ActItemSelect)); c != nil {
		t.Errorf("second close marks again")
	}
	// Already viewed / outgoing.
	again := NewViewOnce(ctx, got)
	if out := again.View(60, 20); !strings.Contains(out, "Already viewed") {
		t.Errorf("already viewed: %q", ansi.Strip(out))
	}
	if handled, c := again.HandleBack(); handled || c != nil {
		t.Errorf("already viewed back should let the App close it")
	}
	out := m
	out.Outgoing = true
	if s := ansi.Strip(NewViewOnce(ctx, out).View(80, 20)); !strings.Contains(s, "can't be viewed") {
		t.Errorf("outgoing: %q", s)
	}
}

func TestViewerGIFHalfBlocks(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	m := findMsg(t, svc, hasAtt(isGIF))
	v := NewViewerList(ctx, []Item{{Message: m, Attachment: firstAtt(m, isGIF)}}, 0)
	v.View(80, 24)
	_, cmd := v.Update(ui.ImageReadyMsg{})
	if cmd == nil {
		t.Fatal("no frame loading started")
	}
	frames := msgsOfType[animFramesMsg](run(cmd))
	if len(frames) != 1 || frames[0].err != nil || len(frames[0].frames) < 2 {
		t.Fatalf("frames: %+v", frames)
	}
	_, cmd = v.Update(frames[0])
	if cmd == nil || v.anim == nil || len(v.anim.frames) < 2 {
		t.Fatal("animation not started")
	}
	first := v.View(80, 24)
	ticks := msgsOfType[animTickMsg](run(cmd))
	if len(ticks) != 1 {
		t.Fatalf("ticks: %v", ticks)
	}
	v.Update(ticks[0])
	if v.anim.frame != 1 {
		t.Fatalf("frame %d", v.anim.frame)
	}
	if v.View(80, 24) == first {
		t.Log("frames 0 and 1 render identically (allowed but unexpected)")
	}
	// Pause stops ticking; a stale tick is ignored.
	v.Update(key("a"))
	if _, c := v.Update(ticks[0]); c != nil {
		t.Errorf("stale tick advanced the animation")
	}
	if !strings.Contains(ansi.Strip(v.View(80, 24)), "paused") {
		t.Errorf("paused not shown")
	}
	_, c := v.Update(key("a"))
	if c == nil {
		t.Errorf("resume should tick")
	}
	// Reduce motion: no animation at all.
	ctx.Config.ReduceMotion = true
	rm := NewViewerList(ctx, []Item{{Message: m, Attachment: firstAtt(m, isGIF)}}, 0)
	rm.View(80, 24)
	if _, c := rm.Update(ui.ImageReadyMsg{}); c != nil {
		t.Errorf("reduce motion still animates")
	}
}

func TestViewerGIFKittyReduceMotion(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{kitty: true})
	m := findMsg(t, svc, hasAtt(isGIF))
	it := []Item{{Message: m, Attachment: firstAtt(m, isGIF)}}

	v := NewViewerList(ctx, it, 0)
	v.View(80, 24)
	if _, c := v.Update(ui.ImageReadyMsg{}); c != nil {
		t.Errorf("kitty animates natively; nothing to do: %v", run(c))
	}
	_, c := v.Update(key("a"))
	if c == nil {
		t.Errorf("pausing under kitty should send an animation control")
	}
	ctx.Config.ReduceMotion = true
	rm := NewViewerList(ctx, it, 0)
	rm.View(80, 24)
	if _, c := rm.Update(ui.ImageReadyMsg{}); c == nil {
		t.Errorf("reduce motion should stop kitty's animation")
	}
	if _, c := rm.Update(ui.ImageReadyMsg{}); c != nil {
		t.Errorf("animation stop sent twice")
	}
}

func TestViewerVideo(t *testing.T) {
	svc := newSvc(t)
	imgs := &fakeImages{}
	ctx := newCtx(t, svc, imgs)
	m := findMsg(t, svc, hasAtt(core.Attachment.IsVideo))
	v := NewViewerList(ctx, []Item{{Message: m, Attachment: firstAtt(m, core.Attachment.IsVideo)}}, 0)
	out := ansi.Strip(v.View(80, 24))
	if !strings.Contains(out, "Video") {
		t.Errorf("video placeholder missing:\n%s", out)
	}
	if len(imgs.rowsReq) != 0 {
		t.Errorf("video bytes were requested as an image")
	}
	checkView(t, v)
}

func TestKittyID(t *testing.T) {
	for _, id := range []uint32{5, 42, 300, 0x123456, 0x07000102} {
		rows := (fakeImages{}).kittyRows(id)
		got, ok := kittyID(rows[0])
		if !ok || got != id {
			t.Errorf("id %#x: got %#x %v", id, got, ok)
		}
	}
	if _, ok := kittyID("\x1b[38;2;1;2;3m▀"); !ok {
		t.Log("a colored half-block row parses as an id; callers check isKittyRows first")
	}
	if _, ok := kittyID("plain"); ok {
		t.Error("plain text parsed as kitty row")
	}
}

func TestForSelected(t *testing.T) {
	svc := newSvc(t)
	ctx := newCtx(t, svc, &fakeImages{})
	if ForSelected(ctx, nil) != nil {
		t.Error("nil message")
	}
	photo := findMsg(t, svc, hasAtt(isPhoto))
	if _, ok := ForSelected(ctx, &photo).(*Viewer); !ok {
		t.Error("photo → viewer")
	}
	voice := findMsg(t, svc, hasAtt(func(a core.Attachment) bool { return a.VoiceNote }))
	if _, ok := ForSelected(ctx, &voice).(*PlayerOverlay); !ok {
		t.Error("voice → player")
	}
	vo := findMsg(t, svc, func(m core.Message) bool { return m.ViewOnce })
	if v, ok := ForSelected(ctx, &vo).(*Viewer); !ok || !v.viewOnce {
		t.Error("view once → view-once viewer")
	}
	text := core.Message{Body: "hi"}
	if ForSelected(ctx, &text) != nil {
		t.Error("text → nil")
	}
}

var _ tea.Msg = chatMediaMsg{}
