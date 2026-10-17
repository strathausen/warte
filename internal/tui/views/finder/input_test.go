package finder

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/tui/theme"
)

func TestLineInput(t *testing.T) {
	var in lineInput
	for _, r := range "hello big world" {
		k := tea.KeyPressMsg{Code: r, Text: string(r)}
		if r == ' ' {
			k = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		}
		in.handle(k)
	}
	steps := []struct {
		key  tea.KeyPressMsg
		want string
		pos  int
	}{
		{tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, "hello big ", 10},
		{tea.KeyPressMsg{Code: tea.KeyLeft}, "hello big ", 9},
		{tea.KeyPressMsg{Code: tea.KeyBackspace}, "hello bi ", 8},
		{tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, "hello bi ", 0},
		{tea.KeyPressMsg{Code: tea.KeyDelete}, "ello bi ", 0},
		{tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}, "ello bi ", 8},
		{tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}, "", 0},
	}
	for i, s := range steps {
		in.handle(s.key)
		if in.Value() != s.want || in.pos != s.pos {
			t.Fatalf("step %d (%s): %q@%d, want %q@%d", i, s.key, in.Value(), in.pos, s.want, s.pos)
		}
	}
	if consumed, _ := in.handle(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); consumed {
		t.Error("unknown ctrl key consumed")
	}
	in.insert("a\x1b[2Jb\nc")
	if in.Value() != "ab c" {
		t.Errorf("insert not sanitized: %q", in.Value())
	}
	th, _ := theme.Get("charm")
	st := theme.NewStyles(th)
	in.SetValue(strings.Repeat("long🙂", 30))
	for _, w := range []int{1, 3, 10, 40} {
		if got := ansi.StringWidth(in.view(st, w, "> ", "ph", true, nil)); got > w {
			t.Errorf("view width %d > %d", got, w)
		}
	}
	in.pos = 3
	if v := ansi.Strip(in.view(st, 20, "> ", "", true, nil)); !strings.HasPrefix(v, "> lon") {
		t.Errorf("view does not start at the beginning: %q", v)
	}
}

func TestPasteAndAltEsc(t *testing.T) {
	ctx, _ := testCtx(t)
	o := push(t, NewChatSwitcher(ctx))
	next, _ := o.Update(tea.PasteMsg{Content: "book\n"})
	if chatsOf(next).Query() != "book " || !strings.HasPrefix(firstTitle(chatsOf(next)), "Book club") {
		t.Fatalf("paste: %q", chatsOf(next).Query())
	}
	if n, _ := next.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Mod: tea.ModAlt}); n != nil {
		t.Fatal("alt+esc should close")
	}
}
