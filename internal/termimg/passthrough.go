package termimg

import "strings"

// TmuxPassthrough wraps seq so tmux forwards it verbatim to the outer
// terminal: ESC P tmux ; <seq with every ESC doubled> ESC \.
//
// tmux only honours this with "set -g allow-passthrough on" (tmux 3.3+).
// Kitty Unicode placeholders work through tmux this way: only the image
// transmission is wrapped, while the placeholder cells are ordinary text that
// tmux stores and redraws itself.
func TmuxPassthrough(seq string) string {
	var b strings.Builder
	b.Grow(len(seq) + len(seq)/16 + 10)
	b.WriteString("\x1bPtmux;")
	for i := 0; i < len(seq); i++ {
		if seq[i] == 0x1b {
			b.WriteByte(0x1b)
		}
		b.WriteByte(seq[i])
	}
	b.WriteString("\x1b\\")
	return b.String()
}

// wrap applies tmux passthrough when enabled.
func wrap(seq string, tmux bool) string {
	if !tmux || seq == "" {
		return seq
	}
	return TmuxPassthrough(seq)
}
