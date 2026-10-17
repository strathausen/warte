package info

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// KeyContextCalls is the keymap overlay context of the call log.
const KeyContextCalls = "info.calls"

// CallsBanner is the reminder shown above the call log.
const CallsBanner = "Calls ring on your phone — answer them there. warte keeps the history."

type callsLoaded struct {
	list []core.CallLogEntry
	err  error
}

// CallLog is the call history overlay ("<leader>c", ":calls").
type CallLog struct {
	ctx    ui.Context
	all    []core.CallLogEntry
	list   []core.CallLogEntry
	missed bool
	loaded bool
	err    error
	sel    int
	offset int
	height int
}

// NewCallLog returns the call log overlay; it loads svc.CallLog in Init.
func NewCallLog(ctx ui.Context) *CallLog { return &CallLog{ctx: ctx} }

func (o *CallLog) load() tea.Cmd {
	svc := o.ctx.Svc
	if svc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l, err := svc.CallLog(ctx)
		return callsLoaded{l, err}
	}
}

func (o *CallLog) Init() tea.Cmd      { return o.load() }
func (o *CallLog) Title() string      { return "Calls" }
func (o *CallLog) KeyContext() string { return KeyContextCalls }
func (o *CallLog) Size(maxW, maxH int) (int, int) {
	return min(64, maxW), min(max(30, maxH*3/4), maxH)
}
func (o *CallLog) Footer() string {
	return o.ctx.Styles.RenderKeyHints("enter", "open chat", "tab", "missed/all", "esc", "close")
}

// filter recomputes the visible list.
func (o *CallLog) filter() {
	o.list = o.list[:0]
	for _, e := range o.all {
		if !o.missed || e.Call.Missed {
			o.list = append(o.list, e)
		}
	}
	o.sel = clamp(o.sel, 0, max(len(o.list)-1, 0))
}

func (o *CallLog) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case callsLoaded:
		o.loaded, o.err = true, msg.err
		o.all = append([]core.CallLogEntry(nil), msg.list...)
		sort.SliceStable(o.all, func(i, j int) bool { return o.all[i].Call.StartedAt.After(o.all[j].Call.StartedAt) })
		o.filter()
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.IncomingCall:
			return o, o.load()
		case core.MessageAdded:
			if ev.Message.Kind == core.KindCall {
				return o, o.load()
			}
		case core.MessageUpdated:
			// Ringing → missed/declined, call event syncs.
			if ev.Message.Kind == core.KindCall {
				return o, o.load()
			}
		case core.MessageRemoved, core.ChatRemoved:
			// Deleted calls (call event syncs, delete for me).
			return o, o.load()
		case core.ChatUpdated:
			// The real backend has no call log event: call log syncs from
			// the phone (clear, mark read) re-announce the affected chats.
			return o, o.load()
		}
	case ui.ActionMsg:
		return o.act(msg.Action, msg.Count)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "tab", "m":
			return o.act(keymap.ActFocusNext, 1)
		}
		if a, ok := keyAction(msg.String()); ok {
			return o.act(a, 1)
		}
	}
	return o, nil
}

func (o *CallLog) act(a keymap.Action, count int) (ui.Overlay, tea.Cmd) {
	switch a {
	case keymap.ActFocusNext, keymap.ActFocusPrev, keymap.ActCursorLeft, keymap.ActCursorRight:
		o.missed = !o.missed
		if a == keymap.ActCursorLeft {
			o.missed = false
		} else if a == keymap.ActCursorRight {
			o.missed = true
		}
		o.sel, o.offset = 0, 0
		o.filter()
		return o, nil
	case keymap.ActItemSelect, keymap.ActChatOpen:
		if o.sel >= len(o.list) {
			return o, nil
		}
		chat := o.list[o.sel].Chat
		return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: chat} }
	}
	o.sel, _ = moveAction(a, count, o.sel, len(o.list), max(o.height/2, 1))
	return o, nil
}

// Selected returns the selected entry, if any.
func (o *CallLog) Selected() (core.CallLogEntry, bool) {
	if o.sel < len(o.list) {
		return o.list[o.sel], true
	}
	return core.CallLogEntry{}, false
}

