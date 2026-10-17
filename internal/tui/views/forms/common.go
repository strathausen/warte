package forms

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Keymap overlay contexts (KeyContext values) of the views in this package.
// Views that embed a huh form or a text filter capture keys (ui.KeyCapturer)
// while it has focus, so these contexts only matter for the list-style pages.
const (
	ContextForm          = "forms.form"     // huh form dialogs (keys captured)
	ContextGroupSettings = "forms.group"    // group settings page
	ContextChatSettings  = "forms.chat"     // contact/chat settings page
	ContextSafety        = "forms.safety"   // safety number view
	ContextMembers       = "forms.members"  // fuzzy member picker (keys captured)
	ContextActions       = "forms.actions"  // small action menus (member actions)
	ContextNewGroup      = "forms.newgroup" // new group wizard (keys captured)
)

// opTimeout bounds every backend action started by a view.
const opTimeout = 30 * time.Second

// muteForever is the MutedUntil used for "muted always" (same convention as
// the App's :mute and the backends).
var muteForever = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// opDoneMsg reports the result of a backend action started by a view. It is
// delivered to the top overlay, which is normally the view that started it
// (sub-dialogs close before their action finishes).
type opDoneMsg struct {
	op  string // human readable, e.g. "Promote"
	err error
	ok  string // success toast ("" = none)
}

// runOp runs fn in a command with a timeout and reports an opDoneMsg.
func runOp(svc core.Service, op, okText string, fn func(ctx context.Context, svc core.Service) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		err := fn(ctx, svc)
		return opDoneMsg{op: op, err: err, ok: okText}
	}
}

// toastFor turns an opDoneMsg into a toast command.
func toastFor(m opDoneMsg) tea.Cmd {
	if m.err != nil {
		return ui.Toast(ui.ToastError, m.op+": "+clean(m.err.Error()))
	}
	if m.ok != "" {
		return ui.Toast(ui.ToastSuccess, m.ok)
	}
	return nil
}

// ---- text helpers -------------------------------------------------------

// clean sanitizes untrusted single-line text.
func clean(s string) string { return theme.SanitizeLine(s) }

// width is the display width of s, ANSI aware.
func width(s string) int { return ansi.StringWidth(s) }

// fit truncates or pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "")
		if strings.Contains(s, "\x1b") {
			s += "\x1b[m"
		}
	}
	if sw := width(s); sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// trunc shortens s to at most w cells, ending in "…" when cut.
func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	s = ansi.Truncate(s, w, "…")
	if strings.Contains(s, "\x1b") {
		s += "\x1b[m"
	}
	return s
}

// clampLines returns at most h lines, each at most w cells wide.
func clampLines(lines []string, w, h int) string {
	if h >= 0 && len(lines) > h {
		lines = lines[:h]
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if width(l) > w {
			l = trunc(l, w)
		}
		out[i] = l
	}
	return strings.Join(out, "\n")
}

// wrapText wraps plain (already sanitized) text to w cells.
func wrapText(s string, w int) []string {
	if w <= 0 {
		return nil
	}
	return format.Wrap(s, w)
}

// spaces returns n spaces.
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// twoCol renders left and right on one line of exactly w cells, truncating
// left first.
func twoCol(left, right string, w int) string {
	rw := width(right)
	if rw >= w {
		return fit(trunc(right, w), w)
	}
	lw := w - rw - 1
	left = trunc(left, lw)
	return left + spaces(w-width(left)-rw) + right
}

// center centers s in w cells.
func center(s string, w int) string {
	sw := width(s)
	if sw >= w {
		return trunc(s, w)
	}
	return spaces((w-sw)/2) + s
}

// timerLabel names a disappearing-message timer.
func timerLabel(d time.Duration) string {
	if d <= 0 {
		return "Off"
	}
	s := format.FormatDurationLong(d)
	return strings.ToUpper(s[:1]) + s[1:]
}

// timerOptions returns the timer presets, plus cur when it is not a preset.
func timerOptions(cur time.Duration) []time.Duration {
	opts := append([]time.Duration(nil), format.TimerPresets...)
	found := false
	for _, d := range opts {
		if d == cur {
			found = true
		}
	}
	if !found && cur > 0 {
		opts = append(opts, cur)
		for i := len(opts) - 1; i > 0 && opts[i] < opts[i-1]; i-- {
			opts[i], opts[i-1] = opts[i-1], opts[i]
		}
	}
	return opts
}

// cycle returns the option after (delta>0) or before cur in opts, wrapping.
func cycle[T comparable](opts []T, cur T, delta int) T {
	if len(opts) == 0 {
		return cur
	}
	idx := 0
	for i, o := range opts {
		if o == cur {
			idx = i
		}
	}
	idx = ((idx+delta)%len(opts) + len(opts)) % len(opts)
	return opts[idx]
}

// MuteOption is one entry of the mute menu.
type MuteOption struct {
	Label string
	// For is the mute duration; 0 = unmute, -1 = always.
	For time.Duration
}

// MuteOptions are the choices offered by the chat settings (Signal's set).
var MuteOptions = []MuteOption{
	{"Off", 0},
	{"1 hour", time.Hour},
	{"8 hours", 8 * time.Hour},
	{"1 day", 24 * time.Hour},
	{"1 week", 7 * 24 * time.Hour},
	{"Always", -1},
}

// MuteUntil converts a mute option to a MutedUntil timestamp.
func MuteUntil(o MuteOption, now time.Time) time.Time {
	switch {
	case o.For == 0:
		return time.Time{}
	case o.For < 0:
		return muteForever
	}
	return now.Add(o.For)
}

