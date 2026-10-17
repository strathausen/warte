package emoji

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// failSvc wraps a Service and fails the calls the pickers make.
type failSvc struct {
	core.Service
	packs []core.StickerPack
	msg   *core.Message
}

var errBoom = errors.New("boom\x1b[31m\nred")

func (f *failSvc) React(context.Context, core.MessageID, string, bool) error { return errBoom }
func (f *failSvc) Send(context.Context, core.ChatID, core.Outgoing) (core.MessageID, error) {
	return core.MessageID{}, errBoom
}
func (f *failSvc) InstallStickerPack(context.Context, string, string, bool) error { return errBoom }
func (f *failSvc) StickerPacks(context.Context) ([]core.StickerPack, error) {
	if f.packs == nil {
		return nil, errBoom
	}
	return f.packs, nil
}
func (f *failSvc) Message(ctx context.Context, id core.MessageID) (core.Message, error) {
	if f.msg != nil {
		return *f.msg, nil
	}
	return core.Message{}, errBoom
}

func toastsIn(msgs []tea.Msg) []ui.ToastMsg {
	var out []ui.ToastMsg
	for _, m := range msgs {
		if t, ok := m.(ui.ToastMsg); ok {
			out = append(out, t)
		}
	}
	return out
}

func noEscapes(t *testing.T, what, s string) {
	t.Helper()
	if strings.ContainsAny(s, "\x1b\n\r‮") {
		t.Errorf("%s not sanitized: %q", what, s)
	}
}

func TestReviewErrorPaths(t *testing.T) {
	e := newEnv(t, "charm")
	m := reactedMessage(t, e.svc)
	fs := &failSvc{Service: e.svc}
	e.ctx.Svc = fs

	// Reacting fails -> sanitized error toast.
	p := NewReactionPicker(e.ctx, m.ID)
	_, cmd := p.Update(act(keymap.ActItemSelect))
	ts := toastsIn(run(cmd))
	if len(ts) != 1 || ts[0].Level != ui.ToastError {
		t.Fatalf("react error toasts %+v", ts)
	}
	noEscapes(t, "react toast", ts[0].Text)

	// Reactions: load fails with no data -> error view; pre-filled data is kept.
	r := NewReactions(e.ctx, m.ID)
	for _, msg := range run(r.Init()) {
		r.Update(msg)
	}
	if out := checkView(t, "reactions-err", r, 100, 30); !strings.Contains(out, "boom") {
		t.Fatalf("no error shown:\n%s", out)
	}
	r = NewReactionsFor(e.ctx, m)
	for _, msg := range run(r.Init()) {
		_, c := r.Update(msg)
		if len(toastsIn(run(c))) != 1 {
			t.Fatal("refresh failure should toast")
		}
	}
	if r.Total() != len(m.Reactions) {
		t.Fatalf("pre-filled reactions dropped: %d", r.Total())
	}

	// Removing own reaction fails -> toast + rollback via reload.
	own := m
	own.Reactions = append(append([]core.Reaction(nil), m.Reactions...), core.Reaction{Emoji: "🎉", From: e.ctx.Self.ACI})
	fs.msg = &own
	r = NewReactionsFor(e.ctx, own)
	before := r.Total()
	_, cmd = r.Update(key("x"))
	if r.Total() != before-1 {
		t.Fatal("optimistic removal")
	}
	msgs := run(cmd)
	if ts := toastsIn(msgs); len(ts) != 1 {
		t.Fatalf("remove failure toasts %+v", ts)
	}
	for _, msg := range msgs {
		if _, ok := msg.(ui.ToastMsg); ok {
			continue
		}
		_, c := r.Update(msg)
		for _, m2 := range run(c) {
			r.Update(m2)
		}
	}
	if r.Total() != before {
		t.Fatalf("rollback: %d want %d", r.Total(), before)
	}

	// Stickers: load fails -> error view; send/install fail -> toasts.
	s := NewStickers(e.ctx, StickerOptions{Recents: NewRecents(0)})
	for _, msg := range run(s.Init()) {
		s.Update(msg)
	}
	checkView(t, "stickers-err", s, 100, 30)
	fs.packs = []core.StickerPack{{ID: "p", Title: "P", Stickers: []core.Sticker{{PackID: "p", StickerID: 1, Emoji: "😀"}}}}
	s = NewStickers(e.ctx, StickerOptions{Recents: NewRecents(0)})
	for _, msg := range run(s.Init()) {
		s.Update(msg)
	}
	_, cmd = s.Update(key("i"))
	if ts := toastsIn(run(cmd)); len(ts) != 1 || ts[0].Level != ui.ToastError {
		t.Fatalf("install failure toasts %+v", ts)
	}
	s.busy = false
	o, cmd := s.Update(act(keymap.ActItemSelect))
	if o != nil {
		t.Fatal("send closes the picker")
	}
	if ts := toastsIn(run(cmd)); len(ts) != 1 || ts[0].Level != ui.ToastError {
		t.Fatalf("send failure toasts %+v", ts)
	}
}

