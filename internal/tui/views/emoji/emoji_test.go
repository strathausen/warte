package emoji

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/fake"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// stubImages is a ui.Images that returns solid rows and records requests.
type stubImages struct {
	calls []ui.ImageRef
	sizes [][2]int
}

func (s *stubImages) Enabled() bool { return true }
func (s *stubImages) Inline() bool  { return true }
func (s *stubImages) Size(ref ui.ImageRef, maxCols, maxRows int) (int, int) {
	return maxCols, maxRows
}

func (s *stubImages) Rows(ref ui.ImageRef, cols, rows int) ([]string, bool) {
	s.calls = append(s.calls, ref)
	s.sizes = append(s.sizes, [2]int{cols, rows})
	out := make([]string, rows)
	for i := range out {
		out[i] = strings.Repeat("▀", cols)
	}
	return out, true
}

type env struct {
	svc    *fake.Service
	ctx    ui.Context
	images *stubImages
	chat   core.ChatID
	sel    *core.Message
}

func newEnv(t *testing.T, themeName string) *env {
	t.Helper()
	svc := fake.New(fake.Options{Seed: 1, Latency: -1, Clock: func() time.Time { return testNow }})
	t.Cleanup(func() { _ = svc.Close() })
	th, ok := theme.Get(themeName)
	if !ok {
		t.Fatalf("theme %q", themeName)
	}
	e := &env{svc: svc, images: &stubImages{}, chat: fake.ChatNoteToSelf}
	e.ctx = ui.Context{
		Svc:      svc,
		Styles:   theme.NewStyles(th),
		Icons:    theme.Icons(false),
		Theme:    th,
		Images:   e.images,
		Keys:     keymap.New(),
		Now:      func() time.Time { return testNow },
		Chat:     func() core.ChatID { return e.chat },
		Selected: func() *core.Message { return e.sel },
		Self:     svc.Self(),
	}
	return e
}

// reactedMessage finds a demo message with at least two different emojis.
func reactedMessage(t *testing.T, svc *fake.Service) core.Message {
	t.Helper()
	ctx := context.Background()
	chats, err := svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chats {
		msgs, _ := svc.Messages(ctx, c.ID, core.MessageID{}, 500)
		for _, m := range msgs {
			if m.Kind != core.KindNormal || m.RemoteDeleted {
				continue
			}
			seen := map[string]bool{}
			for _, r := range m.Reactions {
				seen[r.Emoji] = true
			}
			if len(seen) >= 2 {
				return m
			}
		}
	}
	t.Fatal("no message with reactions in demo data")
	return core.Message{}
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

func act(a keymap.Action) ui.ActionMsg { return ui.ActionMsg{Action: a, Count: 1} }

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func typeText(o ui.Overlay, s string) ui.Overlay {
	for _, r := range s {
		o, _ = o.Update(key(string(r)))
	}
	return o
}

// checkView renders o at the size the App would give it and checks bounds.
func checkView(t *testing.T, name string, o ui.Overlay, W, H int) string {
	t.Helper()
	maxW, maxH := max(W-6, 10)-4, max(H-4, 3)-2
	w, h := o.Size(maxW, maxH)
	w, h = max(min(w, maxW), 1), max(min(h, maxH), 1)
	out := o.View(w, h)
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("%s at %dx%d: %d lines > %d", name, W, H, len(lines), h)
	}
	for i, l := range lines {
		if lw := width(l); lw > w {
			t.Errorf("%s at %dx%d: line %d is %d cells > %d: %q", name, W, H, i, lw, w, l)
		}
	}
	return out
}

var sizes = [][2]int{{60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 48}, {200, 60}, {20, 8}, {12, 5}}

