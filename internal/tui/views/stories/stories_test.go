package stories

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// stubImages is a ui.Images that draws a box of '#' cells.
type stubImages struct{ calls int }

func (s *stubImages) Enabled() bool { return true }
func (s *stubImages) Inline() bool  { return true }
func (s *stubImages) Size(ref ui.ImageRef, maxCols, maxRows int) (int, int) {
	return max(1, min(maxCols, 20)), max(1, min(maxRows, 10))
}
func (s *stubImages) Rows(ref ui.ImageRef, cols, rows int) ([]string, bool) {
	s.calls++
	out := make([]string, rows)
	for i := range out {
		out[i] = strings.Repeat("#", cols)
	}
	return out, true
}

func newCtx(t *testing.T, themeName string) (ui.Context, *fake.Service) {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Clock: func() time.Time { return testNow }, Latency: -1})
	t.Cleanup(func() { _ = svc.Close() })
	th, ok := theme.Get(themeName)
	if !ok {
		t.Fatalf("theme %q", themeName)
	}
	return ui.Context{
		Svc:    svc,
		Styles: theme.NewStyles(th),
		Icons:  theme.Icons(false),
		Theme:  th,
		Config: config.Config{},
		Images: &stubImages{},
		Now:    func() time.Time { return testNow },
		Chat:   func() core.ChatID { return "" },
		Self:   svc.Self(),
	}, svc
}

