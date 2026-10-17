package stories

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/theme"
)

// width is the display width of s in cells (ANSI aware).
func width(s string) int { return ansi.StringWidth(s) }

// clip truncates every line to w cells and keeps at most h lines.
func clip(lines []string, w, h int) string {
	if h >= 0 && len(lines) > h {
		lines = lines[:h]
	}
	for i, l := range lines {
		if width(l) > w {
			lines[i] = ansi.Truncate(l, w, "")
		}
	}
	return strings.Join(lines, "\n")
}

// padRight pads s with spaces to w cells (s must not be wider).
func padRight(s string, w int) string {
	if d := w - width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// center pads s on both sides to w cells.
func center(s string, w int) string {
	d := w - width(s)
	if d <= 0 {
		return s
	}
	return strings.Repeat(" ", d/2) + s + strings.Repeat(" ", d-d/2)
}

// oneLine sanitizes untrusted text and collapses it to a single line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(theme.SanitizeLine(s)), " ")
}

// truncate shortens plain text to w cells with an ellipsis.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return format.Truncate(s, w)
}

// ---- text stories ----------------------------------------------------------------

// textStoryColors resolves the background stops and the text color of a
// text story. Background stops are top to bottom.
func textStoryColors(st *theme.Styles, ts *core.TextStory) (stops []color.Color, fg color.Color) {
	p := st.Theme.Palette
	for _, g := range ts.Gradient {
		stops = append(stops, argb(g, p.Bg))
	}
	if len(stops) == 0 {
		if ts.Background != 0 {
			stops = []color.Color{argb(ts.Background, p.Bg)}
		} else {
			stops = []color.Color{p.Accent}
		}
	}
	mid := gradientAt(stops, 0.5)
	if ts.Foreground>>24 != 0 {
		fg = argb(ts.Foreground, mid)
	}
	return stops, readableOn(fg, mid)
}

// textStyle applies Signal's font style (TextAttachment.Style) as far as a
// terminal can: bold for Default/Bold, italic for Serif/Script.
func textStyle(s lipgloss.Style, style int) lipgloss.Style {
	switch style {
	case 0, 2:
		return s.Bold(true)
	case 3, 4:
		return s.Italic(true)
	}
	return s
}

// graphemes splits s into grapheme clusters.
func graphemes(s string) []string {
	var out []string
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		out = append(out, g.Str())
	}
	return out
}

// largeLines lays out short text "large": letters spaced apart and words
// separated by three spaces, packed greedily into lines of at most w cells.
// ok is false when the text is too long or a word does not fit.
func largeLines(text string, w, maxLines int) (lines []string, ok bool) {
	if strings.Contains(text, "\n") || len(graphemes(text)) > 48 {
		return nil, false
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil, false
	}
	var cur string
	for _, word := range words {
		sp := strings.Join(graphemes(word), " ")
		if width(sp) > w {
			return nil, false
		}
		switch {
		case cur == "":
			cur = sp
		case width(cur)+3+width(sp) <= w:
			cur += "   " + sp
		default:
			lines = append(lines, cur)
			cur = sp
		}
	}
	lines = append(lines, cur)
	if len(lines) > maxLines {
		return nil, false
	}
	return lines, true
}

// layoutStoryText wraps a text story's body for an area of w x h cells.
// Short texts are drawn "large" (letter-spaced, double-spaced lines) when
// they fit; everything else is word-wrapped. The result has at most h lines.
func layoutStoryText(text string, w, h int) []string {
	text = strings.TrimSpace(theme.Sanitize(text))
	if text == "" || w < 1 || h < 1 {
		return nil
	}
	if ll, ok := largeLines(text, w, 3); ok && 2*len(ll)-1 <= h {
		out := make([]string, 0, 2*len(ll)-1)
		for i, l := range ll {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, l)
		}
		return out
	}
	lines := format.Wrap(text, w)
	if len(lines) > h {
		lines = lines[:h]
		last := strings.TrimRight(lines[h-1], " ")
		if width(last)+1 > w {
			last = ansi.Truncate(last, max(w-1, 0), "")
		}
		lines[h-1] = last + "…"
	}
	return lines
}

// urlLabel is the visible text of a story's link preview line.
func urlLabel(u string) string {
	u = oneLine(u)
	for _, p := range []string{"https://", "http://"} {
		u = strings.TrimPrefix(u, p)
	}
	return strings.TrimPrefix(u, "www.")
}

