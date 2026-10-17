package finder

import (
	"strings"
	"testing"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

const aliceACI = core.ACI("a11ce5a1-7c3b-4d2e-8f6a-1b9c0d2e3f41")

func searchOf(o ui.Overlay) *Search { return o.(*Search) }

func TestSearchTyping(t *testing.T) {
	ctx, _ := testCtx(t)
	o := push(t, NewSearch(ctx))
	if !strings.Contains(plain(o, 140, 40), "from:bob") {
		t.Errorf("help not shown for an empty query:\n%s", plain(o, 140, 40))
	}
	checkView(t, o)
	o = typeText(t, o, "the")
	s := searchOf(o)
	if s.searching || len(s.order) == 0 {
		t.Fatalf("no results (searching=%v)", s.searching)
	}
	// Grouped: every chat's hits are contiguous and headers precede them.
	seen := map[core.ChatID]bool{}
	var last core.ChatID
	for _, l := range s.lines {
		if l.header {
			if seen[l.chat] {
				t.Fatalf("chat %s has two groups", l.chat)
			}
			seen[l.chat] = true
			last = l.chat
			continue
		}
		if l.chat != last {
			t.Fatalf("hit outside its group")
		}
	}
	out := plain(o, 150, 44)
	if !strings.Contains(out, "results") || !strings.Contains(out, "Alice Chen") {
		t.Fatalf("view:\n%s", out)
	}
	checkView(t, o)

	// Preview of the first hit was loaded (wide layout).
	first := s.hits[s.order[0]]
	if _, ok := s.preview[first.Message.ID]; !ok {
		t.Fatalf("preview not loaded for first hit")
	}
	if !strings.Contains(out, "│") {
		t.Errorf("no preview pane at 150 cols")
	}
	// Narrow: no preview pane.
	if strings.Contains(plain(o, 80, 24), "│") {
		t.Errorf("preview pane shown at 80 cols")
	}

	// Moving loads the next preview; enter opens the chat focused on the hit.
	o, _ = send(t, o, "down")
	second := s.hits[s.order[1]]
	if _, ok := s.preview[second.Message.ID]; !ok {
		t.Fatalf("preview not loaded after moving")
	}
	o, msgs := send(t, o, "enter")
	if o != nil || len(msgs) != 1 {
		t.Fatalf("enter: closed=%v msgs=%#v", o == nil, msgs)
	}
	open := msgs[0].(ui.OpenChatMsg)
	if open.Chat != second.Message.ID.Chat || open.Focus == nil || *open.Focus != second.Message.ID {
		t.Fatalf("open = %+v", open)
	}
}

func TestSearchDebounceDropsStale(t *testing.T) {
	ctx, _ := testCtx(t)
	o := push(t, NewSearch(ctx))
	s := searchOf(o)
	_, cmd := o.Update(key("x"))
	if cmd == nil || !s.searching {
		t.Fatal("typing should schedule a search")
	}
	s.m.input.insert("y")
	s.seq++ // a newer keystroke
	if _, cmd := s.Update(searchDebounceMsg{s: s, seq: s.seq - 1}); cmd != nil {
		t.Fatal("stale debounce should not search")
	}
	if _, _ = s.Update(searchResultMsg{s: s, seq: s.seq - 1, hits: []core.SearchHit{{}}}); len(s.hits) != 0 {
		t.Fatal("stale results applied")
	}
	// Clearing the query clears results without searching.
	o, _ = send(t, s, "ctrl+u")
	if searchOf(o).searching || len(searchOf(o).hits) != 0 {
		t.Fatal("cleared query should reset")
	}
}

func TestSearchFilters(t *testing.T) {
	ctx, _ := testCtx(t)
	o := push(t, NewSearchQuery(ctx, "from:alice the"))
	s := searchOf(o)
	if len(s.order) == 0 {
		t.Fatal("no results for from:alice")
	}
	for _, h := range s.hits {
		if h.Message.ID.Author != aliceACI {
			t.Fatalf("hit from %s", h.Message.SenderName)
		}
	}
	if !strings.Contains(plain(o, 120, 40), "from Alice Chen") {
		t.Errorf("filter chip missing:\n%s", plain(o, 120, 40))
	}

	o = push(t, NewSearchQuery(ctx, "in:book"))
	s = searchOf(o)
	if len(s.order) == 0 {
		t.Fatal("no results for in:book")
	}
	for _, h := range s.hits {
		if !strings.HasPrefix(h.ChatTitle, "Book club") {
			t.Fatalf("hit in %q", h.ChatTitle)
		}
	}

	o = push(t, NewSearchQuery(ctx, "has:image"))
	for _, h := range searchOf(o).hits {
		img := false
		for _, a := range h.Message.Attachments {
			img = img || a.IsImage()
		}
		if !img {
			t.Fatalf("has:image hit without image")
		}
	}

	o = push(t, NewSearchQuery(ctx, "from:me"))
	for _, h := range searchOf(o).hits {
		if !h.Message.Outgoing {
			t.Fatalf("from:me returned incoming message")
		}
	}

	o = push(t, NewSearchQuery(ctx, "from:zzqqxx hello"))
	s = searchOf(o)
	if len(s.hits) != 0 || len(s.problems) == 0 {
		t.Fatalf("unknown sender should report a problem")
	}
	if !strings.Contains(plain(o, 120, 40), "no contact") {
		t.Errorf("problem not shown:\n%s", plain(o, 120, 40))
	}
	checkView(t, o)

	o = push(t, NewSearchQuery(ctx, "before:2000-01 the"))
	if len(searchOf(o).hits) != 0 || !strings.Contains(plain(o, 120, 40), "No messages found") {
		t.Errorf("before:2000 should find nothing:\n%s", plain(o, 120, 40))
	}

	// in:here uses the open chat.
	ctx.Chat = func() core.ChatID { return core.DirectChatID(aliceACI) }
	o = push(t, NewSearchQuery(ctx, "in:here the"))
	for _, h := range searchOf(o).hits {
		if h.Message.ID.Chat != core.DirectChatID(aliceACI) {
			t.Fatalf("in:here leaked %s", h.ChatTitle)
		}
	}
}

func TestSearchNormalModeAndPreviewToggle(t *testing.T) {
	ctx, _ := testCtx(t)
	o := push(t, NewSearchQuery(ctx, "the"))
	s := searchOf(o)
	o.View(150, 44)
	o, _ = send(t, o, "esc")
	o, _ = send(t, o, "j")
	o, _ = send(t, o, "j")
	if s.sel != 2 || s.Query() != "the" {
		t.Fatalf("sel=%d query=%q", s.sel, s.Query())
	}
	o, _ = send(t, o, "G")
	if s.sel != len(s.order)-1 {
		t.Fatalf("G: sel=%d", s.sel)
	}
	o, _ = send(t, o, "p")
	if s.showPreview || strings.Contains(plain(o, 150, 44), "│") {
		t.Fatal("p should hide the preview")
	}
	checkView(t, o)
	if o, _ = send(t, o, "esc"); o != nil {
		t.Fatal("esc in normal mode should close")
	}
}

func TestSearchEmptyService(t *testing.T) {
	ctx, _ := testCtx(t)
	ctx.Svc = nil
	o := push(t, NewSearch(ctx))
	o = typeText(t, o, "x")
	checkView(t, o)
	if o, _ = send(t, o, "enter"); o == nil {
		t.Fatal("enter without results should not close")
	}
}
