package termimg

import "testing"

func TestTmuxPassthrough(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "\x1bPtmux;\x1b\\"},
		{"abc", "\x1bPtmux;abc\x1b\\"},
		{"\x1b_Ga=q;AAAA\x1b\\", "\x1bPtmux;\x1b\x1b_Ga=q;AAAA\x1b\x1b\\\x1b\\"},
		{"\x1b\x1b", "\x1bPtmux;\x1b\x1b\x1b\x1b\x1b\\"},
	}
	for _, tt := range tests {
		if got := TmuxPassthrough(tt.in); got != tt.want {
			t.Errorf("TmuxPassthrough(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if wrap("x", false) != "x" || wrap("", true) != "" || wrap("x", true) != TmuxPassthrough("x") {
		t.Error("wrap")
	}
}
