package keymap

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestFromTea(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.KeyPressMsg
		want string
	}{
		{"letter", tea.KeyPressMsg{Code: 'j', Text: "j"}, "j"},
		{"uppercase text", tea.KeyPressMsg{Code: 'g', ShiftedCode: 'G', Text: "G", Mod: tea.ModShift}, "G"},
		{"shift without text", tea.KeyPressMsg{Code: 'g', Mod: tea.ModShift}, "G"},
		{"punctuation", tea.KeyPressMsg{Code: '?', Text: "?"}, "?"},
		{"plus", tea.KeyPressMsg{Code: '+', Text: "+"}, "+"},
		{"space", tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, "space"},
		{"enter", tea.KeyPressMsg{Code: tea.KeyEnter}, "enter"},
		{"esc", tea.KeyPressMsg{Code: tea.KeyEscape}, "esc"},
		{"tab", tea.KeyPressMsg{Code: tea.KeyTab}, "tab"},
		{"shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, "shift+tab"},
		{"backspace", tea.KeyPressMsg{Code: tea.KeyBackspace}, "backspace"},
		{"up", tea.KeyPressMsg{Code: tea.KeyUp}, "up"},
		{"pgdown", tea.KeyPressMsg{Code: tea.KeyPgDown}, "pgdown"},
		{"ctrl+d", tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "ctrl+d"},
		{"ctrl+w", tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, "ctrl+w"},
		{"alt+x", tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt}, "alt+x"},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, "alt+enter"},
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}, "shift+enter"},
		{"ctrl+shift+a", tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModShift}, "ctrl+shift+a"},
	}
	for _, tt := range tests {
		got := FromTea(tt.msg)
		if got != tt.want {
			t.Errorf("%s: FromTea = %q (String %q), want %q", tt.name, got, tt.msg.String(), tt.want)
		}
		// Every converted key must round-trip through the parser.
		keys, err := ParseKeys(FormatNotation([]string{got}))
		if err != nil || len(keys) != 1 || keys[0] != got {
			t.Errorf("%s: notation round trip of %q = %q, %v", tt.name, got, keys, err)
		}
	}
}

func TestFromTeaDrivesEngine(t *testing.T) {
	e := New()
	msgs := []tea.KeyPressMsg{{Code: 'g', Text: "g"}, {Code: 'g', Text: "g"}}
	var r Result
	for _, m := range msgs {
		r = e.Feed(FromTea(m), Conversation)
	}
	if r.Action != ActCursorTop {
		t.Fatalf("gg via tea = %+v", r)
	}
	if r := e.Feed(FromTea(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}), Conversation); r.Action != ActHalfPageDown {
		t.Fatalf("ctrl+d via tea = %+v", r)
	}
}