func TestViewsFitAllSizes(t *testing.T) {
	for _, th := range []string{"charm", "mono", "charm-light"} {
		e := newEnv(t, th)
		m := reactedMessage(t, e.svc)
		picker := NewReactionPicker(e.ctx, m.ID)
		searching := NewInsertPicker(e.ctx)
		searching.Update(act(keymap.ActSearchForward))
		typeText(searching, "heart")
		empty := NewInsertPicker(e.ctx)
		empty.Update(act(keymap.ActSearchForward))
		typeText(empty, "zzzzqqq")
		reactions := NewReactions(e.ctx, m.ID)
		for _, msg := range run(reactions.Init()) {
			reactions.Update(msg)
		}
		stickers := NewStickers(e.ctx, StickerOptions{})
		for _, msg := range run(stickers.Init()) {
			stickers.Update(msg)
		}
		views := map[string]ui.Overlay{
			"picker":          picker,
			"picker-search":   searching,
			"picker-empty":    empty,
			"reactions":       reactions,
			"reactions-empty": NewReactionsFor(e.ctx, core.Message{}),
			"reactions-load":  NewReactions(e.ctx, m.ID),
			"stickers":        stickers,
			"stickers-load":   NewStickers(e.ctx, StickerOptions{}),
		}
		for tab := 0; tab < 10; tab++ {
			for name, v := range views {
				for _, sz := range sizes {
					checkView(t, th+"/"+name, v, sz[0], sz[1])
				}
			}
			picker.Update(act(keymap.ActFocusNext))
			reactions.Update(act(keymap.ActFocusNext))
			stickers.Update(act(keymap.ActFocusNext))
		}
		// Degenerate sizes must not panic.
		for _, v := range views {
			for _, s := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {5, 10}} {
				_ = v.View(s[0], s[1])
			}
		}
	}
}

func TestPickerCategoriesAndNavigation(t *testing.T) {
	e := newEnv(t, "charm")
	p := NewInsertPicker(e.ctx)
	if p.tab != 0 || p.Selected() == "" {
		t.Fatalf("starts on recents with defaults, got tab %d sel %q", p.tab, p.Selected())
	}
	p.Update(act(keymap.ActFocusNext)) // Smileys
	if p.tab != 1 {
		t.Fatalf("tab = %d", p.tab)
	}
	first := p.Selected()
	if first != format.EmojisInCategory(format.CategorySmileys)[0].Char {
		t.Fatalf("first smiley %q", first)
	}
	checkView(t, "picker", p, 80, 24) // layout: cols known
	cols := p.cols
	p.Update(act(keymap.ActCursorDown))
	if p.sel != cols {
		t.Fatalf("down moves a row: sel=%d cols=%d", p.sel, cols)
	}
	p.Update(act(keymap.ActCursorRight))
	p.Update(act(keymap.ActCursorUp))
	if p.sel != 1 {
		t.Fatalf("up: sel=%d", p.sel)
	}
	p.Update(act(keymap.ActCursorUp))
	if p.sel != 1 {
		t.Fatalf("up at top keeps column: sel=%d", p.sel)
	}
	p.Update(act(keymap.ActCursorBottom))
	if p.sel != len(p.items)-1 {
		t.Fatalf("G: sel=%d of %d", p.sel, len(p.items))
	}
	out := checkView(t, "picker", p, 80, 24)
	if !strings.Contains(out, p.Selected()) {
		t.Fatalf("selected emoji scrolled out of view")
	}
	p.Update(act(keymap.ActFocusPrev))
	p.Update(act(keymap.ActFocusPrev))
	if p.tab != len(format.Categories()) {
		t.Fatalf("wraps to last tab, got %d", p.tab)
	}
	// Tab grid width adapts.
	narrow := NewInsertPicker(e.ctx)
	narrow.View(20, 10)
	wide := NewInsertPicker(e.ctx)
	wide.View(60, 10)
	if narrow.cols != 5 || wide.cols != 15 {
		t.Fatalf("cols narrow=%d wide=%d", narrow.cols, wide.cols)
	}
}

