package termimg

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in      string
		want    Protocol
		wantErr bool
	}{
		{"auto", Auto, false},
		{"", Auto, false},
		{"kitty", Kitty, false},
		{"KITTY", Kitty, false},
		{" iterm2 ", ITerm2, false},
		{"iterm", ITerm2, false},
		{"sixel", Sixel, false},
		{"blocks", HalfBlocks, false},
		{"halfblocks", HalfBlocks, false},
		{"off", None, false},
		{"none", None, false},
		{"jpeg", None, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("Parse(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestProtocolStringRoundTrip(t *testing.T) {
	for _, p := range []Protocol{Auto, None, HalfBlocks, Sixel, ITerm2, Kitty} {
		got, err := Parse(p.String())
		if err != nil || got != p {
			t.Errorf("Parse(%v.String()) = %v, %v", p, got, err)
		}
	}
	if Protocol(42).String() != "Protocol(42)" {
		t.Error("unknown protocol string")
	}
	if !ITerm2.Overlay() || !Sixel.Overlay() || Kitty.Overlay() || HalfBlocks.Overlay() {
		t.Error("Overlay classification wrong")
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		want      Protocol
		terminal  string
		tmux      bool
		truecolor bool
		sixelCand bool
	}{
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "1", "TERM": "xterm-256color"}, Kitty, "kitty", false, true, false},
		{"kitty term", map[string]string{"TERM": "xterm-kitty"}, Kitty, "kitty", false, true, false},
		{"ghostty program", map[string]string{"TERM_PROGRAM": "ghostty", "TERM": "xterm-ghostty"}, Kitty, "ghostty", false, true, false},
		{"ghostty resources", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x", "TERM": "xterm-256color"}, Kitty, "ghostty", false, true, false},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm", "TERM": "xterm-256color"}, ITerm2, "wezterm", false, true, false},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app", "TERM": "xterm-256color"}, ITerm2, "iterm2", false, true, false},
		{"iterm lc_terminal", map[string]string{"LC_TERMINAL": "iTerm2", "TERM": "xterm-256color"}, ITerm2, "iterm2", false, true, false},
		{"foot", map[string]string{"TERM": "foot"}, Sixel, "foot", false, true, false},
		{"foot extra", map[string]string{"TERM": "foot-extra"}, Sixel, "foot", false, true, false},
		{"mlterm", map[string]string{"TERM": "mlterm"}, Sixel, "mlterm", false, true, false},
		{"contour", map[string]string{"TERM": "contour"}, Sixel, "contour", false, true, false},
		{"windows terminal", map[string]string{"WT_SESSION": "abc", "TERM": "xterm-256color"}, Sixel, "windows-terminal", false, true, false},
		{"konsole", map[string]string{"KONSOLE_VERSION": "230804", "TERM": "xterm-256color"}, Sixel, "konsole", false, true, false},
		{"xterm", map[string]string{"XTERM_VERSION": "XTerm(390)", "TERM": "xterm"}, HalfBlocks, "xterm", false, false, true},
		{"apple terminal", map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}, HalfBlocks, "apple-terminal", false, false, false},
		{"generic truecolor", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor"}, HalfBlocks, "", false, true, false},
		{"generic direct", map[string]string{"TERM": "xterm-direct"}, HalfBlocks, "", false, true, false},
		{"dumb", map[string]string{"TERM": "dumb"}, None, "", false, false, false},
		{"empty", map[string]string{}, None, "", false, false, false},
		{"tmux in kitty", map[string]string{"TMUX": "/tmp/tmux,1,0", "TERM": "tmux-256color", "TERM_PROGRAM": "tmux", "KITTY_WINDOW_ID": "3"}, Kitty, "kitty", true, true, false},
		{"tmux in ghostty", map[string]string{"TMUX": "x", "TERM": "screen-256color", "GHOSTTY_RESOURCES_DIR": "/x"}, Kitty, "ghostty", true, true, false},
		{"tmux in iterm degrades", map[string]string{"TMUX": "x", "TERM": "tmux-256color", "LC_TERMINAL": "iTerm2"}, HalfBlocks, "iterm2", true, true, false},
		{"tmux in foot degrades", map[string]string{"TMUX": "x", "TERM": "foot"}, HalfBlocks, "foot", true, true, false},
		{"tmux unknown", map[string]string{"TMUX": "x", "TERM": "tmux-256color", "TERM_PROGRAM": "tmux"}, HalfBlocks, "", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Detect(MapEnv(tt.env))
			if d.Protocol != tt.want || d.Terminal != tt.terminal || d.Tmux != tt.tmux || d.TrueColor != tt.truecolor || d.SixelCandidate != tt.sixelCand {
				t.Fatalf("Detect = %+v, want protocol=%v terminal=%q tmux=%v truecolor=%v sixelCandidate=%v",
					d, tt.want, tt.terminal, tt.tmux, tt.truecolor, tt.sixelCand)
			}
		})
	}
	if Detect(nil).Protocol != None {
		t.Error("nil env should detect None")
	}
}

func TestResolve(t *testing.T) {
	blocks := Detection{Protocol: HalfBlocks}
	tests := []struct {
		name       string
		configured Protocol
		d          Detection
		probe      *ProbeResult
		want       Protocol
	}{
		{"forced wins", Sixel, Detection{Protocol: Kitty}, &ProbeResult{Kitty: true}, Sixel},
		{"forced off", None, Detection{Protocol: Kitty}, nil, None},
		{"auto no probe", Auto, Detection{Protocol: ITerm2}, nil, ITerm2},
		{"probe kitty upgrades blocks", Auto, blocks, &ProbeResult{Kitty: true, Sixel: true}, Kitty},
		{"probe sixel upgrades blocks", Auto, blocks, &ProbeResult{Sixel: true}, Sixel},
		{"probe sixel not in tmux", Auto, Detection{Protocol: HalfBlocks, Tmux: true}, &ProbeResult{Sixel: true}, HalfBlocks},
		{"probe kitty keeps wezterm iterm2", Auto, Detection{Protocol: ITerm2, Terminal: "wezterm"}, &ProbeResult{Kitty: true}, ITerm2},
		{"probe negative", Auto, blocks, &ProbeResult{}, HalfBlocks},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Resolve(tt.configured, tt.d, tt.probe); got != tt.want {
				t.Fatalf("Resolve = %v, want %v", got, tt.want)
			}
		})
	}
}