// run executes cmd and returns the messages it produces, flattening
// batches. Commands that take longer than a short timeout (ticks) are
// dropped.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(300 * time.Millisecond):
		return nil
	}
	switch m := msg.(type) {
	case nil:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range m {
			out = append(out, run(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// feed sends msg to o and then feeds every resulting message of o's own
// commands back (except toasts/pushes, which are returned).
func feed(t *testing.T, o ui.Overlay, msg tea.Msg) (ui.Overlay, []tea.Msg) {
	t.Helper()
	var app []tea.Msg
	queue := []tea.Msg{msg}
	for len(queue) > 0 && o != nil {
		m := queue[0]
		queue = queue[1:]
		var cmd tea.Cmd
		o, cmd = o.Update(m)
		for _, r := range run(cmd) {
			switch r.(type) {
			case ui.ToastMsg, ui.PushOverlayMsg, ui.CloseOverlayMsg, ui.OpenChatMsg:
				app = append(app, r)
			case tickMsg:
				// don't auto-advance during these tests
			default:
				queue = append(queue, r)
			}
		}
	}
	return o, app
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	}
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func typeText(t *testing.T, o ui.Overlay, s string) ui.Overlay {
	for _, r := range s {
		o, _ = feed(t, o, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return o
}

func loadedList(t *testing.T, ctx ui.Context) *List {
	t.Helper()
	l := NewList(ctx)
	o, _ := feed(t, l, run(l.Init())[0])
	return o.(*List)
}

// checkView renders o the way the App does at a W x H terminal and checks
// that nothing overflows.
func checkView(t *testing.T, name string, o ui.Overlay, W, H int) string {
	t.Helper()
	maxW, maxH := max(W-6, 10), max(H-4, 3)
	cw, ch := o.Size(maxW-4, maxH-2)
	if cw < 1 || ch < 1 || cw > maxW-4 || ch > maxH-2 {
		t.Errorf("%s %dx%d: size %dx%d outside 1..%dx%d", name, W, H, cw, ch, maxW-4, maxH-2)
	}
	cw, ch = max(1, min(cw, maxW-4)), max(1, min(ch, maxH-2))
	out := o.View(cw, ch)
	lines := strings.Split(out, "\n")
	if len(lines) > ch {
		t.Errorf("%s %dx%d: %d lines > %d", name, W, H, len(lines), ch)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > cw {
			t.Errorf("%s %dx%d: line %d is %d wide > %d: %q", name, W, H, i, w, cw, ansi.Strip(l))
		}
	}
	return ansi.Strip(out)
}

var sizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}, {40, 12}}

func TestListGroupsAuthors(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	if !l.loaded || l.err != nil {
		t.Fatalf("not loaded: %v", l.err)
	}
	if len(l.groups) == 0 || !l.groups[0].Self {
		t.Fatalf("self group should come first: %+v", l.groups)
	}
	// Unviewed authors before viewed ones.
	seenViewed := false
	for _, g := range l.groups[1:] {
		if g.unviewed() == 0 {
			seenViewed = true
		} else if seenViewed {
			t.Fatalf("unviewed author %s after viewed ones", g.Name)
		}
	}
	// Initial selection is the first author with something new.
	a, ok := l.selectedAuthor()
	if !ok || a == fake.SelfACI {
		t.Fatalf("selection = %v %v", a, ok)
	}
	out := checkView(t, "list", l, 120, 50)
	for _, want := range []string{"MY STORIES", "New story", "RECENT", "VIEWED", "1 new"} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q:\n%s", want, out)
		}
	}
}

func TestListRingColors(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	var unv, vw *authorGroup
	for i := range l.groups {
		g := &l.groups[i]
		if g.Self {
			continue
		}
		if g.unviewed() > 0 && unv == nil {
			unv = g
		}
		if g.unviewed() == 0 && vw == nil {
			vw = g
		}
	}
	if unv == nil || vw == nil {
		t.Fatal("need viewed and unviewed authors in demo data")
	}
	a := l.authorRow(unv, 50, false, ">")[0]
	b := l.authorRow(vw, 50, false, ">")[0]
	if a == b {
		t.Fatal("unviewed and viewed rings render identically")
	}
}

func TestListNavigationAndOpen(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	l.sel = 0
	var o ui.Overlay = l
	o, _ = feed(t, o, ui.ActionMsg{Action: keymap.ActCursorBottom, Count: 1})
	if l.sel != len(l.entries)-1 {
		t.Fatalf("G: sel=%d", l.sel)
	}
	o, _ = feed(t, o, ui.ActionMsg{Action: keymap.ActCursorTop, Count: 1})
	o, _ = feed(t, o, ui.ActionMsg{Action: keymap.ActCursorDown, Count: 1})
	if l.entries[l.sel].kind != entryNew {
		t.Fatalf("second row should be New story, got %+v", l.entries[l.sel])
	}
	_, msgs := feed(t, o, ui.ActionMsg{Action: keymap.ActItemSelect, Count: 1})
	if len(msgs) != 1 {
		t.Fatalf("enter on New story: %v", msgs)
	}
	if _, ok := msgs[0].(ui.PushOverlayMsg).Overlay.(*Post); !ok {
		t.Fatalf("expected Post overlay, got %T", msgs[0])
	}
	o, _ = feed(t, o, ui.ActionMsg{Action: keymap.ActCursorDown, Count: 1})
	_, msgs = feed(t, o, ui.ActionMsg{Action: keymap.ActItemSelect, Count: 1})
	if len(msgs) != 1 {
		t.Fatalf("enter on author: %v", msgs)
	}
	v, ok := msgs[0].(ui.PushOverlayMsg).Overlay.(*Viewer)
	if !ok {
		t.Fatalf("expected Viewer, got %T", msgs[0])
	}
	g, s := v.current()
	if g == nil || s == nil || g.Author != l.groups[l.entries[l.sel].group].Author {
		t.Fatalf("viewer opened on wrong author")
	}
	_, msgs = feed(t, o, key("c"))
	if len(msgs) != 1 {
		t.Fatalf("c: %v", msgs)
	}
}

func TestListReloadsOnStoriesChanged(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	l := loadedList(t, ctx)
	before := 0
	for _, g := range l.groups {
		before += g.unviewed()
	}
	sts, _ := svc.Stories(context.Background())
	for _, s := range sts {
		_ = svc.MarkStoryViewed(context.Background(), s.ID)
	}
	o, _ := feed(t, l, ui.EventMsg{Event: core.StoriesChanged{}})
	after := 0
	for _, g := range o.(*List).groups {
		after += g.unviewed()
	}
	if before == 0 || after != 0 {
		t.Fatalf("unviewed before=%d after=%d", before, after)
	}
}

func TestViewsFitAllSizes(t *testing.T) {
	for _, th := range []string{"charm", "mono"} {
		ctx, _ := newCtx(t, th)
		l := loadedList(t, ctx)
		empty := NewList(ctx)
		emptyLoaded, _ := feed(t, NewList(ctx), storiesLoadedMsg{})
		post := NewPost(ctx)
		pp := NewPost(ctx)
		postOpen, _ := feed(t, pp, groupsMsg(t, pp))
		media := NewMediaPost(ctx, core.OutgoingAttachment{FileName: "pic.png", ContentType: "image/png", Width: 10, Height: 20})
		noViewer := newViewer(ctx, nil, nil, 0)
		loadingViewer := NewViewer(ctx)
		for _, sz := range sizes {
			W, H := sz[0], sz[1]
			checkView(t, th+"/list", l, W, H)
			checkView(t, th+"/list-empty", empty, W, H)
			checkView(t, th+"/list-empty-loaded", emptyLoaded, W, H)
			checkView(t, th+"/post", post, W, H)
			checkView(t, th+"/post-open", postOpen, W, H)
			checkView(t, th+"/post-media", media, W, H)
			checkView(t, th+"/viewer-empty", noViewer, W, H)
			checkView(t, th+"/viewer-loading", loadingViewer, W, H)
			// Every story of every author.
			for gi := range l.groups {
				v := newViewer(ctx, l.groups, l.titles, gi)
				for si := range v.groups[gi].Stories {
					v.si = si
					checkView(t, fmt.Sprintf("%s/viewer[%d,%d]", th, gi, si), v, W, H)
					v.replying = true
					checkView(t, th+"/viewer-reply", v, W, H)
					v.replying = false
					v.paused = true
					checkView(t, th+"/viewer-paused", v, W, H)
					v.paused = false
				}
			}
		}
		// Tiny sizes must not panic.
		for _, o := range []ui.Overlay{l, empty, post, media, noViewer, newViewer(ctx, l.groups, l.titles, 0)} {
			for w := 1; w < 8; w++ {
				for h := 1; h < 8; h++ {
					_ = o.View(w, h)
				}
			}
		}
	}
}

func TestViewerNavigation(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	// Open on the first unviewed author.
	start := -1
	for i := range l.groups {
		if l.groups[i].unviewed() > 0 {
			start = i
			break
		}
	}
	v := newViewer(ctx, l.groups, l.titles, start)
	run(v.Init())
	if v.gi != start {
		t.Fatalf("gi=%d want %d", v.gi, start)
	}
	var o ui.Overlay = v
	o, _ = feed(t, o, key("j"))
	if v.gi != start+1 || v.si != v.groups[v.gi].firstUnviewed() {
		t.Fatalf("j: gi=%d si=%d", v.gi, v.si)
	}
	o, _ = feed(t, o, key("k"))
	if v.gi != start || v.si != 0 {
		t.Fatalf("k: gi=%d si=%d", v.gi, v.si)
	}
	// l walks through the stories of this author, then to the next author.
	n := len(v.groups[v.gi].Stories)
	for i := 0; i < n; i++ {
		o, _ = feed(t, o, key("l"))
	}
	if v.gi != start+1 {
		t.Fatalf("l past last story: gi=%d", v.gi)
	}
	o, _ = feed(t, o, key("h"))
	if v.gi != start || v.si != n-1 {
		t.Fatalf("h back: gi=%d si=%d", v.gi, v.si)
	}
	// Past the very last story the viewer closes.
	v.gi = len(v.groups) - 1
	v.si = len(v.groups[v.gi].Stories) - 1
	o, _ = feed(t, o, key("l"))
	if o != nil {
		t.Fatal("viewer should close after the last story")
	}
	// esc closes.
	v2 := newViewer(ctx, l.groups, l.titles, 0)
	if o, _ := feed(t, v2, key("esc")); o != nil {
		t.Fatal("esc should close")
	}
	if !v2.CaptureKeys() || v2.KeyContext() != keymap.OverlayStory {
		t.Fatal("viewer key handling")
	}
}

func TestViewerAutoAdvanceAndPause(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	v := newViewer(ctx, l.groups, l.titles, 0)
	v.groups[0].Stories = append(v.groups[0].Stories, v.groups[0].Stories[0])
	v.groups[0].Stories[1].ID.Timestamp++
	v.duration = time.Second
	v.Init()
	gen := v.gen
	var o ui.Overlay = v
	// A stale tick is ignored.
	o, _ = o.Update(tickMsg{v: v, gen: gen - 1, step: time.Hour})
	if v.si != 0 || v.elapsed != 0 {
		t.Fatal("stale tick advanced")
	}
	o, cmd := o.Update(tickMsg{v: v, gen: gen, step: 400 * time.Millisecond})
	if v.elapsed != 400*time.Millisecond || cmd == nil {
		t.Fatalf("tick: elapsed=%v cmd=%v", v.elapsed, cmd != nil)
	}
	// Pause: ticks of the old generation do nothing; no new tick scheduled.
	o, cmd = o.Update(key("space"))
	if !v.paused || cmd != nil {
		t.Fatalf("pause: %v %v", v.paused, cmd != nil)
	}
	if !strings.Contains(checkView(t, "paused", v, 100, 30), "paused") {
		t.Error("paused indicator missing")
	}
	o, _ = o.Update(tickMsg{v: v, gen: gen, step: time.Hour})
	if v.si != 0 {
		t.Fatal("advanced while paused")
	}
	o, cmd = o.Update(key("space"))
	if v.paused || cmd == nil {
		t.Fatal("resume should schedule a tick")
	}
	o, _ = o.Update(tickMsg{v: v, gen: v.gen, step: time.Second})
	if v.si != 1 || v.elapsed != 0 {
		t.Fatalf("auto advance: si=%d elapsed=%v", v.si, v.elapsed)
	}
	_ = o
}

func TestViewerReduceMotion(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	ctx.Config.ReduceMotion = true
	l := loadedList(t, ctx)
	v := newViewer(ctx, l.groups, l.titles, 1)
	v.duration = 50 * time.Millisecond
	var tick tickMsg
	for _, m := range run(v.Init()) {
		if tm, ok := m.(tickMsg); ok {
			tick = tm
		}
	}
	if tick.step != v.storyDuration(nil) && tick.step < v.duration {
		t.Fatalf("reduce motion should tick once per story, step=%v", tick.step)
	}
}

func TestViewerMarksViewed(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	l := loadedList(t, ctx)
	gi := -1
	for i := range l.groups {
		if l.groups[i].unviewed() > 0 {
			gi = i
			break
		}
	}
	v := newViewer(ctx, l.groups, l.titles, gi)
	_, s := v.current()
	id := s.ID
	run(v.Init())
	sts, _ := svc.Stories(context.Background())
	for _, st := range sts {
		if st.ID == id && !st.Viewed {
			t.Fatal("story not marked viewed")
		}
	}
}

func TestViewerReactAndReply(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	l := loadedList(t, ctx)
	gi := -1
	for i := range l.groups {
		if !l.groups[i].Self {
			gi = i
			break
		}
	}
	v := newViewer(ctx, l.groups, l.titles, gi)
	g, s := v.current()
	target := core.DirectChatID(g.Author)
	if s.Group != "" {
		target = s.Group
	}
	var o ui.Overlay = v
	o, msgs := feed(t, o, key("1"))
	if len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastSuccess {
		t.Fatalf("react: %#v", msgs)
	}
	o, _ = feed(t, o, key("r"))
	if !v.replying {
		t.Fatal("r should open the reply input")
	}
	if !strings.Contains(checkView(t, "reply", v, 100, 30), "↩") {
		t.Error("reply prompt not shown")
	}
	o = typeText(t, o, "nice!")
	o, msgs = feed(t, o, key("enter"))
	if v.replying || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastSuccess {
		t.Fatalf("reply: replying=%v %#v", v.replying, msgs)
	}
	ms, err := svc.Messages(context.Background(), target, core.MessageID{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var gotEmoji, gotText bool
	for _, m := range ms {
		if m.Kind == core.KindStoryReply && m.StoryContext != nil {
			if m.StoryContext.Emoji == "❤️" {
				gotEmoji = true
			}
			if m.Body == "nice!" {
				gotText = true
			}
		}
	}
	if !gotEmoji || !gotText {
		t.Fatalf("story replies missing: emoji=%v text=%v", gotEmoji, gotText)
	}
	// esc in the reply input cancels without closing.
	o, _ = feed(t, o, key("r"))
	o, _ = feed(t, o, key("esc"))
	if o == nil || v.replying {
		t.Fatal("esc should only leave the reply input")
	}
}

func TestViewerOwnStoryNoReply(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	v := newViewer(ctx, l.groups, l.titles, 0) // self first
	if g, _ := v.current(); !g.Self {
		t.Fatal("expected own stories first")
	}
	_, msgs := feed(t, v, key("r"))
	if v.replying || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastInfo {
		t.Fatalf("own story reply: %#v", msgs)
	}
	out := checkView(t, "own", v, 100, 30)
	if !strings.Contains(out, "Sent to My Story") || !strings.Contains(out, "replies") {
		t.Errorf("own story bar:\n%s", out)
	}
}

func TestViewerMediaUsesImages(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	l := loadedList(t, ctx)
	imgs := ctx.Images.(*stubImages)
	for gi := range l.groups {
		for si, s := range l.groups[gi].Stories {
			if s.Media == nil {
				continue
			}
			v := newViewer(ctx, l.groups, l.titles, gi)
			v.si = si
			out := checkView(t, "media", v, 120, 40)
			if !strings.Contains(out, "####") {
				t.Fatalf("image rows not drawn:\n%s", out)
			}
			if imgs.calls == 0 {
				t.Fatal("Images.Rows not called")
			}
			return
		}
	}
	t.Fatal("no media story in demo data")
}

func TestViewerLoadsItself(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	v := NewViewer(ctx)
	o, _ := feed(t, v, run(v.Init())[0])
	g, s := o.(*Viewer).current()
	if g == nil || s == nil || g.Self {
		t.Fatalf("NewViewer should start at an unviewed author: %+v", g)
	}
}

func TestPostTextStory(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	p := NewPost(ctx)
	var o ui.Overlay = p
	o, _ = feed(t, o, groupsMsg(t, p))
	if len(p.audience) < 2 {
		t.Fatalf("groups not loaded: %+v", p.audience)
	}
	o = typeText(t, o, "hello ridge")
	o, _ = feed(t, o, key("tab"))
	o, _ = feed(t, o, key("ctrl+t"))
	if p.preset != 1 || p.font != 1 {
		t.Fatalf("preset=%d font=%d", p.preset, p.font)
	}
	out := checkView(t, "post", p, 100, 30)
	if !strings.Contains(out, Presets[1].Name) || !strings.Contains(out, "h e l l o") {
		t.Errorf("post view:\n%s", out)
	}
	o, msgs := feed(t, o, key("enter"))
	if o != nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastSuccess {
		t.Fatalf("post: overlay=%v msgs=%#v", o != nil, msgs)
	}
	sts, _ := svc.Stories(context.Background())
	found := false
	for _, s := range sts {
		if s.ID.Author == fake.SelfACI && s.Text != nil && s.Text.Text == "hello ridge" {
			found = true
			if s.Text.Background != Presets[1].Background || !reflect.DeepEqual(s.Text.Gradient, Presets[1].Gradient) || s.Text.Style != 1 {
				t.Errorf("story colors/style: %+v", s.Text)
			}
		}
	}
	if !found {
		t.Fatal("story not posted")
	}
}

func TestPostAudience(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	p := NewPost(ctx)
	var o ui.Overlay = p
	o, _ = feed(t, o, groupsMsg(t, p))
	o = typeText(t, o, "group only")
	o, _ = feed(t, o, key("esc")) // to audience
	if p.focus != focusAudience {
		t.Fatal("esc should move focus to the audience")
	}
	o, _ = feed(t, o, key("space")) // My Story off
	o, msgs := feed(t, o, key("enter"))
	if o == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastWarning {
		t.Fatalf("empty audience should warn: %#v", msgs)
	}
	o, _ = feed(t, o, key("j"))
	o, _ = feed(t, o, key("x"))
	group := p.audience[1].chat
	o, _ = feed(t, o, key("enter"))
	if o != nil {
		t.Fatal("post should close")
	}
	sts, _ := svc.Stories(context.Background())
	var mine, inGroup int
	for _, s := range sts {
		if s.Text != nil && s.Text.Text == "group only" {
			if s.Group == group {
				inGroup++
			} else {
				mine++
			}
		}
	}
	if inGroup != 1 || mine != 0 {
		t.Fatalf("group=%d mine=%d", inGroup, mine)
	}
}

func TestPostEmptyAndError(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	p := NewPost(ctx)
	o, msgs := feed(t, p, key("enter"))
	if o == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastWarning {
		t.Fatalf("empty post: %#v", msgs)
	}
	o, msgs = feed(t, o, postDoneMsg{p: p, err: fmt.Errorf("story: %w", errorsUnsupported())})
	if o == nil || len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("error should toast and keep the form: %#v", msgs)
	}
	if !strings.Contains(msgs[0].(ui.ToastMsg).Text, "supported") {
		t.Errorf("unsupported message: %q", msgs[0].(ui.ToastMsg).Text)
	}
	if o, _ := feed(t, o, key("ctrl+c")); o != nil {
		t.Fatal("ctrl+c closes")
	}
}

func TestMediaStoryCommand(t *testing.T) {
	ctx, svc := newCtx(t, "charm")
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 9, 16))
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	path := filepath.Join(dir, "my pic.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMedia(filepath.Join(dir, "notes.txt")); err == nil {
		t.Fatal("text file accepted as media")
	}
	msgs := run(runStory(ctx, []string{"post", filepath.Join(dir, "my"), "pic.png"}))
	if len(msgs) != 1 {
		t.Fatalf("post cmd: %#v", msgs)
	}
	p, ok := msgs[0].(ui.PushOverlayMsg).Overlay.(*Post)
	if !ok || p.media == nil || p.media.Width != 9 || p.media.Height != 16 || p.media.ContentType != "image/png" {
		t.Fatalf("media post: %+v", p)
	}
	checkView(t, "media", p, 80, 24)
	var o ui.Overlay = p
	o = typeText(t, o, "caption!")
	o, msgs = feed(t, o, key("enter"))
	if o != nil || len(msgs) != 1 {
		t.Fatalf("media submit: %#v", msgs)
	}
	sts, _ := svc.Stories(context.Background())
	found := false
	for _, s := range sts {
		if s.Media != nil && s.Caption == "caption!" {
			found = true
		}
	}
	if !found {
		t.Fatalf("media story not posted: %#v", msgs)
	}
	// Errors become toasts.
	msgs = run(runStory(ctx, []string{"post", filepath.Join(dir, "missing.png")}))
	if len(msgs) != 1 || msgs[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("missing file: %#v", msgs)
	}
	// Completion.
	c := completeStory(ctx, []string{"post", filepath.Join(dir, "my")})
	if len(c) == 0 || !strings.HasSuffix(c[0], "my pic.png") {
		t.Fatalf("completion: %v", c)
	}
}

func TestRegistration(t *testing.T) {
	if _, ok := ui.ActionHandlerFor(keymap.ActStories); !ok {
		t.Fatal("ActStories not registered")
	}
	found := false
	for _, c := range ui.Commands() {
		if c.Name == "story" {
			found = true
		}
	}
	if !found {
		t.Fatal(":story not registered")
	}
	ctx, _ := newCtx(t, "charm")
	for _, args := range [][]string{nil, {"view"}, {"new"}} {
		msgs := run(runStory(ctx, args))
		if len(msgs) != 1 {
			t.Fatalf("%v: %#v", args, msgs)
		}
		if _, ok := msgs[0].(ui.PushOverlayMsg); !ok {
			t.Fatalf("%v: %#v", args, msgs)
		}
	}
}

func TestRenderTextStory(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	ts := &core.TextStory{Text: "first snow on the ridge ❄️", Style: 2, Foreground: 0xffffffff,
		Gradient: []uint32{0xff3d5a80, 0xff98c1d9}, URL: "https://example.com/x\x1b]0;evil\x07"}
	for _, sz := range [][2]int{{1, 1}, {5, 3}, {20, 6}, {48, 20}, {80, 40}} {
		lines := renderTextStory(ctx.Styles, ctx.Icons, ts, sz[0], sz[1])
		if len(lines) != sz[1] {
			t.Fatalf("%v: %d lines", sz, len(lines))
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != sz[0] {
				t.Fatalf("%v line %d: width %d", sz, i, w)
			}
			if strings.Contains(l, "\x07") || strings.Contains(l, "evil") && strings.Contains(l, "]0;") {
				t.Fatalf("control characters leaked: %q", l)
			}
		}
		if sz[1] >= 6 && lines[0] == lines[len(lines)-1] {
			t.Errorf("%v: gradient rows identical", sz)
		}
	}
	// Short text is drawn large (letter-spaced).
	out := ansi.Strip(strings.Join(renderTextStory(ctx.Styles, ctx.Icons, &core.TextStory{Text: "hi there"}, 40, 10), "\n"))
	if !strings.Contains(out, "h i   t h e r e") {
		t.Errorf("large text:\n%s", out)
	}
	// Long text wraps and truncates.
	long := strings.Repeat("word ", 300)
	lines := renderTextStory(ctx.Styles, ctx.Icons, &core.TextStory{Text: long}, 30, 8)
	if !strings.Contains(ansi.Strip(strings.Join(lines, "")), "…") {
		t.Error("long text should end in an ellipsis")
	}
}

func TestProgressBar(t *testing.T) {
	ctx, _ := newCtx(t, "charm")
	for _, n := range []int{1, 2, 3, 10, 40} {
		for _, w := range []int{1, 10, 30, 64} {
			bar := renderProgress(ctx.Styles, n, n/2, 0.5, w)
			if got := ansi.StringWidth(bar); got > w {
				t.Errorf("n=%d w=%d: width %d", n, w, got)
			}
		}
	}
	if ansi.StringWidth(renderProgress(ctx.Styles, 3, 1, 0.3, 32)) != 32 {
		t.Error("segments should fill the width exactly")
	}
}

func TestColors(t *testing.T) {
	base := color.RGBA{0, 0, 0, 255}
	if r, g, b := rgb8(argb(0xff112233, base)); r != 0x11 || g != 0x22 || b != 0x33 {
		t.Fatalf("argb: %x %x %x", r, g, b)
	}
	if r, _, _ := rgb8(argb(0x80ff0000, base)); r < 0x7f || r > 0x81 {
		t.Fatalf("alpha blend: %x", r)
	}
	if got := readableOn(color.RGBA{0xff, 0xff, 0xff, 0xff}, color.RGBA{0xfe, 0xfe, 0xfe, 0xff}); contrast(got, color.RGBA{0xfe, 0xfe, 0xfe, 0xff}) < 4 {
		t.Fatal("readableOn should pick dark text on white")
	}
	stops := []color.Color{color.RGBA{0, 0, 0, 255}, color.RGBA{200, 200, 200, 255}}
	if r, _, _ := rgb8(gradientAt(stops, 0.5)); r != 100 {
		t.Fatalf("gradient mid: %d", r)
	}
}

func errorsUnsupported() error { return errors.ErrUnsupported }

func groupsMsg(t *testing.T, p *Post) tea.Msg {
	t.Helper()
	for _, m := range run(p.Init()) {
		if g, ok := m.(groupsLoadedMsg); ok {
			return g
		}
	}
	t.Fatal("no groupsLoadedMsg")
	return nil
}