func TestPickerSearchInsert(t *testing.T) {
	e := newEnv(t, "charm")
	rec := NewRecents(8)
	var got []PickedMsg
	p := NewPicker(e.ctx, Options{Purpose: PurposeInsert, Recents: rec})
	p.Update(act(keymap.ActSearchForward))
	if !p.CaptureKeys() {
		t.Fatal("search should capture keys")
	}
	typeText(p, "thumbs up")
	if p.Selected() != "👍" {
		t.Fatalf("search result %q (%d items)", p.Selected(), len(p.items))
	}
	out := checkView(t, "search", p, 80, 24)
	if !strings.Contains(out, "thumbs up") {
		t.Fatalf("query not shown:\n%s", out)
	}
	o, cmd := p.Update(key("enter"))
	if o != nil {
		t.Fatal("enter should close")
	}
	for _, m := range run(cmd) {
		ins, ok := m.(ui.ComposeInsertMsg)
		if !ok || ins.Text != "👍" {
			t.Fatalf("got %#v", m)
		}
	}
	if r := rec.Emojis(); len(r) != 1 || r[0] != "👍" {
		t.Fatalf("recents %v", r)
	}

	// OnPick receives PickedMsg.
	p = NewPicker(e.ctx, Options{Purpose: "custom", Recents: rec, OnPick: func(m PickedMsg) tea.Cmd {
		got = append(got, m)
		return nil
	}})
	if p.Selected() != "👍" {
		t.Fatalf("recent first: %q", p.Selected())
	}
	p.Update(act(keymap.ActItemSelect))
	if len(got) != 1 || got[0].Emoji != "👍" || got[0].Purpose != "custom" {
		t.Fatalf("OnPick %v", got)
	}
}

func TestPickerSearchEditingAndBack(t *testing.T) {
	e := newEnv(t, "charm")
	p := NewPicker(e.ctx, Options{Purpose: PurposeInsert, Recents: NewRecents(4)})
	p.Update(act(keymap.ActFocusNext))
	p.Update(key("/"))
	typeText(p, "catx")
	p.Update(key("backspace"))
	if p.Query() != "cat" {
		t.Fatalf("query %q", p.Query())
	}
	if len(p.items) == 0 {
		t.Fatal("no results for cat")
	}
	p.Update(key("down"))
	p.Update(key("tab")) // leave typing, keep results
	if p.CaptureKeys() || !p.search {
		t.Fatal("tab should leave typing but keep the filter")
	}
	if handled, _ := p.HandleBack(); !handled || p.search {
		t.Fatal("back clears the search first")
	}
	if p.tab != 1 {
		t.Fatalf("tab restored: %d", p.tab)
	}
	if handled, _ := p.HandleBack(); handled {
		t.Fatal("second back closes")
	}
	// esc with an empty query leaves search mode.
	p.Update(act(keymap.ActSearchForward))
	p.Update(key("esc"))
	if p.search || p.typing {
		t.Fatal("esc on empty query ends search")
	}
	// Prefilled query.
	q := NewPicker(e.ctx, Options{Query: ":joy"})
	if q.Selected() != "😂" {
		t.Fatalf(":joy → %q", q.Selected())
	}
}

func TestPickerSkinTone(t *testing.T) {
	e := newEnv(t, "charm")
	rec := NewRecents(4)
	p := NewPicker(e.ctx, Options{Purpose: PurposeInsert, Recents: rec, Query: "waving hand"})
	if p.items[p.sel].base != "👋" {
		t.Fatalf("got %q", p.items[p.sel].base)
	}
	p.Update(key("tab"))
	p.Update(key("t"))
	if rec.SkinTone() != format.SkinToneLight {
		t.Fatalf("tone %x", rec.SkinTone())
	}
	if want := format.WithSkinTone("👋", format.SkinToneLight); p.Selected() != want {
		t.Fatalf("selected %q want %q", p.Selected(), want)
	}
	p.Update(key("T"))
	p.Update(key("T"))
	if rec.SkinTone() != format.SkinToneDark {
		t.Fatalf("reverse cycle %x", rec.SkinTone())
	}
	// Non-supporting emoji unchanged.
	p2 := NewPicker(e.ctx, Options{Recents: rec, Query: "joy"})
	if p2.Selected() != "😂" {
		t.Fatalf("joy toned: %q", p2.Selected())
	}
}

