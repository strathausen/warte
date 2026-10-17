package info

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

const sgrReset = "\x1b[m"

// width is the display width of s in cells (ANSI aware).
func width(s string) int { return ansi.StringWidth(s) }

// fit truncates or right-pads s to exactly w cells.
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

// trunc shortens s to at most w cells, marking the cut with "…". It does
// not pad.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	s = ansi.Truncate(s, w, "…")
	if strings.Contains(s, "\x1b") {
		s += sgrReset
	}
	return s
}

// center pads s on both sides to w cells (truncating when too wide).
func center(s string, w int) string {
	sw := width(s)
	if sw >= w {
		return fit(s, w)
	}
	left := (w - sw) / 2
	return fit(strings.Repeat(" ", left)+s, w)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// fitLines returns exactly h lines, each exactly w cells wide.
func fitLines(lines []string, w, h int) string {
	if h <= 0 || w <= 0 {
		return ""
	}
	out := make([]string, h)
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = spaces(w)
		}
	}
	return strings.Join(out, "\n")
}

// clean sanitizes untrusted single-line text.
func clean(s string) string { return theme.SanitizeLine(s) }

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	return max(lo, min(v, hi))
}

// now returns the context clock (time.Now when unset).
func now(ctx ui.Context) time.Time {
	if ctx.Now != nil {
		return ctx.Now()
	}
	return time.Now()
}

func timeFormatter(ctx ui.Context) format.TimeFormatter {
	return format.TimeFormatter{Hour12: ctx.Config.TimeFormat == "12h"}
}

// fmtStyles maps the theme onto format.Styles for message bodies.
func fmtStyles(ctx ui.Context) format.Styles {
	st := ctx.Styles
	return format.Styles{
		Bold:        lipgloss.NewStyle().Bold(true),
		Italic:      lipgloss.NewStyle().Italic(true),
		Strike:      lipgloss.NewStyle().Strikethrough(true),
		Mono:        st.Code,
		Mention:     st.Mention,
		MentionSelf: st.MentionSelf,
		Link:        st.Link,
		Spoiler:     st.Spoiler,
		SelfACI:     ctx.Self.ACI,
	}
}

// section renders a section heading with a hairline rule:
// " MEMBERS · 5 ──────────".
func section(st *theme.Styles, title, extra string, w int) string {
	head := " " + st.Title.Render(strings.ToUpper(title))
	if extra != "" {
		head += st.Subtitle.Render(" · " + extra)
	}
	rest := w - width(head) - 1
	if rest > 0 {
		head += " " + st.KeyHintSep.Render(strings.Repeat("─", rest))
	}
	return fit(head, w)
}

// kv renders a "key   value" row with a fixed key column.
func kv(st *theme.Styles, key, value string, keyW, w int) string {
	k := st.Subtitle.Render(fit(key, keyW))
	return fit(" "+k+" "+trunc(value, w-keyW-2), w)
}

// humanSize formats a byte count ("1.2 MB").
func humanSize(n int64) string {
	if n <= 0 {
		return "—"
	}
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + string("KMGTP"[exp]) + "B"
}

// fullTime formats an absolute timestamp for detail views.
func fullTime(t time.Time, tf format.TimeFormatter) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("Mon, Jan 2 2006") + " · " + tf.Clock(t) + t.Format(":05")
}

// callDuration formats a call length ("41m", "1h 5m", "23m 14s", "45s").
func callDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int(d % time.Hour / time.Minute)
	s := int(d % time.Minute / time.Second)
	var parts []string
	if h > 0 {
		parts = append(parts, strconv.Itoa(h)+"h")
	}
	if m > 0 {
		parts = append(parts, strconv.Itoa(m)+"m")
	}
	if s > 0 && h == 0 {
		parts = append(parts, strconv.Itoa(s)+"s")
	}
	return strings.Join(parts, " ")
}

// statusText names a delivery status.
func statusText(s core.DeliveryStatus) string {
	switch s {
	case core.StatusPending:
		return "Sending"
	case core.StatusSent:
		return "Sent"
	case core.StatusDelivered:
		return "Delivered"
	case core.StatusRead:
		return "Read"
	case core.StatusViewed:
		return "Viewed"
	case core.StatusFailed:
		return "Failed"
	}
	return "—"
}

// attachmentKind returns an icon and label for an attachment.
func attachmentKind(ic theme.IconSet, a core.Attachment) (string, string) {
	switch {
	case a.VoiceNote:
		return ic.Voice, "Voice message"
	case a.GIF:
		return ic.Image, "GIF"
	case a.IsImage():
		return ic.Image, "Photo"
	case a.IsVideo():
		return ic.Video, "Video"
	case a.IsAudio():
		return ic.Audio, "Audio"
	}
	return ic.File, "File"
}

