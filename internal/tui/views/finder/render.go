package finder

import (
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/tui/theme"
)

// width returns the display width of s in cells (ANSI aware).
func width(s string) int { return ansi.StringWidth(s) }

// clip truncates s (plain or styled) to at most w cells, ending in "…" when
// shortened.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// hardClip truncates s to at most w cells without a tail.
func hardClip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

// pad clips s to w cells and fills the rest with spaces rendered in fill
// (so selected rows keep their background up to the edge).
func pad(s string, w int, fill lipgloss.Style) string {
	s = hardClip(s, w)
	if gap := w - width(s); gap > 0 {
		s += fill.Render(strings.Repeat(" ", gap))
	}
	return s
}

// spread lays out left and right segments across w cells, filling the gap
// with fill. The left segment is clipped first so the right one stays
// visible; when even the right segment does not fit it is dropped.
func spread(left, right string, w int, fill lipgloss.Style) string {
	rw := width(right)
	if rw > 0 && rw+2 > w {
		right, rw = "", 0
	}
	avail := w - rw
	if rw > 0 {
		avail-- // keep one cell between left and right
	}
	left = clip(left, max(avail, 0))
	gap := w - width(left) - rw
	if gap < 0 {
		return hardClip(left+right, w)
	}
	return left + fill.Render(strings.Repeat(" ", gap)) + right
}

// on returns s with the background (or reverse video in the mono theme) of
// row, so text drawn on a selected row keeps the row's highlight.
func on(s, row lipgloss.Style) lipgloss.Style {
	s = s.UnsetPadding().UnsetWidth().UnsetMargins()
	if row.GetReverse() {
		return s.Reverse(true)
	}
	if bg := row.GetBackground(); bg != nil {
		if _, none := bg.(lipgloss.NoColor); !none {
			return s.Background(bg)
		}
	}
	return s
}

// highlight renders text with the bytes listed in idx (byte offsets of
// matched runes, as returned by github.com/sahilm/fuzzy) drawn in hl and
// everything else in base. text must already be sanitized.
func highlight(text string, idx []int, base, hl lipgloss.Style) string {
	if text == "" {
		return ""
	}
	if len(idx) == 0 {
		return base.Render(text)
	}
	marked := make(map[int]bool, len(idx))
	for _, i := range idx {
		marked[i] = true
	}
	var b strings.Builder
	var run strings.Builder
	runHL := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runHL {
			b.WriteString(hl.Render(run.String()))
		} else {
			b.WriteString(base.Render(run.String()))
		}
		run.Reset()
	}
	for i, r := range text {
		m := marked[i]
		if m != runHL {
			flush()
			runHL = m
		}
		run.WriteRune(r)
	}
	flush()
	return b.String()
}

// highlightRanges renders untrusted text with the byte ranges in matches
// highlighted. Each segment is sanitized on its own so the offsets stay
// valid; out-of-range or overlapping ranges are ignored.
func highlightRanges(text string, matches [][2]int, base, hl lipgloss.Style) string {
	rs := make([][2]int, 0, len(matches))
	for _, m := range matches {
		if m[0] >= 0 && m[1] > m[0] && m[1] <= len(text) {
			rs = append(rs, m)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i][0] < rs[j][0] })
	var b strings.Builder
	pos := 0
	seg := func(s string, st lipgloss.Style) {
		s = theme.SanitizeLine(s)
		if s != "" {
			b.WriteString(st.Render(s))
		}
	}
	for _, r := range rs {
		if r[0] < pos {
			continue
		}
		seg(text[pos:r[0]], base)
		seg(text[r[0]:r[1]], hl)
		pos = r[1]
	}
	seg(text[pos:], base)
	return b.String()
}

// hrule renders a horizontal divider of w cells.
func hrule(st *theme.Styles, w int) string {
	if w <= 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(st.Theme.Palette.Border).Render(strings.Repeat("─", w))
}

// rowStyles returns the base text style for a list row and its selected
// variant, both without padding.
func rowStyles(st *theme.Styles, selected bool) lipgloss.Style {
	if selected {
		return st.ChatListItemSelected.UnsetPadding().UnsetWidth()
	}
	return st.ChatListItem.UnsetPadding().UnsetWidth().UnsetBold()
}

// cursorCell is the one-cell gutter in front of a row: an accent bar on the
// selected row, blank otherwise.
func cursorCell(st *theme.Styles, selected bool) string {
	if !selected {
		return " "
	}
	return lipgloss.NewStyle().Foreground(st.Theme.Palette.Accent).Bold(true).Render("▌")
}

// finish clips every line to w cells and caps the number of lines at h.
func finish(lines []string, w, h int) string {
	if len(lines) > h {
		lines = lines[:max(h, 0)]
	}
	for i, l := range lines {
		lines[i] = hardClip(l, w)
	}
	return strings.Join(lines, "\n")
}

// sideBySide joins a left column of lw cells and a right column of rw cells
// with a vertical divider, producing h lines.
func sideBySide(st *theme.Styles, left, right []string, lw, rw, h int) []string {
	sep := lipgloss.NewStyle().Foreground(st.Theme.Palette.Border).Render("│")
	out := make([]string, 0, h)
	for i := range h {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = hardClip(right[i], rw)
		}
		out = append(out, pad(l, lw, lipgloss.NewStyle())+" "+sep+" "+r)
	}
	return out
}

// scrollTo returns the first visible index so that sel stays inside a
// window of rows lines, keeping a little context when possible.
func scrollTo(off, sel, rows, total int) int {
	if rows <= 0 {
		return 0
	}
	if sel < off {
		off = sel
	}
	if sel >= off+rows {
		off = sel - rows + 1
	}
	off = min(off, max(total-rows, 0))
	return max(off, 0)
}