func TestPickerReact(t *testing.T) {
	e := newEnv(t, "charm")
	m := reactedMessage(t, e.svc)
	rec := NewRecents(4)
	p := NewPicker(e.ctx, Options{Purpose: PurposeReact, Target: m.ID, Recents: rec, Query: "fire"})
	if p.Selected() != "🔥" {
		t.Fatalf("fire → %q", p.Selected())
	}
	o, cmd := p.Update(key("enter"))
	if o != nil {
		t.Fatal("should close")
	}
	if msgs := run(cmd); len(msgs) != 0 {
		t.Fatalf("unexpected %v", msgs)
	}
	own := func() string {
		got, _ := e.svc.Message(context.Background(), m.ID)
		for _, r := range got.Reactions {
			if r.From == fake.SelfACI {
				return r.Emoji
			}
		}
		return ""
	}
	if own() != "🔥" {
		t.Fatalf("reaction not applied: %q", own())
	}
	// Picking the same emoji again removes it (Signal toggles).
	run(Handle(e.ctx, PickedMsg{Emoji: "🔥", Purpose: PurposeReact, Target: m.ID}))
	if own() != "" {
		t.Fatalf("not removed: %q", own())
	}
	// Errors become toasts.
	bad := m.ID
	bad.Timestamp = 1
	msgs := run(Handle(e.ctx, PickedMsg{Emoji: "🔥", Purpose: PurposeReact, Target: bad}))
	if len(msgs) != 1 {
		t.Fatalf("want toast, got %v", msgs)
	}
	if tm, ok := msgs[0].(ui.ToastMsg); !ok || tm.Level != ui.ToastError {
		t.Fatalf("got %#v", msgs[0])
	}
	msgs = run(Handle(e.ctx, PickedMsg{Emoji: "🔥", Purpose: PurposeReact}))
	if tm, ok := msgs[0].(ui.ToastMsg); !ok || tm.Level != ui.ToastWarning {
		t.Fatalf("zero target: %#v", msgs)
	}
}

func TestPickerMouse(t *testing.T) {
	e := newEnv(t, "charm")
	var got string
	p := NewPicker(e.ctx, Options{OnPick: func(m PickedMsg) tea.Cmd { got = m.Emoji; return nil }})
	p.View(60, 16)
	// Click the third tab (Animals is tab index 3: recents, smileys, people).
	sp := p.spans[3]
	p.Update(tea.MouseClickMsg{X: sp.x + 1, Y: 0})
	if p.tab != 3 {
		t.Fatalf("tab %d", p.tab)
	}
	p.View(60, 16)
	want := p.items[1].char
	o, _ := p.Update(tea.MouseClickMsg{X: cellW + 1, Y: p.gridTop})
	if o != nil || got != want {
		t.Fatalf("click picked %q want %q", got, want)
	}
}

func TestReactionsOverlay(t *testing.T) {
	e := newEnv(t, "charm")
	m := reactedMessage(t, e.svc)
	if err := e.svc.React(context.Background(), m.ID, "🎉", false); err != nil {
		t.Fatal(err)
	}
	r := NewReactions(e.ctx, m.ID)
	for _, msg := range run(r.Init()) {
		r.Update(msg)
	}
	if !r.loaded || len(r.groups) < 3 {
		t.Fatalf("groups %d", len(r.groups))
	}
	out := checkView(t, "reactions", r, 100, 30)
	if !strings.Contains(out, "All ") || !strings.Contains(out, "You") || !strings.Contains(out, "🎉") {
		t.Fatalf("view:\n%s", out)
	}
	total := r.Total()
	// Tab through emoji groups.
	r.Update(act(keymap.ActFocusNext))
	if r.tab != 1 || len(r.current()) != len(r.groups[0].list) {
		t.Fatalf("tab 1: %d", len(r.current()))
	}
	r.Update(act(keymap.ActCursorLeft))
	if r.tab != 0 || len(r.current()) != total {
		t.Fatal("back to all")
	}
	// Enter on someone else's row does nothing.
	self := e.ctx.Self.ACI
	for i, x := range r.current() {
		if x.From != self {
			r.sel = i
			break
		}
	}
	if _, cmd := r.Update(act(keymap.ActItemSelect)); cmd != nil {
		t.Fatal("enter on other's reaction should be a no-op")
	}
	// x removes own reaction.
	_, cmd := r.Update(key("x"))
	if r.Total() != total-1 {
		t.Fatalf("optimistic removal: %d", r.Total())
	}
	if msgs := run(cmd); len(msgs) != 0 {
		t.Fatalf("unexpected %v", msgs)
	}
	got, _ := e.svc.Message(context.Background(), m.ID)
	for _, x := range got.Reactions {
		if x.From == self {
			t.Fatal("own reaction still there")
		}
	}
	// Second x: nothing to remove.
	_, cmd = r.Update(key("x"))
	if msgs := run(cmd); len(msgs) != 1 {
		t.Fatalf("want info toast, got %v", msgs)
	}
	// Events refresh the view.
	got.Reactions = append(got.Reactions, core.Reaction{Emoji: "🦄", From: "x", FromName: "Uni\x1b[31mcorn"})
	r.Update(ui.EventMsg{Event: core.MessageUpdated{Message: got}})
	out = checkView(t, "reactions", r, 100, 30)
	if !strings.Contains(out, "🦄") || strings.Contains(out, "\x1b[31mcorn") {
		t.Fatalf("event not applied or not sanitized:\n%s", out)
	}
	r.Update(ui.EventMsg{Event: core.MessageRemoved{ID: m.ID}})
	if out := checkView(t, "reactions", r, 100, 30); !strings.Contains(out, "deleted") {
		t.Fatalf("removed:\n%s", out)
	}
	// "+" opens the reaction picker.
	r2 := NewReactionsFor(e.ctx, m)
	_, cmd = r2.Update(key("+"))
	msgs := run(cmd)
	if len(msgs) != 1 {
		t.Fatalf("got %v", msgs)
	}
	if po, ok := msgs[0].(ui.PushOverlayMsg); !ok || po.Overlay.(*Picker).opts.Target != m.ID {
		t.Fatalf("got %#v", msgs[0])
	}
}

