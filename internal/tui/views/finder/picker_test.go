package finder

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

var fruits = []string{"apple", "banana", "cherry", "date", "elderberry", "fig", "grape"}

func TestPickerSingle(t *testing.T) {
	ctx, _ := testCtx(t)
	var got []string
	var highlighted []string
	p := NewPicker(ctx, PickerOptions[string]{
		Title:       "Fruit",
		Items:       fruits,
		Filter:      func(s string) string { return s },
		OnSelect:    func(s []string) tea.Cmd { got = s; return nil },
		OnHighlight: func(s string) tea.Cmd { highlighted = append(highlighted, s); return nil },
	})
	o := push(t, p)
	if len(highlighted) != 1 || highlighted[0] != "apple" {
		t.Fatalf("initial highlight = %v", highlighted)
	}
	o = typeText(t, o, "rry")
	if p.Len() != 2 {
		t.Fatalf("matches = %d, want cherry and elderberry", p.Len())
	}
	if !strings.Contains(plain(o, 56, 12), "2/7") {
		t.Errorf("count missing:\n%s", plain(o, 56, 12))
	}
	o, _ = send(t, o, "down")
	cur, _ := p.Current()
	o, _ = send(t, o, "enter")
	if o != nil || len(got) != 1 || got[0] != cur {
		t.Fatalf("selected %v, current %q", got, cur)
	}
	if highlighted[len(highlighted)-1] != cur {
		t.Fatalf("OnHighlight not called on move: %v", highlighted)
	}
}

func TestPickerMulti(t *testing.T) {
	ctx, _ := testCtx(t)
	var got []string
	cancelled := false
	p := NewPicker(ctx, PickerOptions[string]{
		Items:    fruits,
		Multi:    true,
		Filter:   func(s string) string { return s },
		OnSelect: func(s []string) tea.Cmd { got = s; return nil },
		OnCancel: func() tea.Cmd { cancelled = true; return nil },
	})
	o := push(t, p)
	o, _ = send(t, o, "tab")   // apple
	o, _ = send(t, o, "down")  // skip cherry? cursor moved to banana by tab, now cherry
	o, _ = send(t, o, "tab")   // cherry
	o, _ = send(t, o, "esc")   // normal mode
	o, _ = send(t, o, "space") // date
	if p.CheckedCount() != 3 {
		t.Fatalf("checked = %d", p.CheckedCount())
	}
	o, _ = send(t, o, "k")
	o, _ = send(t, o, "space") // untoggle date
	if p.CheckedCount() != 2 {
		t.Fatalf("checked after untoggle = %d", p.CheckedCount())
	}
	view := plain(o, 56, 12)
	if !strings.Contains(view, "●") || !strings.Contains(view, "2 marked") {
		t.Errorf("marks not rendered:\n%s", view)
	}
	// Filtering keeps marks.
	o, _ = send(t, o, "i")
	o = typeText(t, o, "fig")
	if p.CheckedCount() != 2 {
		t.Fatal("filter dropped marks")
	}
	o, _ = send(t, o, "enter")
	if o != nil || strings.Join(got, ",") != "apple,cherry" {
		t.Fatalf("selected %v", got)
	}
	if cancelled {
		t.Fatal("cancel called on accept")
	}

	o = push(t, NewPicker(ctx, PickerOptions[string]{Items: fruits, Filter: func(s string) string { return s },
		OnCancel: func() tea.Cmd { cancelled = true; return nil }}))
	o, _ = send(t, o, "ctrl+c")
	if o != nil || !cancelled {
		t.Fatal("ctrl+c should cancel")
	}
}

func TestPickerViewSizes(t *testing.T) {
	ctx, _ := testCtx(t)
	long := []string{strings.Repeat("wide🙂", 40), "\x1b[31mred\x1b[0m", "x"}
	p := NewPicker(ctx, PickerOptions[string]{
		Items:   long,
		Multi:   true,
		Filter:  func(s string) string { return s },
		Preview: func(s string, w, h int) string { return strings.Repeat(s+"\n", 50) },
	})
	o := push(t, p)
	checkView(t, o)
	if strings.Contains(o.View(80, 20), "\x1b[31mred") {
		t.Fatal("unsanitized item")
	}
	empty := push(t, NewPicker(ctx, PickerOptions[int]{Filter: func(int) string { return "" }}))
	checkView(t, empty)
	if !strings.Contains(plain(empty, 40, 10), "No matches") {
		t.Errorf("empty picker:\n%s", plain(empty, 40, 10))
	}
	if o, _ := send(t, empty, "enter"); o == nil {
		t.Fatal("enter on empty picker closed it")
	}
}