// renderTextStory draws a text story into exactly h lines of exactly w
// cells: the background follows the story's color (gradients are
// approximated by blending one color per row), the text is centered.
func renderTextStory(st *theme.Styles, icons theme.IconSet, ts *core.TextStory, w, h int) []string {
	if w < 1 || h < 1 {
		return nil
	}
	if ts == nil {
		ts = &core.TextStory{}
	}
	mono := st.Theme.Mono
	stops, fg := textStoryColors(st, ts)
	innerW := max(w-4, 1)

	url := strings.TrimSpace(oneLine(ts.URL))
	urlRows := 0
	if url != "" {
		urlRows = 2
		if h < 4 {
			urlRows = 1
		}
	}
	body := layoutStoryText(ts.Text, innerW, max(h-urlRows-2, 1))
	if len(body) == 0 && url == "" {
		body = []string{"…"}
	}
	if len(body)+urlRows > h {
		body = body[:max(h-urlRows, 0)]
	}
	block := len(body) + urlRows
	if len(body) == 0 && urlRows > 0 {
		block = 1
	}
	top := max((h-block)/2, 0)

	out := make([]string, h)
	for row := 0; row < h; row++ {
		t := 0.0
		if h > 1 {
			t = float64(row) / float64(h-1)
		}
		bg := gradientAt(stops, t)
		base := lipgloss.NewStyle()
		if !mono {
			base = base.Background(bg).Foreground(fg)
		}
		i := row - top
		switch {
		case i >= 0 && i < len(body):
			out[row] = renderCentered(textStyle(base, ts.Style), base, body[i], w)
		case url != "" && i == block-1:
			out[row] = renderURL(st, icons, base, bg, fg, url, w)
		default:
			out[row] = base.Render(strings.Repeat(" ", w))
		}
	}
	return out
}

// renderCentered centers plain text s in w cells: padding in pad style, the
// text in text style.
func renderCentered(text, pad lipgloss.Style, s string, w int) string {
	if width(s) > w {
		s = ansi.Truncate(s, w, "")
	}
	d := w - width(s)
	l, r := d/2, d-d/2
	out := ""
	if l > 0 {
		out += pad.Render(strings.Repeat(" ", l))
	}
	if s != "" {
		out += text.Render(s)
	}
	if r > 0 {
		out += pad.Render(strings.Repeat(" ", r))
	}
	return out
}

// renderURL draws the link preview pill of a text story.
func renderURL(st *theme.Styles, icons theme.IconSet, base lipgloss.Style, bg, fg color.Color, url string, w int) string {
	icon := icons.Link
	if icon == "" {
		icon = "↗"
	}
	label := truncate(urlLabel(url), max(w-8, 1))
	pill := " " + icon + " " + label + " "
	if width(pill) > w {
		pill = ansi.Truncate(pill, w, "")
	}
	pillStyle := base.Underline(true)
	if !st.Theme.Mono {
		shade := white
		if luminance(bg) > 0.4 {
			shade = black
		}
		pillStyle = lipgloss.NewStyle().Background(blend(bg, shade, 0.22)).Foreground(fg).Underline(true)
	}
	rendered := pillStyle.Render(pill)
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		rendered = format.Hyperlink(url, rendered)
	}
	d := w - width(pill)
	l, r := d/2, d-d/2
	return base.Render(strings.Repeat(" ", l)) + rendered + base.Render(strings.Repeat(" ", r))
}

// ---- progress --------------------------------------------------------------------

// renderProgress draws one segment per story: watched ones filled, the
// current one filled to frac, upcoming ones muted. With too many stories for
// the width it draws a single bar plus a counter.
func renderProgress(st *theme.Styles, n, cur int, frac float64, w int) string {
	if w < 1 || n < 1 {
		return ""
	}
	p := st.Theme.Palette
	on := lipgloss.NewStyle().Foreground(p.Accent)
	off := lipgloss.NewStyle().Foreground(p.FgSubtle)
	onCh, offCh := "━", "━"
	if st.Theme.Mono {
		on, off = lipgloss.NewStyle().Bold(true), lipgloss.NewStyle().Faint(true)
		offCh = "─"
	}
	frac = max(0, min(1, frac))
	bar := func(cells int, filled float64) string {
		f := int(filled*float64(cells) + 0.5)
		f = max(0, min(cells, f))
		s := ""
		if f > 0 {
			s += on.Render(strings.Repeat(onCh, f))
		}
		if cells-f > 0 {
			s += off.Render(strings.Repeat(offCh, cells-f))
		}
		return s
	}
	segW := (w - (n - 1)) / n
	if segW < 2 {
		label := " " + itoa(cur+1) + "/" + itoa(n)
		if width(label)+4 > w {
			return bar(w, (float64(cur)+frac)/float64(n))
		}
		bw := w - width(label)
		total := (float64(cur) + frac) / float64(n)
		return bar(bw, total) + st.Timestamp.Render(label)
	}
	extra := w - (segW*n + (n - 1))
	var b strings.Builder
	for i := 0; i < n; i++ {
		cw := segW
		if i < extra {
			cw++
		}
		if i > 0 {
			b.WriteString(" ")
		}
		switch {
		case i < cur:
			b.WriteString(bar(cw, 1))
		case i == cur:
			b.WriteString(bar(cw, frac))
		default:
			b.WriteString(bar(cw, 0))
		}
	}
	return b.String()
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