func TestStickers(t *testing.T) {
	e := newEnv(t, "charm")
	rec := NewRecents(8)
	s := NewStickers(e.ctx, StickerOptions{Recents: rec})
	for _, msg := range run(s.Init()) {
		s.Update(msg)
	}
	if len(s.packs) < 2 || !s.packs[0].Installed {
		t.Fatalf("installed pack first: %+v", s.packs)
	}
	out := checkView(t, "stickers", s, 100, 30)
	if !strings.Contains(out, "Trail Critters") || !strings.Contains(out, "installed") {
		t.Fatalf("view:\n%s", out)
	}
	if len(e.images.calls) == 0 {
		t.Fatal("no sticker images requested")
	}
	for i, ref := range e.images.calls {
		if ref.Kind != ui.ImageSticker || ref.PackID == "" {
			t.Fatalf("ref %+v", ref)
		}
		if sz := e.images.sizes[i]; sz[0] > stickerW || sz[1] > stickerH {
			t.Fatalf("size %v", sz)
		}
	}
	// Navigate and send.
	s.Update(act(keymap.ActCursorRight))
	want, _ := s.SelectedSticker()
	o, cmd := s.Update(act(keymap.ActItemSelect))
	if o != nil {
		t.Fatal("send closes")
	}
	if msgs := run(cmd); len(msgs) != 0 {
		t.Fatalf("send: %v", msgs)
	}
	msgs, _ := e.svc.Messages(context.Background(), e.chat, core.MessageID{}, 1)
	if len(msgs) == 0 || msgs[0].Sticker == nil || msgs[0].Sticker.StickerID != want.StickerID {
		t.Fatalf("sticker not sent: %+v", msgs)
	}
	if r := rec.Stickers(); len(r) != 1 || r[0].StickerID != want.StickerID {
		t.Fatalf("recents %v", r)
	}

	// Reopen: recent tab first; install the other pack.
	s = NewStickers(e.ctx, StickerOptions{Recents: rec})
	for _, msg := range run(s.Init()) {
		s.Update(msg)
	}
	if s.pack() != nil || len(s.items()) != 1 {
		t.Fatal("recent tab first")
	}
	if _, cmd := s.Update(key("i")); cmd != nil {
		t.Fatal("i on recents is a no-op")
	}
	s.Update(act(keymap.ActFocusPrev)) // wraps to the last (uninstalled) pack
	p := s.pack()
	if p == nil || p.Installed {
		t.Fatalf("pack %+v", p)
	}
	id := p.ID
	_, cmd = s.Update(key("i"))
	var toasts int
	for _, msg := range run(cmd) {
		if _, ok := msg.(ui.ToastMsg); ok {
			toasts++
			continue
		}
		_, c2 := s.Update(msg)
		for _, m3 := range run(c2) {
			if _, ok := m3.(ui.ToastMsg); ok {
				toasts++
			}
			s.Update(m3)
		}
	}
	if toasts != 1 {
		t.Fatalf("toasts %d", toasts)
	}
	if p := s.pack(); p == nil || p.ID != id || !p.Installed {
		t.Fatalf("after install: %+v", p)
	}

	// No chat open.
	e.chat = ""
	s.sel = 0
	if o, cmd := s.Update(act(keymap.ActItemSelect)); o == nil || cmd == nil {
		t.Fatal("no chat: stay open and warn")
	}
}

