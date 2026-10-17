package keymap

import (
	"reflect"
	"strings"
	"testing"
)

func TestCanonical(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{" ", "space"},
		{"j", "j"},
		{"G", "G"},
		{"+", "+"},
		{"space", "space"},
		{"Space", "space"},
		{"escape", "esc"},
		{"Esc", "esc"},
		{"return", "enter"},
		{"PageUp", "pgup"},
		{"ctrl+d", "ctrl+d"},
		{"Ctrl+D", "ctrl+shift+d"},
		{"control+d", "ctrl+d"},
		{"shift+g", "G"},
		{"shift+G", "G"},
		{"alt+X", "alt+shift+x"},
		{"shift+alt+x", "alt+shift+x"},
		{"shift+ctrl+a", "ctrl+shift+a"},
		{"shift+tab", "shift+tab"},
		{"Shift+Tab", "shift+tab"},
		{"alt+enter", "alt+enter"},
		{"alt+return", "alt+enter"},
		{"ctrl++", "ctrl++"},
		{"alt+ ", "alt+space"},
		{"cmd+k", "super+k"},
		{"f5", "f5"},
		{"F12", "f12"},
		{"hello", "hello"}, // unknown multi-rune text passes through
		{"é", "é"},
	}
	for _, tt := range tests {
		if got := Canonical(tt.in); got != tt.want {
			t.Errorf("Canonical(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseKeys(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "j", want: []string{"j"}},
		{in: "gg", want: []string{"g", "g"}},
		{in: "]u", want: []string{"]", "u"}},
		{in: "dD", want: []string{"d", "D"}},
		{in: "<C-d>", want: []string{"ctrl+d"}},
		{in: "<c-D>", want: []string{"ctrl+d"}},
		{in: "<C-x>", want: []string{"ctrl+x"}},
		{in: "<C-S-a>", want: []string{"ctrl+shift+a"}},
		{in: "<CR>", want: []string{"enter"}},
		{in: "<Enter>", want: []string{"enter"}},
		{in: "<Esc>", want: []string{"esc"}},
		{in: "<Space>", want: []string{"space"}},
		{in: "<Tab>", want: []string{"tab"}},
		{in: "<S-Tab>", want: []string{"shift+tab"}},
		{in: "<A-x>", want: []string{"alt+x"}},
		{in: "<M-x>", want: []string{"alt+x"}},
		{in: "<A-X>", want: []string{"alt+shift+x"}},
		{in: "<A-CR>", want: []string{"alt+enter"}},
		{in: "<S-CR>", want: []string{"shift+enter"}},
		{in: "<S-a>", want: []string{"A"}},
		{in: "<BS>", want: []string{"backspace"}},
		{in: "<PageDown>", want: []string{"pgdown"}},
		{in: "<F5>", want: []string{"f5"}},
		{in: "<leader>m", want: []string{LeaderToken, "m"}},
		{in: "<Leader><LEADER>", want: []string{LeaderToken, LeaderToken}},
		{in: "<C-w>h", want: []string{"ctrl+w", "h"}},
		{in: "<C-w><Left>", want: []string{"ctrl+w", "left"}},
		{in: "<C-]>", want: []string{"ctrl+]"}},
		{in: "<lt>", want: []string{"<"}},
		{in: "<Bar><Bslash>", want: []string{"|", "\\"}},
		{in: "<", want: []string{"<"}},
		{in: "<<", want: []string{"<", "<"}},
		{in: "<>", want: []string{"<", ">"}},
		{in: "a<b", want: []string{"a", "<", "b"}},
		{in: "ctrl+w h", want: []string{"ctrl+w", "h"}},
		{in: "alt+enter", want: []string{"alt+enter"}},
		{in: "g g", want: []string{"g", "g"}},
		{in: "<leader> /", want: []string{LeaderToken, "/"}},
		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "<Foo>", wantErr: true},
		{in: "<X-a>", wantErr: true},
		{in: "<C-Bogus>", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseKeys(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseKeys(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseKeys(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: " ", want: "space"},
		{in: ",", want: ","},
		{in: "\\", want: "\\"},
		{in: "<Space>", want: "space"},
		{in: "space", want: "space"},
		{in: "ctrl+a", want: "ctrl+a"},
		{in: "<C-a>", want: "ctrl+a"},
		{in: "enter", want: "enter"},
		{in: "", wantErr: true},
		{in: "ab", wantErr: true},
		{in: "<C-a>b", wantErr: true},
		{in: "<Nope>", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseKey(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseKey(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatNotation(t *testing.T) {
	tests := []struct {
		keys []string
		want string
	}{
		{[]string{"g", "g"}, "gg"},
		{[]string{"ctrl+d"}, "<C-d>"},
		{[]string{"ctrl+w", "h"}, "<C-w>h"},
		{[]string{LeaderToken, "m"}, "<leader>m"},
		{[]string{"space", "space"}, "<Space><Space>"},
		{[]string{"shift+tab"}, "<S-Tab>"},
		{[]string{"alt+enter"}, "<A-CR>"},
		{[]string{"<"}, "<lt>"},
		{[]string{">"}, ">"},
		{[]string{"f5"}, "<F5>"},
		{[]string{"ctrl+shift+a"}, "<C-S-a>"},
		{[]string{"pgdown"}, "<PageDown>"},
	}
	for _, tt := range tests {
		if got := FormatNotation(tt.keys); got != tt.want {
			t.Errorf("FormatNotation(%q) = %q, want %q", tt.keys, got, tt.want)
		}
	}
}

func TestNotationRoundTrip(t *testing.T) {
	extra := [][]string{
		{"meta+x"}, {"hyper+a", "b"}, {"|", "\\"}, {"ctrl+]"}, {"alt+shift+x"}, {"super+k"},
	}
	var all [][]string
	for _, s := range DefaultSpecs {
		k, err := ParseKeys(s.Keys)
		if err != nil {
			t.Fatalf("ParseKeys(%q): %v", s.Keys, err)
		}
		all = append(all, k)
	}
	all = append(all, extra...)
	for _, keys := range all {
		n := FormatNotation(keys)
		back, err := ParseKeys(n)
		if err != nil {
			t.Errorf("ParseKeys(FormatNotation(%q) = %q): %v", keys, n, err)
			continue
		}
		if !reflect.DeepEqual(back, keys) {
			t.Errorf("round trip %q -> %q -> %q", keys, n, back)
		}
	}
}

func TestDisplayKeys(t *testing.T) {
	tests := []struct {
		keys []string
		want string
	}{
		{nil, ""},
		{[]string{"g", "g"}, "gg"},
		{[]string{"5", "j"}, "5j"},
		{[]string{`"`, "a", "y", "y"}, `"ayy`},
		{[]string{"ctrl+w", "h"}, "ctrl+w h"},
		{[]string{"space", "space"}, "space space"},
		{[]string{"space", "/"}, "space /"},
		{[]string{"3", "ctrl+d"}, "3 ctrl+d"},
	}
	for _, tt := range tests {
		if got := DisplayKeys(tt.keys); got != tt.want {
			t.Errorf("DisplayKeys(%q) = %q, want %q", tt.keys, got, tt.want)
		}
	}
}

func TestSeqKeySeparatorNotInKeys(t *testing.T) {
	for _, s := range DefaultSpecs {
		if strings.Contains(s.Keys, "\x1f") {
			t.Fatalf("default keys %q contain the sequence separator", s.Keys)
		}
	}
}