// avatarTile renders a w×h colored tile with centered initials, used when
// no avatar image is available.
func avatarTile(st *theme.Styles, code, id, name string, w, h int) []string {
	p := st.Theme.Palette
	tile := lipgloss.NewStyle().Background(p.AvatarColor(code, id)).Foreground(p.Bg).Bold(true)
	if st.Theme.Mono {
		tile = lipgloss.NewStyle().Reverse(true).Bold(true)
	}
	out := make([]string, h)
	mid := h / 2
	for i := range out {
		txt := spaces(w)
		if i == mid {
			txt = center(theme.Initials(name), w)
		}
		out[i] = tile.Render(txt)
	}
	return out
}

// selStyle is the selected-row style without the chat list's padding.
func selStyle(st *theme.Styles) lipgloss.Style { return st.ChatListItemSelected.Padding(0) }

// selectedRow paints a full-width selected list row with an accent cursor.
// Unfocused, only a muted cursor marks the row.
func selectedRow(st *theme.Styles, ic theme.IconSet, s string, w int, focused bool) string {
	if !focused {
		return fit(" "+st.Subtitle.Render(ic.Cursor)+s, w)
	}
	base := selStyle(st)
	cur := base
	if !st.Theme.Mono {
		cur = base.Foreground(st.Theme.Palette.Accent)
	}
	return fit(base.Render(" ")+cur.Render(ic.Cursor)+base.Render(fit(ansi.Strip(s), w-1-width(ic.Cursor))), w)
}

// selectedCont renders a continuation line of a selected multi-line row.
func selectedCont(st *theme.Styles, ic theme.IconSet, s string, w int) string {
	return selStyle(st).Render(fit(" "+spaces(width(ic.Cursor))+ansi.Strip(s), w))
}

// listRow renders an unselected row with the same indent as selectedRow.
func listRow(ic theme.IconSet, s string, w int) string {
	return fit(" "+spaces(width(ic.Cursor))+s, w)
}

// moveAction maps cursor actions to a new index in [0, n).
func moveAction(a keymap.Action, count, sel, n, page int) (int, bool) {
	if n <= 0 {
		return 0, false
	}
	count = max(count, 1)
	switch a {
	case keymap.ActCursorDown:
		return clamp(sel+count, 0, n-1), true
	case keymap.ActCursorUp:
		return clamp(sel-count, 0, n-1), true
	case keymap.ActCursorTop:
		return 0, true
	case keymap.ActCursorBottom:
		return n - 1, true
	case keymap.ActHalfPageDown:
		return clamp(sel+max(page/2, 1), 0, n-1), true
	case keymap.ActHalfPageUp:
		return clamp(sel-max(page/2, 1), 0, n-1), true
	case keymap.ActPageDown:
		return clamp(sel+max(page, 1), 0, n-1), true
	case keymap.ActPageUp:
		return clamp(sel-max(page, 1), 0, n-1), true
	}
	return sel, false
}

// scrollAction maps cursor actions to a new scroll offset in [0, maxOff].
func scrollAction(a keymap.Action, count, off, maxOff, page int) (int, bool) {
	count = max(count, 1)
	switch a {
	case keymap.ActCursorDown:
		off += count
	case keymap.ActCursorUp:
		off -= count
	case keymap.ActCursorTop:
		off = 0
	case keymap.ActCursorBottom:
		off = maxOff
	case keymap.ActHalfPageDown:
		off += max(page/2, 1)
	case keymap.ActHalfPageUp:
		off -= max(page/2, 1)
	case keymap.ActPageDown:
		off += max(page, 1)
	case keymap.ActPageUp:
		off -= max(page, 1)
	default:
		return off, false
	}
	return clamp(off, 0, maxOff), true
}

// keyAction maps raw key presses to cursor actions, for hosts that deliver
// tea.KeyPressMsg instead of ActionMsg (unbound keys or embedded use).
func keyAction(key string) (keymap.Action, bool) {
	switch key {
	case "j", "down":
		return keymap.ActCursorDown, true
	case "k", "up":
		return keymap.ActCursorUp, true
	case "h", "left":
		return keymap.ActCursorLeft, true
	case "l", "right":
		return keymap.ActCursorRight, true
	case "G", "end":
		return keymap.ActCursorBottom, true
	case "home":
		return keymap.ActCursorTop, true
	case "ctrl+d":
		return keymap.ActHalfPageDown, true
	case "ctrl+u":
		return keymap.ActHalfPageUp, true
	case "enter":
		return keymap.ActItemSelect, true
	}
	return "", false
}

// memberName resolves an ACI to a display name using chat members.
func memberName(c *core.Chat, self core.Contact, aci core.ACI) string {
	if aci == self.ACI && aci != "" {
		return "You"
	}
	if c != nil {
		for _, m := range c.Members {
			if m.ACI == aci {
				return clean(m.Name)
			}
		}
		if c.Contact != nil && c.Contact.ACI == aci {
			return clean(c.Contact.DisplayName())
		}
	}
	return "Unknown"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