func TestStickersWithoutImages(t *testing.T) {
	e := newEnv(t, "mono")
	e.ctx.Images = nil
	s := NewStickers(e.ctx, StickerOptions{Recents: NewRecents(2)})
	for _, msg := range run(s.Init()) {
		s.Update(msg)
	}
	out := checkView(t, "stickers", s, 80, 24)
	if !strings.Contains(out, s.packs[0].Stickers[0].Emoji) {
		t.Fatalf("emoji fallback missing:\n%s", out)
	}
}

func TestRecents(t *testing.T) {
	r := NewRecents(3)
	changes := 0
	r.OnChange(func() { changes++ })
	for _, e := range []string{"a", "b", "a", "c", "d", "a"} {
		r.AddEmoji(e)
	}
	if got := strings.Join(r.Emojis(), ""); got != "adc" {
		t.Fatalf("mru %q", got)
	}
	if got := r.Frequent(1); len(got) != 1 || got[0] != "a" || r.Count("a") != 3 {
		t.Fatalf("frequent %v", got)
	}
	if changes != 6 {
		t.Fatalf("changes %d", changes)
	}
	r.SetEmojis([]string{"x", "y", "z", "w"})
	if got := strings.Join(r.Emojis(), ""); got != "xyz" {
		t.Fatalf("set %q", got)
	}
	for i := 0; i < 5; i++ {
		r.AddSticker(core.Sticker{PackID: "p", StickerID: uint32(i % 4)})
	}
	if s := r.Stickers(); len(s) != 3 || s[0].StickerID != 0 || s[1].StickerID != 3 {
		t.Fatalf("stickers %+v", s)
	}
	if NextSkinTone(format.SkinToneDark, 1) != format.SkinToneNone || NextSkinTone(format.SkinToneNone, -1) != format.SkinToneDark {
		t.Fatal("tone cycle")
	}
}

func TestRegisteredHooks(t *testing.T) {
	e := newEnv(t, "charm")
	h, ok := ui.ActionHandlerFor(ui.ActReactionPicker)
	if !ok {
		t.Fatal("reaction picker action not registered")
	}
	if msgs := run(h(e.ctx)); len(msgs) != 1 {
		t.Fatalf("no selection → toast, got %v", msgs)
	} else if _, ok := msgs[0].(ui.ToastMsg); !ok {
		t.Fatalf("got %#v", msgs[0])
	}
	m := reactedMessage(t, e.svc)
	e.sel = &m
	msgs := run(h(e.ctx))
	if po, ok := msgs[0].(ui.PushOverlayMsg); !ok || po.Overlay.Title() != "React" {
		t.Fatalf("got %#v", msgs[0])
	}
	names := map[string]bool{}
	for _, c := range ui.Commands() {
		names[c.Name] = true
	}
	for _, n := range []string{"emoji", "stickers", "reactions"} {
		if !names[n] {
			t.Errorf("command %s not registered", n)
		}
	}
	msgs = run(OpenReactions(e.ctx))
	if po, ok := msgs[0].(ui.PushOverlayMsg); !ok || po.Overlay.Title() != "Reactions" {
		t.Fatalf("got %#v", msgs[0])
	}
	msgs = run(OpenStickers(e.ctx))
	if _, ok := msgs[0].(ui.PushOverlayMsg); !ok {
		t.Fatalf("got %#v", msgs[0])
	}
}

func TestEmojiCellWidths(t *testing.T) {
	// Every emoji must fit its grid cell, or the grid would misalign.
	for _, e := range format.AllEmojis() {
		for _, tone := range SkinTones {
			c := format.WithSkinTone(e.Char, tone)
			if w := width(c); w < 1 || w > cellW-1 {
				t.Errorf("%q (%s) is %d cells", c, e.Name, w)
			}
		}
	}
}