func (o *CallLog) View(w, h int) string {
	o.height = h
	st := o.ctx.Styles
	if w <= 0 || h <= 0 {
		return ""
	}
	var top []string
	// Banner.
	p := st.Theme.Palette
	banner := lipgloss.NewStyle().Background(p.BgAlt).Foreground(p.FgMuted)
	if st.Theme.Mono {
		banner = lipgloss.NewStyle().Italic(true)
	}
	for _, l := range format.Wrap(o.ctx.Icons.Call+"  "+CallsBanner, w-2) {
		top = append(top, banner.Render(fit(" "+l+" ", w)))
	}
	// Tabs.
	tab := func(label string, on bool) string {
		if on {
			return st.KeyHintKey.Render(" " + label + " ")
		}
		return st.Subtitle.Render(" " + label + " ")
	}
	nMissed := 0
	for _, e := range o.all {
		if e.Call.Missed {
			nMissed++
		}
	}
	missedLabel := "Missed"
	if nMissed > 0 {
		missedLabel += " " + itoa(nMissed)
	}
	top = append(top, "", fit(" "+tab("All", !o.missed)+" "+tab(missedLabel, o.missed), w))

	bodyH := h - len(top)
	if bodyH <= 0 {
		return fitLines(top, w, h)
	}
	body, spans := o.renderList(w)
	if o.sel < len(spans) {
		a, b := spans[o.sel][0], spans[o.sel][1]
		if a < o.offset {
			o.offset = a
		}
		if b >= o.offset+bodyH {
			o.offset = b - bodyH + 1
		}
		if o.sel == 0 {
			o.offset = 0
		}
	}
	o.offset = clamp(o.offset, 0, max(len(body)-bodyH, 0))
	body = body[min(o.offset, len(body)):]
	return fitLines(append(top, body...), w, h)
}

func (o *CallLog) renderList(w int) ([]string, [][2]int) {
	st := o.ctx.Styles
	ic := o.ctx.Icons
	t := now(o.ctx)
	tf := timeFormatter(o.ctx)
	switch {
	case !o.loaded:
		return []string{"", center(st.Subtitle.Render("Loading…"), w)}, nil
	case o.err != nil:
		return []string{"", center(st.ErrorText.Render("Couldn't load calls: "+trunc(clean(o.err.Error()), w-24)), w)}, nil
	case len(o.list) == 0:
		msg := "No calls yet"
		if o.missed {
			msg = "No missed calls"
		}
		return []string{"", center(st.Subtitle.Render(msg), w)}, nil
	}
	var lines []string
	spans := make([][2]int, len(o.list))
	lastDay := ""
	for i, e := range o.list {
		day := tf.DayHeader(e.Call.StartedAt, t)
		if e.Call.StartedAt.IsZero() {
			day = "Earlier"
		}
		if day != lastDay {
			lines = append(lines, "", section(st, day, "", w))
			lastDay = day
		}
		icon, kind := callIcon(ic, e.Call)
		iconSt := st.Subtitle
		nameSt := lipgloss.NewStyle().Bold(true).Foreground(st.Theme.Palette.Fg)
		if e.Call.Missed {
			iconSt = st.ErrorText
			nameSt = st.ErrorText.Bold(true)
		}
		if st.Theme.Mono {
			nameSt = lipgloss.NewStyle().Bold(true)
		}
		id := string(e.Chat)
		if e.Chat.IsDirect() {
			id = string(e.Chat.ACI())
		}
		title := clean(e.Title)
		if title == "" {
			title = "Unknown"
		}
		when := ""
		if !e.Call.StartedAt.IsZero() {
			when = tf.Clock(e.Call.StartedAt)
		}
		unread := ""
		if e.Unread {
			unread = st.UnreadBadge.Render(ic.Unread) + " "
		}
		whenR := unread + st.Timestamp.Render(when)
		left := iconSt.Render(icon) + " " + st.RenderAvatar("", id, title) + " "
		rowW := w - 2 - width(ic.Cursor)
		nameW := max(rowW-width(left)-width(whenR)-1, 1)
		line1 := left + nameSt.Render(trunc(title, nameW))
		line1 += spaces(rowW-width(line1)-width(whenR)) + whenR
		sub := kind
		if d := callDuration(e.Call.Duration); d != "" {
			sub += " · " + d
		}
		if e.Call.Group {
			sub += " · group"
		}
		line2 := spaces(width(left)) + st.Subtitle.Render(trunc(sub, max(rowW-width(left), 1)))
		start := len(lines)
		if i == o.sel {
			lines = append(lines, selectedRow(st, ic, line1, w, true), selectedCont(st, ic, line2, w))
		} else {
			lines = append(lines, listRow(ic, line1, w), listRow(ic, line2, w))
		}
		spans[i] = [2]int{start, len(lines) - 1}
	}
	return lines, spans
}

// callIcon returns the glyph and description of a call: direction arrow
// plus kind icon, and "Missed video call" style text.
func callIcon(ic theme.IconSet, c core.CallInfo) (string, string) {
	kind, glyph := "voice call", ic.Call
	if c.Video {
		kind, glyph = "video call", ic.VideoCall
	}
	switch {
	case c.Missed:
		return "↙" + glyph, "Missed " + kind
	case c.Declined:
		return "↙" + glyph, "Declined " + kind
	case c.Incoming:
		return "↙" + glyph, "Incoming " + kind
	}
	return "↗" + glyph, "Outgoing " + kind
}
