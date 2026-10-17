package finder

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/tui/theme"
)

// lineInput is a minimal single-line text field with readline keys. It is
// deliberately tiny (no blinking cursor, no suggestions) so the finder views
// fully control rendering and key routing.
type lineInput struct {
	value []rune
	pos   int
}

func (in *lineInput) Value() string { return string(in.value) }

func (in *lineInput) SetValue(s string) {
	in.value = []rune(theme.SanitizeLine(s))
	in.pos = len(in.value)
}

func (in *lineInput) insert(s string) {
	s = theme.SanitizeLine(s)
	if s == "" {
		return
	}
	rs := []rune(s)
	v := make([]rune, 0, len(in.value)+len(rs))
	v = append(v, in.value[:in.pos]...)
	v = append(v, rs...)
	v = append(v, in.value[in.pos:]...)
	in.value = v
	in.pos += len(rs)
}

// wordStart returns the start of the word before pos (readline ctrl+w).
func (in *lineInput) wordStart() int {
	i := in.pos
	for i > 0 && unicode.IsSpace(in.value[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(in.value[i-1]) {
		i--
	}
	return i
}

func (in *lineInput) wordEnd() int {
	i := in.pos
	n := len(in.value)
	for i < n && unicode.IsSpace(in.value[i]) {
		i++
	}
	for i < n && !unicode.IsSpace(in.value[i]) {
		i++
	}
	return i
}

func (in *lineInput) del(from, to int) {
	if from < 0 || to > len(in.value) || from >= to {
		return
	}
	in.value = append(in.value[:from], in.value[to:]...)
	in.pos = from
}

// handle applies an editing key. It reports whether the key was consumed
// and whether the value changed.
func (in *lineInput) handle(msg tea.KeyPressMsg) (consumed, changed bool) {
	before := string(in.value)
	switch msg.String() {
	case "left", "ctrl+b":
		in.pos = max(in.pos-1, 0)
	case "right", "ctrl+f":
		in.pos = min(in.pos+1, len(in.value))
	case "home", "ctrl+a":
		in.pos = 0
	case "end", "ctrl+e":
		in.pos = len(in.value)
	case "alt+left", "alt+b", "ctrl+left":
		in.pos = in.wordStart()
	case "alt+right", "alt+f", "ctrl+right":
		in.pos = in.wordEnd()
	case "backspace", "ctrl+h":
		if in.pos > 0 {
			in.del(in.pos-1, in.pos)
		}
	case "delete":
		if in.pos < len(in.value) {
			p := in.pos
			in.del(p, p+1)
			in.pos = p
		}
	case "ctrl+w", "alt+backspace", "ctrl+backspace":
		in.del(in.wordStart(), in.pos)
	case "alt+d":
		p := in.pos
		in.del(p, in.wordEnd())
		in.pos = p
	case "ctrl+u":
		in.del(0, in.pos)
	case "ctrl+k":
		in.value = in.value[:in.pos]
	case "space":
		in.insert(" ")
	default:
		k := msg.Key()
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper) != 0 {
			return false, false
		}
		in.insert(k.Text)
	}
	return true, string(in.value) != before
}

// view renders the field in w cells: prompt, value (with an optional
// per-rune style) and a block cursor when focused. The value scrolls
// horizontally to keep the cursor visible.
func (in *lineInput) view(st *theme.Styles, w int, prompt, placeholder string, focused bool, styleAt func(i int) lipgloss.Style) string {
	pal := st.Theme.Palette
	promptStyle := lipgloss.NewStyle().Foreground(pal.Accent).Bold(true)
	if !focused {
		promptStyle = lipgloss.NewStyle().Foreground(pal.FgMuted)
	}
	p := promptStyle.Render(prompt)
	avail := w - width(prompt)
	if avail <= 1 {
		return hardClip(p, w)
	}
	text := lipgloss.NewStyle().Foreground(pal.Fg)
	cursor := lipgloss.NewStyle().Reverse(true)
	if len(in.value) == 0 {
		ph := st.Placeholder.Render(clip(placeholder, avail-1))
		if focused {
			return p + cursor.Render(" ") + ph
		}
		return p + " " + ph
	}
	// Choose a window [start, end) of runes that fits and contains pos.
	cells := func(r rune) int { return max(width(string(r)), 1) }
	start := 0
	used := 1 // cursor cell at the end
	for i := in.pos - 1; i >= 0; i-- {
		c := cells(in.value[i])
		if used+c > avail {
			start = i + 1
			break
		}
		used += c
	}
	var b strings.Builder
	used = 0
	for i := start; i <= len(in.value); i++ {
		if i == len(in.value) {
			if focused && in.pos == i && used < avail {
				b.WriteString(cursor.Render(" "))
			}
			break
		}
		r := string(in.value[i])
		c := cells(in.value[i])
		if used+c > avail {
			break
		}
		s := text
		if styleAt != nil {
			s = styleAt(i)
		}
		if focused && i == in.pos {
			s = s.Reverse(true)
		}
		b.WriteString(s.Render(r))
		used += c
	}
	return hardClip(p+b.String(), w)
}
