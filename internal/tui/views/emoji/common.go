package emoji

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/tui/theme"
)

// width returns the display width of s in terminal cells.
func width(s string) int { return ansi.StringWidth(s) }

// fit truncates or right-pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "")
		sw = width(s)
	}
	if sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// fitEllipsis truncates s to w cells with a trailing "…" and pads it.
func fitEllipsis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return fit(s, w)
}

// center pads s with spaces on both sides to exactly w cells.
func center(s string, w int) string {
	sw := width(s)
	if sw >= w {
		return fit(s, w)
	}
	left := (w - sw) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", w-sw-left)
}

// clean makes untrusted text a safe single line.
func clean(s string) string { return theme.SanitizeLine(s) }

// selStyle highlights the selected cell / row.
func selStyle(st *theme.Styles) lipgloss.Style {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Reverse(true).Bold(true)
	}
	p := st.Theme.Palette
	return lipgloss.NewStyle().Background(p.BgSelected).Foreground(p.Fg).Bold(true)
}

func muted(st *theme.Styles) lipgloss.Style {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Faint(true)
	}
	return lipgloss.NewStyle().Foreground(st.Theme.Palette.FgMuted)
}

func subtle(st *theme.Styles) lipgloss.Style {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Faint(true)
	}
	return lipgloss.NewStyle().Foreground(st.Theme.Palette.FgSubtle)
}

func accent(st *theme.Styles) lipgloss.Style {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Bold(true)
	}
	return lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true)
}

// tabSpan is the horizontal extent of a rendered tab (for the underline and
// mouse hit testing).
type tabSpan struct{ x, w, index int }

// renderTabs renders labels as a one-line tab bar of exactly w cells with
// sel highlighted, scrolling horizontally so the selected tab stays visible.
// It returns the line, the underline rule (accent under the selected tab)
// and the visible tab spans.
func renderTabs(st *theme.Styles, labels []string, sel, w int) (bar, rule string, spans []tabSpan) {
	if w <= 0 {
		return "", "", nil
	}
	cells := make([]string, len(labels))
	widths := make([]int, len(labels))
	for i, l := range labels {
		cells[i] = " " + l + " "
		widths[i] = width(cells[i])
		if widths[i] > w {
			cells[i] = fitEllipsis(cells[i], w)
			widths[i] = w
		}
	}
	// Choose the first visible tab so that sel fits.
	start := 0
	total := func(from, to int) int {
		n := 0
		for i := from; i <= to && i < len(widths); i++ {
			n += widths[i]
		}
		return n
	}
	if sel >= 0 && sel < len(labels) {
		for start < sel && total(start, sel) > w {
			start++
		}
	}
	var b, r strings.Builder
	x := 0
	if start > 0 {
		b.WriteString(subtle(st).Render("‹"))
		r.WriteString(subtle(st).Render("─"))
		x = 1
	}
	ruleStyle := lipgloss.NewStyle().Foreground(st.Theme.Palette.Border)
	if st.Theme.Mono {
		ruleStyle = lipgloss.NewStyle().Faint(true)
	}
	for i := start; i < len(cells); i++ {
		cw := widths[i]
		more := i < len(cells)-1
		room := w - x
		if more {
			room-- // keep a cell for "›"
		}
		if cw > room {
			if i == start || i == sel {
				cw = max(room, 0)
				if cw == 0 {
					break
				}
				cells[i] = fitEllipsis(cells[i], cw)
			} else {
				break
			}
		}
		if i == sel {
			b.WriteString(selStyle(st).Render(cells[i]))
			r.WriteString(accent(st).Render(strings.Repeat("━", cw)))
		} else {
			b.WriteString(muted(st).Render(cells[i]))
			r.WriteString(ruleStyle.Render(strings.Repeat("─", cw)))
		}
		spans = append(spans, tabSpan{x: x, w: cw, index: i})
		x += cw
	}
	if len(spans) > 0 && spans[len(spans)-1].index < len(cells)-1 && x < w {
		b.WriteString(subtle(st).Render("›"))
		r.WriteString(ruleStyle.Render("─"))
		x++
	}
	if x < w {
		b.WriteString(strings.Repeat(" ", w-x))
		r.WriteString(ruleStyle.Render(strings.Repeat("─", w-x)))
	}
	return b.String(), r.String(), spans
}

// scrollTo returns the first visible row so that row sel is inside a window
// of n rows starting at off.
func scrollTo(off, sel, n, total int) int {
	if n <= 0 {
		return 0
	}
	if sel < off {
		off = sel
	}
	if sel >= off+n {
		off = sel - n + 1
	}
	off = min(off, max(total-n, 0))
	return max(off, 0)
}

// padLines pads (or cuts) lines to exactly h lines of w cells.
func padLines(lines []string, w, h int) string {
	if h <= 0 {
		return ""
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = fit(l, w)
	}
	return strings.Join(lines, "\n")
}

// joinRight renders left and right on one line of w cells, dropping right
// when there is no room.
func joinRight(left, right string, w int) string {
	lw, rw := width(left), width(right)
	if right == "" || lw+rw+1 > w {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", w-lw-rw) + right
}

func plural(n int, one, many string) string {
	if n == 1 {
		return itoa(n) + " " + one
	}
	return itoa(n) + " " + many
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