// muteLabel describes the current mute state.
func muteLabel(until, now time.Time) string {
	switch {
	case !now.Before(until):
		return "Off"
	case until.Year() >= 9000:
		return "Always"
	}
	left := until.Sub(now)
	if left < time.Minute {
		return "< 1 minute"
	}
	return "for " + format.FormatDurationLong(left.Round(time.Minute))
}

// ---- keys -----------------------------------------------------------------

// keyOf returns the key a message stands for: the canonical key of a raw
// press, or the typed keys of a fired binding. Views use it for letter
// shortcuts that the keymap might also bind globally (e.g. "n").
func keyOf(msg tea.Msg) string {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		return keymap.FromTea(m)
	case ui.ActionMsg:
		return m.Keys
	}
	return ""
}

// navDelta interprets list navigation actions, returning the cursor delta
// (0 = not navigation). page is the visible height for page motions.
func navDelta(msg tea.Msg, page int) (delta int, ok bool) {
	page = max(page, 1)
	switch m := msg.(type) {
	case ui.ActionMsg:
		n := max(m.Count, 1)
		switch m.Action {
		case keymap.ActCursorDown:
			return n, true
		case keymap.ActCursorUp:
			return -n, true
		case keymap.ActHalfPageDown:
			return max(page/2, 1), true
		case keymap.ActHalfPageUp:
			return -max(page/2, 1), true
		case keymap.ActPageDown:
			return page, true
		case keymap.ActPageUp:
			return -page, true
		case keymap.ActCursorTop:
			return -1 << 30, true
		case keymap.ActCursorBottom:
			return 1 << 30, true
		}
	case tea.KeyPressMsg:
		switch keymap.FromTea(m) {
		case "down", "ctrl+n":
			return 1, true
		case "up", "ctrl+p":
			return -1, true
		}
	case tea.MouseWheelMsg:
		if m.Button == tea.MouseWheelDown {
			return 1, true
		}
		if m.Button == tea.MouseWheelUp {
			return -1, true
		}
	}
	return 0, false
}

// isSelect reports enter / item.select.
func isSelect(msg tea.Msg) bool {
	switch m := msg.(type) {
	case ui.ActionMsg:
		return m.Action == keymap.ActItemSelect
	case tea.KeyPressMsg:
		return keymap.FromTea(m) == "enter"
	}
	return false
}

// leftRight interprets h/l and arrow keys: -1, +1 or 0.
func leftRight(msg tea.Msg) int {
	switch m := msg.(type) {
	case ui.ActionMsg:
		switch m.Action {
		case keymap.ActCursorLeft:
			return -1
		case keymap.ActCursorRight:
			return 1
		}
	case tea.KeyPressMsg:
		switch keymap.FromTea(m) {
		case "left", "h":
			return -1
		case "right", "l":
			return 1
		}
	}
	return 0
}

// ---- styling --------------------------------------------------------------

// pal is a shortcut for the live palette.
func pal(st *theme.Styles) theme.Palette { return st.Theme.Palette }

// fg returns a style with the given foreground (plain style in mono).
func fg(st *theme.Styles, c interface{ RGBA() (r, g, b, a uint32) }) lipgloss.Style {
	if st.Theme.Mono {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(c)
}

// sectionTitle renders a section heading.
func sectionTitle(st *theme.Styles, s string) string {
	return st.Subtitle.Bold(true).Render(s)
}

// muted renders secondary text.
func muted(st *theme.Styles, s string) string {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Faint(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(pal(st).FgMuted).Render(s)
}

// subtle renders tertiary text.
func subtle(st *theme.Styles, s string) string {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Faint(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(pal(st).FgSubtle).Render(s)
}

// accent renders accented text.
func accent(st *theme.Styles, s string) string {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Bold(true).Render(s)
	}
	return lipgloss.NewStyle().Foreground(pal(st).Accent).Bold(true).Render(s)
}

// badge renders a small pill such as "Admin".
func badge(st *theme.Styles, s string, c interface{ RGBA() (r, g, b, a uint32) }) string {
	if st.Theme.Mono {
		return lipgloss.NewStyle().Reverse(true).Render(" " + s + " ")
	}
	return lipgloss.NewStyle().Foreground(pal(st).Bg).Background(c).Bold(true).Render(" " + s + " ")
}

// toggleGlyph renders an on/off switch.
func toggleGlyph(st *theme.Styles, on bool) string {
	if on {
		if st.Theme.Mono {
			return lipgloss.NewStyle().Bold(true).Render("● on ")
		}
		return lipgloss.NewStyle().Foreground(pal(st).Success).Bold(true).Render("● on ")
	}
	return muted(st, "○ off")
}

// statusLine renders an error (red) or info line.
func statusLine(st *theme.Styles, text string, isErr bool, w int) string {
	if text == "" {
		return ""
	}
	if isErr {
		return st.ErrorText.Render(trunc("✗ "+clean(text), w))
	}
	return st.InfoText.Render(trunc(clean(text), w))
}

// selectedRow renders a highlighted row of exactly w cells.
func selectedRow(st *theme.Styles, s string, w int) string {
	return st.ChatListItemSelected.UnsetPadding().Render(fit(ansi.Strip(s), w))
}

// stripANSI removes escape sequences (for re-styling a rendered string).
func stripANSI(s string) string { return ansi.Strip(s) }

// nowFn returns the context's clock (time.Now when unset).
func nowFn(ctx ui.Context) func() time.Time {
	if ctx.Now != nil {
		return ctx.Now
	}
	return time.Now
}

// selfACI returns the local account's ACI.
func selfACI(ctx ui.Context) core.ACI {
	if ctx.Self.ACI != "" {
		return ctx.Self.ACI
	}
	if ctx.Svc != nil {
		return ctx.Svc.Self().ACI
	}
	return ""
}
