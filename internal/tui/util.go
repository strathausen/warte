package tui

import (
	"image/color"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/termimg"
	"warte/internal/tui/theme"
)

// sgrReset closes every SGR attribute. Used after cutting ANSI strings so no
// style leaks into the next cell.
const sgrReset = "\x1b[m"

// width is the display width of s in cells, ANSI aware.
func width(s string) int { return ansi.StringWidth(s) }

// fit returns s truncated or right-padded with spaces to exactly w cells.
// It never produces a line wider than w, even when s contains wide
// characters that straddle the boundary.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "")
		if strings.Contains(s, "\x1b") {
			s += sgrReset
		}
		sw = width(s)
	}
	if sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// fitEllipsis is fit but marks truncation with "…".
func fitEllipsis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "…")
		if strings.Contains(s, "\x1b") {
			s += sgrReset
		}
	}
	return fit(s, w)
}

// padStyled pads s to w cells, painting the padding with st (so a background
// continues across the line).
func padStyled(s string, w int, st lipgloss.Style) string {
	sw := width(s)
	if sw > w {
		return fit(s, w)
	}
	if sw == w {
		return s
	}
	return s + st.Render(strings.Repeat(" ", w-sw))
}

// spaces returns n spaces (n <= 0 gives "").
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// fitLines returns exactly h lines, each exactly w cells wide.
func fitLines(lines []string, w, h int) []string {
	out := make([]string, h)
	for i := 0; i < h; i++ {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = spaces(w)
		}
	}
	return out
}

// splitLines splits a rendered block into lines.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// hjoin joins blocks of equal height side by side. Each block's lines must
// already have their final width.
func hjoin(blocks ...[]string) []string {
	h := 0
	for _, b := range blocks {
		h = max(h, len(b))
	}
	out := make([]string, h)
	for i := 0; i < h; i++ {
		var sb strings.Builder
		for _, b := range blocks {
			if i < len(b) {
				sb.WriteString(b[i])
			}
		}
		out[i] = sb.String()
	}
	return out
}

// splice overlays fg (a single line of width fw) onto bg starting at cell x,
// keeping bg's content on both sides. ANSI styles are reset around the
// overlay. The result is exactly as wide as bg (assuming x+fw <= width(bg)).
func splice(bg, fg string, x int) string {
	bw := width(bg)
	fw := width(fg)
	if x < 0 {
		fg = ansi.TruncateLeft(fg, -x, "")
		fw = width(fg)
		x = 0
	}
	if x >= bw {
		return bg
	}
	if x+fw > bw {
		fg = ansi.Truncate(fg, bw-x, "")
		fw = width(fg)
	}
	left := ansi.Truncate(bg, x, "")
	lw := width(left)
	var sb strings.Builder
	sb.WriteString(left)
	sb.WriteString(sgrReset)
	sb.WriteString(spaces(x - lw)) // a wide char straddled the edge
	sb.WriteString(fg)
	sb.WriteString(sgrReset)
	want := bw - x - fw
	right := ansi.TruncateLeft(bg, x+fw, "")
	for k := 1; width(right) > want && k <= 2; k++ {
		// A wide character straddles the right edge: skip past it.
		right = ansi.TruncateLeft(bg, x+fw+k, "")
	}
	sb.WriteString(spaces(want - width(right)))
	sb.WriteString(right)
	return sb.String()
}

// blockGlyphs blanks image cells (half-blocks) in the dimmed backdrop, where
// they would otherwise show as stripes.
var blockGlyphs = strings.NewReplacer("▀", " ", "▄", " ", "█", " ", "▌", " ", "▐", " ", "░", " ", "▒", " ", "▓", " ")

// dimLine strips styling from a rendered line and draws it in the subtle
// color, for the backdrop behind dialogs. Kitty placeholder cells are blanked
// (without their color they would reference no image).
func dimLine(s string, st lipgloss.Style) string {
	plain := blockGlyphs.Replace(ansi.Strip(s))
	if strings.ContainsRune(plain, termimg.PlaceholderRune) {
		var b strings.Builder
		skip := false
		for _, r := range plain {
			if r == termimg.PlaceholderRune {
				b.WriteByte(' ')
				skip = true
				continue
			}
			if skip && termimg.DiacriticValue(r) >= 0 {
				continue
			}
			skip = false
			b.WriteRune(r)
		}
		plain = b.String()
	}
	return st.Render(plain)
}

// clean sanitizes untrusted single-line text (names, titles, snippets).
func clean(s string) string { return theme.SanitizeLine(s) }