func TestForwardPicker(t *testing.T) {
	ctx, svc := testCtx(t)
	bg := context.Background()
	msgs, err := svc.Messages(bg, core.DirectChatID(aliceACI), core.MessageID{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	var src core.Message
	for _, m := range msgs {
		if m.Body != "" && m.Kind == core.KindNormal && !m.ViewOnce && m.Poll == nil {
			src = m
		}
	}
	p := NewForwardPicker(ctx, src.ID)
	o := push(t, p)
	if p.Len() == 0 {
		t.Fatal("no chats loaded")
	}
	o = typeText(t, o, "book")
	o, _ = send(t, o, "tab")
	o, _ = send(t, o, "ctrl+u")
	o = typeText(t, o, "dev")
	o, _ = send(t, o, "tab")
	checkView(t, o)
	o, out := send(t, o, "enter")
	if o != nil || len(out) != 1 {
		t.Fatalf("closed=%v out=%#v", o == nil, out)
	}
	toast := out[0].(ui.ToastMsg)
	if toast.Level != ui.ToastSuccess || !strings.Contains(toast.Text, "Book club") || !strings.Contains(toast.Text, "Dev Patel") {
		t.Fatalf("toast = %+v", toast)
	}
	chats, _ := svc.Chats(bg, core.ChatFilter{IncludeEmpty: true})
	for _, c := range chats {
		if strings.HasPrefix(c.Title, "Book club") || c.Title == "Dev Patel" {
			last, _ := svc.Messages(bg, c.ID, core.MessageID{}, 1)
			if len(last) != 1 || last[0].Body != src.Body || !last[0].Outgoing {
				t.Errorf("%s: last message %+v, want forwarded %q", c.Title, last, src.Body)
			}
		}
	}

	// Forwarding something that cannot be forwarded reports an error.
	bad := NewForwardPicker(ctx, core.MessageID{Chat: core.DirectChatID(aliceACI), Author: aliceACI, Timestamp: 1})
	o = push(t, bad)
	_, out = send(t, o, "enter")
	if len(out) != 1 || out[0].(ui.ToastMsg).Level != ui.ToastError {
		t.Fatalf("expected error toast, got %#v", out)
	}
}

func TestThemePicker(t *testing.T) {
	ctx, _ := testCtx(t)
	p := NewThemePicker(ctx)
	o := push(t, p)
	if cur, _ := p.Current(); cur != "charm" {
		t.Fatalf("initial = %q", cur)
	}
	w, h := o.Size(156, 44)
	view := plain(o, w, h)
	if !strings.Contains(view, "Ada Lovelace") {
		t.Fatalf("no preview:\n%s", view)
	}
	checkView(t, o)
	o = typeText(t, o, "nord")
	o, out := send(t, o, "enter")
	if o != nil || len(out) != 1 || out[0].(ui.RunCommandMsg).Line != "theme nord" {
		t.Fatalf("out = %#v", out)
	}
}

func TestRegistered(t *testing.T) {
	ctx, _ := testCtx(t)
	for _, c := range []string{"find", "chats", "themes", "forward"} {
		found := false
		for _, cmd := range ui.Commands() {
			found = found || cmd.Name == c
		}
		if !found {
			t.Errorf("command %q not registered", c)
		}
	}
	for _, c := range ui.Commands() {
		if c.Name == "forward" {
			msg := c.Run(ctx, nil)()
			if tm, ok := msg.(ui.ToastMsg); !ok || tm.Level != ui.ToastWarning {
				t.Errorf(":forward without selection = %#v", msg)
			}
		}
		if c.Name == "find" {
			msg := c.Run(ctx, []string{"from:bob", "x"})()
			if pm, ok := msg.(ui.PushOverlayMsg); !ok || pm.Overlay.(*Search).Query() != "from:bob x" {
				t.Errorf(":find = %#v", msg)
			}
		}
	}
}