func TestReviewHostileText(t *testing.T) {
	e := newEnv(t, "charm")
	evil := "\x1b]0;pwn\x07\x1b[2J名前‮漢字漢字漢字漢字漢字漢字漢字漢字漢字漢字\nnext"
	msg := core.Message{ID: reactedMessage(t, e.svc).ID, Reactions: []core.Reaction{
		{Emoji: "👍", From: "a", FromName: evil},
		{Emoji: "\x1b[31m❤️", From: "b", FromName: "Zoë 👩‍👩‍👧‍👦"},
		{Emoji: "漢字漢字漢字", From: "c", FromName: ""},
	}}
	r := NewReactionsFor(e.ctx, msg)
	packs := []core.StickerPack{{ID: "x", Title: evil, Author: evil, Installed: true,
		Stickers: []core.Sticker{{PackID: "x", StickerID: 1, Emoji: evil}, {PackID: "x", StickerID: 2, Emoji: "漢字漢字"}}}}
	s := NewStickers(e.ctx, StickerOptions{Recents: NewRecents(0)})
	s.Update(packsLoadedMsg{packs: packs})
	{
		for _, sz := range sizes {
			for tab := 0; tab < 4; tab++ {
				out := checkView(t, "reactions-hostile", r, sz[0], sz[1])
				for _, bad := range []string{"\x1b]0;", "\x1b[2J", "\x07", "‮", "\x1b[31m❤"} {
					if strings.Contains(out, bad) {
						t.Fatalf("reactions leaked %q", bad)
					}
				}
				r.Update(act(keymap.ActFocusNext))
			}
			out := checkView(t, "stickers-hostile", s, sz[0], sz[1])
			for _, bad := range []string{"\x1b]0;", "\x1b[2J", "\x07", "‮"} {
				if strings.Contains(out, bad) {
					t.Fatalf("stickers leaked %q", bad)
				}
			}
		}
	}
	e.ctx.Images = nil
	s2 := NewStickers(e.ctx, StickerOptions{Recents: NewRecents(0)})
	s2.Update(packsLoadedMsg{packs: packs})
	for _, sz := range sizes {
		checkView(t, "stickers-hostile-noimg", s2, sz[0], sz[1])
	}
	// Picker query with control chars / wide runes.
	p := NewPicker(e.ctx, Options{Purpose: PurposeInsert, Query: "漢字\x1b[31m" + strings.Repeat("x", 100)})
	for _, sz := range sizes {
		out := checkView(t, "picker-hostile", p, sz[0], sz[1])
		if strings.Contains(out, "\x1b[31m") && strings.Contains(out, "漢字\x1b[31m") {
			t.Fatal("query leaked escape")
		}
	}
}

func TestReviewStickerClickBelowGrid(t *testing.T) {
	e := newEnv(t, "charm")
	var st []core.Sticker
	for i := range 40 {
		st = append(st, core.Sticker{PackID: "p", StickerID: uint32(i), Emoji: "😀"})
	}
	sent := 0
	s := NewStickers(e.ctx, StickerOptions{Recents: NewRecents(0), OnSend: func(core.ChatID, core.Sticker) tea.Cmd { sent++; return nil }})
	s.Update(packsLoadedMsg{packs: []core.StickerPack{{ID: "p", Title: "P", Installed: true, Stickers: st}}})
	w, h := 56, 12 // 2 rows of stickers visible, a few lines of padding
	_ = s.View(w, h)
	y := s.gridTop + s.rows*stickerCellH // first line below the visible rows
	if y >= h {
		t.Skip("no padding at this size")
	}
	if o, _ := s.Update(tea.MouseClickMsg{X: 1, Y: y}); o == nil || sent != 0 {
		t.Fatal("click below the visible grid sent a sticker")
	}
	if o, _ := s.Update(tea.MouseClickMsg{X: 1, Y: s.gridTop}); o != nil || sent != 1 {
		t.Fatal("click on the first sticker should send")
	}
}