// firstLine returns the first line of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// truncRunes cuts s to at most n runes.
func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// clamp limits v to [lo, hi].
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// humanSize formats a byte count.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := ftoa1(v)
	return s + " " + string("KMGTPE"[exp]) + "B"
}

func ftoa1(v float64) string {
	i := int(v*10 + 0.5)
	if i%10 == 0 {
		return itoa(i / 10)
	}
	return itoa(i/10) + "." + itoa(i%10)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// box draws a rounded border around content lines with an optional title in
// the top edge and an optional footer in the bottom edge. The result is
// exactly w x h cells. Content lines are fitted to the inner width.
func box(st *theme.Styles, lines []string, w, h int, focused bool, title, footer string) []string {
	if w < 2 || h < 2 {
		return fitLines(nil, max(w, 0), max(h, 0))
	}
	b := lipgloss.RoundedBorder()
	var bs lipgloss.Style
	if focused {
		bs = lipgloss.NewStyle().Foreground(st.PaneFocused.GetBorderTopForeground())
	} else {
		bs = lipgloss.NewStyle().Foreground(st.Pane.GetBorderTopForeground())
	}
	if st.Theme.Mono && focused {
		b = lipgloss.ThickBorder()
	}
	inner := w - 2
	out := make([]string, 0, h)
	out = append(out, borderEdge(bs, b.TopLeft, b.Top, b.TopRight, title, inner))
	body := fitLines(lines, inner, h-2)
	l := bs.Render(b.Left)
	r := bs.Render(b.Right)
	for _, line := range body {
		out = append(out, l+line+r)
	}
	out = append(out, borderEdge(bs, b.BottomLeft, b.Bottom, b.BottomRight, footer, inner))
	return out
}

// borderEdge renders "╭─ title ────╮" with the title already styled.
func borderEdge(bs lipgloss.Style, left, fill, right, label string, inner int) string {
	if label == "" || inner < 6 {
		return bs.Render(left + strings.Repeat(fill, max(inner, 0)) + right)
	}
	label = fitEllipsisRaw(label, inner-4)
	lw := width(label)
	rest := inner - lw - 3
	return bs.Render(left+fill) + " " + label + " " + bs.Render(strings.Repeat(fill, max(rest, 0))+right)
}

// fitEllipsisRaw truncates (but does not pad) s to w cells.
func fitEllipsisRaw(s string, w int) string {
	if width(s) <= w {
		return s
	}
	s = ansi.Truncate(s, w, "…")
	if strings.Contains(s, "\x1b") {
		s += sgrReset
	}
	return s
}

// hrule draws a horizontal divider of w cells.
func hrule(st *theme.Styles, w int) string {
	return lipgloss.NewStyle().Foreground(st.Pane.GetBorderTopForeground()).Render(strings.Repeat("─", max(w, 0)))
}

// rightAlign places right at the end of a w-cell line that starts with left;
// left is truncated when both don't fit.
func rightAlign(left, right string, w int) string {
	rw := width(right)
	if rw >= w {
		return fit(right, w)
	}
	return fitEllipsis(left, w-rw) + right
}

// withLineBg paints bg behind a whole rendered line: the color is set at the
// start and re-applied after every SGR sequence that resets the background,
// so styled segments with their own background keep it.
func withLineBg(line string, bg color.Color) string {
	if isNoColor(bg) {
		return line
	}
	seq := ansi.NewStyle().BackgroundColor(bg).String()
	var b strings.Builder
	b.WriteString(seq)
	for i := 0; i < len(line); {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
				j++
			}
			if j >= len(line) {
				b.WriteString(line[i:])
				break
			}
			b.WriteString(line[i : j+1])
			if line[j] == 'm' && resetsBg(line[i+2:j]) {
				b.WriteString(seq)
			}
			i = j + 1
			continue
		}
		b.WriteByte(line[i])
		i++
	}
	b.WriteString(sgrReset)
	return b.String()
}

// resetsBg reports whether SGR parameters reset the background color.
func resetsBg(params string) bool {
	if params == "" {
		return true
	}
	ps := strings.Split(params, ";")
	reset := false
	for i := 0; i < len(ps); i++ {
		p := ps[i]
		if k := strings.IndexByte(p, ':'); k >= 0 { // colon sub-params
			p = p[:k]
		}
		switch p {
		case "0", "", "49":
			reset = true
		case "48":
			reset = false // a later explicit background wins
			fallthrough
		case "38", "58":
			if strings.Contains(ps[i], ":") {
				continue
			}
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return reset
}
