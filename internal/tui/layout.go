package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// narrowWidth is the width below which only one pane is shown.
const narrowWidth = 90

// minSidebarConv is the minimum conversation width that leaves room for
// the sidebar.
const minSidebarConv = 48

type rect struct{ x, y, w, h int }

type layout struct {
	w, h   int
	narrow bool
	list   rect
	conv   rect
	side   rect
}

func (a *App) computeLayout() layout {
	W, H := a.width, a.height
	l := layout{w: W, h: H}
	ph := max(H-1, 0) // status line
	if W < narrowWidth {
		l.narrow = true
		r := rect{0, 0, W, ph}
		pane := a.narrowPane
		if pane == FocusComposer {
			pane = FocusConversation
		}
		switch {
		case pane == FocusConversation && a.conv.open:
			l.conv = r
		case pane == FocusSidebar && a.sidebarOpen && a.conv.open:
			l.side = r
		default:
			l.list = r
		}
		return l
	}
	lw := a.cfg.ChatListWidth
	if lw <= 0 {
		lw = 32
	}
	lw = clamp(lw, 24, max(W/3, 24))
	l.list = rect{0, 0, lw, ph}
	rest := W - lw
	if a.sidebarOpen && a.conv.open {
		sw := clamp(a.cfg.SidebarWidth, 24, 60)
		if rest-sw >= minSidebarConv {
			l.side = rect{W - sw, 0, sw, ph}
			rest -= sw
		}
	}
	l.conv = rect{lw, 0, rest, ph}
	return l
}

// render draws the whole screen: exactly a.height lines of a.width cells.
func (a *App) render() string {
	W, H := a.width, a.height
	if W <= 0 || H <= 0 {
		return ""
	}
	a.zones.reset()
	if a.locked() {
		return strings.Join(fitLines(a.renderLock(W, H), W, H), "\n")
	}
	if W < 20 || H < 6 {
		return strings.Join(fitLines([]string{a.styles.Subtitle.Render("warte: window too small")}, W, H), "\n")
	}
	if a.loggedOut != nil && len(a.overlays) == 0 {
		return strings.Join(a.renderLoggedOut(W, H), "\n")
	}
	a.layout = a.computeLayout()
	l := a.layout
	screen := make([]string, H)
	for i := range screen {
		screen[i] = ""
	}
	// Panes, left to right.
	var cols [][]string
	if l.list.w > 0 {
		cols = append(cols, a.renderListPane(l.list))
	}
	switch {
	case l.conv.w > 0 && a.conv.open:
		cols = append(cols, a.renderConvPane(l.conv))
	case l.conv.w > 0:
		cols = append(cols, a.renderEmptyPane(l.conv))
	}
	if l.side.w > 0 {
		if a.sidebarPane != nil {
			cols = append(cols, a.renderSidebarPane(l.side))
		} else {
			cols = append(cols, a.renderSidebar(l.side))
		}
	}
	rows := hjoin(cols...)
	rows = fitLines(rows, W, H-1)
	rows = append(rows, a.renderStatus(W))

	// Floating layers: completion popup, toasts, overlays.
	if pop := a.renderCompletion(l.conv.w); len(pop) > 0 && a.composerTop > 0 {
		y := max(a.composerTop-len(pop), 0)
		x := l.conv.x + 3
		for i, p := range pop {
			if y+i < len(rows) {
				rows[y+i] = splice(rows[y+i], p, x)
			}
		}
	}
	rows = a.renderOverlay(rows)
	rows = a.renderToasts(rows)
	for i := range rows {
		rows[i] = fit(rows[i], W)
	}
	return strings.Join(rows, "\n")
}

func (a *App) renderListPane(r rect) []string {
	focused := a.focus == FocusChatList
	inner := a.renderChatList(r.w-2, r.h-2, r.x+1, r.y+1)
	title := a.paneTitle("warte", focused)
	if a.list.archived {
		title = a.paneTitle("Archived", focused)
	}
	if ring := a.storyRing(); ring != "" && r.w >= 24 {
		// Story ring next to the title; click (or <leader>s) opens stories.
		tw := width(title)
		a.zones.add(zone{x: r.x + 3 + tw + 2, y: r.y, w: width(ring), h: 1, kind: zoneStories})
		title += "  " + ring
	}
	n := len(a.list.chats)
	foot := ""
	if n > 0 {
		foot = a.styles.KeyHintSep.Render(itoa(a.list.sel+1) + "/" + itoa(n))
	}
	return box(a.styles, inner, r.w, r.h, focused, title, foot)
}

func (a *App) paneTitle(t string, focused bool) string {
	st := a.styles
	if focused {
		return st.Title.Render(t)
	}
	return st.Subtitle.Bold(true).Render(t)
}

func (a *App) renderEmptyPane(r rect) []string {
	st := a.styles
	lines := make([]string, r.h-2)
	mid := len(lines) / 2
	logo := []string{
		st.Title.Render("warte"),
		st.Subtitle.Render("a watchtower for your messages"),
		"",
		st.RenderKeyHints("enter", "open chat", "space space", "find", "?", "help"),
	}
	for i, l := range logo {
		if y := mid - 2 + i; y >= 0 && y < len(lines) {
			lines[y] = centerText(l, r.w-2)
		}
	}
	return box(st, lines, r.w, r.h, false, "", "")
}

// renderConvPane draws the conversation: header, messages, typing line and
// composer.
func (a *App) renderConvPane(r rect) []string {
	st := a.styles
	focused := a.focus == FocusConversation || a.focus == FocusComposer
	iw, ih := r.w-2, r.h-2
	var lines []string

	header := a.renderHeader(iw)
	lines = append(lines, header...)
	if strip := a.renderPinnedStrip(iw); len(strip) > 0 && ih >= 12 {
		a.zones.add(zone{x: r.x + 1, y: r.y + 1 + len(lines), w: iw, h: 1, kind: zonePinned})
		lines = append(lines, strip...)
	}
	lines = append(lines, hrule(st, iw))

	comp := a.renderComposer(iw)
	typing := a.renderTypingLine(iw)
	msgH := ih - len(lines) - len(comp) - len(typing)
	msgW := iw
	if msgH < 1 {
		// Too short: drop the header first.
		lines = nil
		msgH = ih - len(comp) - len(typing)
	}
	msgY := r.y + 1 + len(lines)
	msgTop := len(lines)
	if msgH >= 1 {
		lines = append(lines, a.renderMessages(msgW, msgH, r.x+1, msgY)...)
	}
	lines = append(lines, typing...)
	a.composerTop = r.y + 1 + len(lines)
	a.zones.add(zone{x: r.x + 1, y: a.composerTop, w: iw, h: len(comp), kind: zoneComposer})
	lines = append(lines, comp...)

	title := ""
	foot := ""
	if a.search.highlight != "" && len(a.search.hits) > 0 {
		foot = st.KeyHintKey.Render("/"+clean(a.search.highlight)) + st.KeyHintDesc.Render(" "+itoa(a.search.idx+1)+"/"+itoa(len(a.search.hits)))
	}
	out := box(st, lines, r.w, r.h, focused, title, foot)
	// Scroll thumb drawn on the right border of the message area.
	if msgH >= 1 {
		if y0, n, ok := a.convThumb(msgH); ok {
			th := st.ScrollbarThumb.Render("┃")
			for i := y0; i < y0+n; i++ {
				row := 1 + msgTop + i
				if row > 0 && row < len(out)-1 {
					out[row] = ansi.Truncate(out[row], r.w-1, "") + sgrReset + th
				}
			}
		}
	}
	return out
}

// convThumb computes the scroll thumb (first row, height) for the message
// list from message indices (heights of off-screen messages are unknown).
func (a *App) convThumb(h int) (int, int, bool) {
	c := a.conv
	n := len(c.msgs)
	if n == 0 {
		return 0, 0, false
	}
	top := a.topVisible()
	if c.atBottom() && top == 0 && !c.hasMore {
		return 0, 0, false
	}
	total := n
	if c.hasMore {
		total += n // assume at least as much older history
	}
	visible := max(c.bottom-top+1, 1)
	thumb := clamp(h*visible/max(total, 1), 1, h)
	first := total - n + top
	y0 := clamp(h*first/max(total, 1), 0, h-thumb)
	if c.atBottom() {
		y0 = h - thumb
	}
	return y0, thumb, true
}

// renderHeader draws the chat title row(s) at the top of the conversation.
func (a *App) renderHeader(w int) []string {
	st := a.styles
	p := st.Theme.Palette
	c := a.conv.chat
	now := a.now()
	if !a.conv.open {
		return []string{fit(st.Subtitle.Render(" No chat open"), w)}
	}
	title := clean(c.Title)
	if c.Kind == core.ChatNoteToSelf {
		title = "Note to Self"
	}
	id := string(c.ID)
	if c.Contact != nil {
		id = string(c.Contact.ACI)
	}
	av := st.Avatar(c.Color, id).Render(theme.Initials(title))
	left := " " + av + " " + lipgloss.NewStyle().Foreground(p.Fg).Bold(true).Render(title)
	var icons []string
	if c.Contact != nil && c.Contact.Verified {
		icons = append(icons, st.SuccessText.Render(a.icons.Lock))
	}
	if c.Muted(now) {
		icons = append(icons, st.MutedIcon.Render(a.icons.Muted))
	}
	if c.ExpireTimer > 0 {
		icons = append(icons, st.Subtitle.Render(a.icons.Clock+" "+format.FormatDuration(c.ExpireTimer)))
	}
	if c.Pinned {
		icons = append(icons, st.PinnedIcon.Render(a.icons.Pinned))
	}
	if c.Blocked {
		icons = append(icons, st.ErrorText.Render("blocked"))
	}
	right := strings.Join(icons, "  ")
	if right != "" {
		right += " "
	}
	row1 := rightAlign(left, right, w)

	sub := ""
	switch {
	case c.Kind == core.ChatNoteToSelf:
		sub = "Messages to yourself, synced to your devices"
	case c.Kind == core.ChatGroup:
		sub = plural(len(c.Members), "member")
		if c.OnlyAdminsSend {
			sub += " · only admins can send"
		}
		if d := strings.TrimSpace(c.Description); d != "" {
			sub += " · " + format.Snippet(d, nil, nil, 0)
		}
	case c.Contact != nil:
		var parts []string
		if c.Contact.AboutEmoji != "" || c.Contact.About != "" {
			parts = append(parts, strings.TrimSpace(c.Contact.AboutEmoji+" "+c.Contact.About))
		}
		if c.Contact.Username != "" {
			parts = append(parts, c.Contact.Username)
		} else if c.Contact.E164 != "" {
			parts = append(parts, c.Contact.E164)
		}
		sub = strings.Join(parts, " · ")
	}
	if names := a.typingNames(c.ID); len(names) > 0 && c.Kind != core.ChatGroup {
		sub = "typing…"
	}
	row2 := fitEllipsis("      "+st.Subtitle.Render(clean(sub)), w)
	rows := []string{row1, row2}

	// Incoming call banner.
	for _, call := range a.calls {
		if !call.Ringing {
			continue
		}
		icon := a.icons.Call
		kind := "calling"
		if call.Video {
			icon = a.icons.VideoCall
			kind = "video calling"
		}
		banner := lipgloss.NewStyle().Background(p.Accent).Foreground(p.Bg).Bold(true)
		if st.Theme.Mono {
			banner = lipgloss.NewStyle().Reverse(true).Bold(true)
		}
		text := " " + icon + "  " + clean(call.Name) + " is " + kind + " — answer on your phone "
		rows = append(rows, banner.Render(fit(text, w)))
		break
	}
	return rows
}

// renderTypingLine shows "Alice is typing ●∙∙" above the composer.
func (a *App) renderTypingLine(w int) []string {
	if !a.conv.open {
		return nil
	}
	names := a.typingNames(a.conv.chat.ID)
	if len(names) == 0 {
		return nil
	}
	st := a.styles
	var who string
	switch len(names) {
	case 1:
		who = names[0] + " is typing"
	case 2:
		who = names[0] + " and " + names[1] + " are typing"
	default:
		who = itoa(len(names)) + " people are typing"
	}
	frame := a.icons.TypingFrame(a.tick)
	if a.cfg.ReduceMotion {
		frame = a.icons.Typing[0]
	}
	return []string{fit("  "+st.Typing.Render(frame)+" "+st.Subtitle.Render(who), w)}
}

// renderSidebar shows information about the open chat.
func (a *App) renderSidebar(r rect) []string {
	st := a.styles
	p := st.Theme.Palette
	focused := a.focus == FocusSidebar
	c := a.conv.chat
	iw := r.w - 2
	var lines []string
	title := clean(c.Title)
	if c.Kind == core.ChatNoteToSelf {
		title = "Note to Self"
	}
	id := string(c.ID)
	if c.Contact != nil {
		id = string(c.Contact.ACI)
	}
	// Big avatar: 8x4 colored tile with initials.
	col := p.AvatarColor(c.Color, id)
	tile := lipgloss.NewStyle().Background(col).Foreground(p.Bg).Bold(true)
	if st.Theme.Mono {
		tile = lipgloss.NewStyle().Reverse(true).Bold(true)
	}
	shown := false
	if c.AvatarID != "" && a.images.avatarsAsImages() {
		rows, ok := a.images.Rows(ui.ImageRef{Kind: ui.ImageAvatar, AvatarID: c.AvatarID, Attachment: core.Attachment{Width: 1, Height: 1}}, 10, 5)
		if ok {
			for _, row := range rows {
				lines = append(lines, centerText(row, iw))
			}
			shown = true
		}
	}
	if !shown {
		lines = append(lines, "",
			centerText(tile.Render(spaces(10)), iw),
			centerText(tile.Render(centerText(theme.Initials(title), 10)), iw),
			centerText(tile.Render(spaces(10)), iw))
	}
	lines = append(lines, "", centerText(lipgloss.NewStyle().Bold(true).Foreground(p.Fg).Render(fitEllipsisRaw(title, iw-2)), iw))
	if c.Contact != nil {
		if ab := strings.TrimSpace(c.Contact.AboutEmoji + " " + c.Contact.About); ab != "" {
			lines = append(lines, centerText(st.Subtitle.Render(fitEllipsisRaw(clean(ab), iw-2)), iw))
		}
	}
	lines = append(lines, "")
	kv := func(k, v string) {
		lines = append(lines, fit(" "+st.Subtitle.Render(fit(k, 11))+st.App.Render(fitEllipsisRaw(clean(v), iw-13)), iw))
	}
	if c.Contact != nil {
		if c.Contact.Username != "" {
			kv("Username", c.Contact.Username)
		}
		if c.Contact.E164 != "" {
			kv("Phone", c.Contact.E164)
		}
		v := "not verified"
		if c.Contact.Verified {
			v = a.icons.Verified + " verified"
		}
		kv("Safety", v)
	}
	timer := "off"
	if c.ExpireTimer > 0 {
		timer = format.FormatDurationLong(c.ExpireTimer)
	}
	kv("Disappear", timer)
	mute := "off"
	if c.Muted(a.now()) {
		mute = "on"
		if c.MutedUntil.Year() < 9000 {
			mute = "until " + a.tf.RelativeTime(c.MutedUntil, a.now())
		}
	}
	kv("Muted", mute)
	if c.Kind == core.ChatGroup {
		if d := strings.TrimSpace(c.Description); d != "" {
			lines = append(lines, "")
			for _, l := range format.Wrap(clean(d), iw-2) {
				lines = append(lines, " "+st.Subtitle.Render(l))
			}
		}
		lines = append(lines, "", " "+st.Title.Render(plural(len(c.Members), "member")))
		for _, m := range c.Members {
			name := clean(m.Name)
			if m.ACI == a.self.ACI {
				name = "You"
			}
			row := " " + st.Avatar("", string(m.ACI)).Render(theme.Initials(name)) + " " + fitEllipsisRaw(name, iw-14)
			if m.Admin {
				row += " " + st.Subtitle.Render("admin")
			}
			lines = append(lines, fit(row, iw))
		}
	}
	// Scroll.
	ih := r.h - 2
	a.sidebarMax = max(len(lines)-ih, 0)
	a.sidebarOff = clamp(a.sidebarOff, 0, a.sidebarMax)
	lines = lines[min(a.sidebarOff, len(lines)):]
	return box(st, lines, r.w, r.h, focused, a.paneTitle("Info", focused), "")
}

func (a *App) sidebarScroll(n int) {
	a.sidebarOff = clamp(a.sidebarOff+n, 0, a.sidebarMax)
}

// renderStatus draws the bottom status line.
func (a *App) renderStatus(w int) string {
	st := a.styles
	p := st.Theme.Palette
	mode := a.keys.Mode()
	var tm theme.Mode
	switch mode {
	case keymap.Insert:
		tm = theme.ModeInsert
	case keymap.Visual, keymap.VisualLine:
		tm = theme.ModeVisual
	case keymap.Command:
		tm = theme.ModeCommand
	case keymap.Search:
		tm = theme.ModeSearch
	}
	// An overlay that reads keys raw (a finder or form field with focus,
	// the story viewer) bypasses the keymap, so its keymap hints ("j/k
	// move", "q close") would be wrong there; its own hints are in its
	// border.
	capturing := false
	if n := len(a.overlays); n > 0 {
		if kc, ok := a.overlays[n-1].(ui.KeyCapturer); ok && kc.CaptureKeys() {
			capturing = true
		}
	}
	badge := st.ModeBadge(tm).Render(mode.Label())
	bar := lipgloss.NewStyle()
	sep := " "

	// Right side: sync, connection.
	var right []string
	if pk := a.keys.PendingKeys(); pk != "" {
		right = append(right, st.KeyHintKey.Render(clean(pk)))
	}
	if a.sync != nil && (a.sync.Total == 0 || a.sync.Done < a.sync.Total) {
		s := "◐ " + clean(a.sync.Stage)
		if a.sync.Total > 0 {
			s += " " + itoa(a.sync.Done) + "/" + itoa(a.sync.Total)
		}
		right = append(right, st.InfoText.Render(s))
	}
	if mode.IsVisual() {
		lo, hi, _ := a.conv.visualRange()
		right = append(right, st.Subtitle.Render(plural(hi-lo+1, "message")))
	}
	var conn string
	switch a.conn {
	case core.ConnConnected:
		conn = lipgloss.NewStyle().Foreground(p.Success).Render("●") + st.Subtitle.Render(" linked")
	case core.ConnConnecting:
		conn = lipgloss.NewStyle().Foreground(p.Warning).Render("◌") + st.Subtitle.Render(" connecting")
	case core.ConnLoggedOut:
		conn = st.ErrorText.Render("● logged out")
	default:
		conn = lipgloss.NewStyle().Foreground(p.Error).Render("○") + st.Subtitle.Render(" offline")
	}
	right = append(right, conn)
	rs := strings.Join(right, st.KeyHintSep.Render("  ")) + " "

	var mid string
	switch {
	case mode == keymap.Command || mode == keymap.Search:
		mid = a.renderCmdLine(max(w-width(badge)-width(rs)-2, 4))
	case capturing:
	default:
		// Add hints while they fit; never cut one in half.
		avail := w - width(badge) - width(rs) - 3 // keep a two-cell gap before the right side
		var pairs []string
		hctx := a.focusContext()
		if len(a.overlays) > 0 {
			hctx = keymap.Overlay(a.overlays[len(a.overlays)-1].KeyContext())
		}
		for _, e := range a.keys.ShortHelp(mode, hctx) {
			next := append(append([]string(nil), pairs...), e.KeyLabel(), e.Help)
			if width(st.RenderKeyHints(next...)) > avail {
				break
			}
			pairs = next
		}
		mid = st.RenderKeyHints(pairs...)
	}
	left := badge + bar.Render(sep) + mid
	return rightAlign(left, rs, w)
}

// renderToasts floats toasts in the top-right corner.
func (a *App) renderToasts(rows []string) []string {
	if len(a.toasts) == 0 {
		return rows
	}
	st := a.styles
	W := a.width
	y := 1
	maxW := min(max(W/2, 30), W-4)
	for i := len(a.toasts) - 1; i >= 0; i-- {
		t := a.toasts[i]
		s := st.ToastInfo
		icon := "ℹ"
		switch t.level {
		case ui.ToastSuccess:
			s, icon = st.ToastSuccess, "✓"
		case ui.ToastWarning:
			s, icon = st.ToastWarning, "!"
		case ui.ToastError:
			s, icon = st.ToastError, "✗"
		}
		iconStyle := lipgloss.NewStyle().Foreground(s.GetBorderTopForeground()).Bold(true)
		text := fitEllipsisRaw(t.text, maxW-6)
		tl := splitLines(s.Render(iconStyle.Render(icon) + " " + text))
		tw := 0
		for _, l := range tl {
			tw = max(tw, width(l))
		}
		x := W - tw - 2
		for k, l := range tl {
			if y+k < len(rows)-1 {
				rows[y+k] = splice(rows[y+k], l, x)
			}
		}
		y += len(tl)
		if y >= len(rows)-2 {
			break
		}
	}
	return rows
}

type toast struct {
	id    int
	level ui.ToastLevel
	text  string
}

func (a *App) renderLoggedOut(W, H int) []string {
	st := a.styles
	msg := []string{
		st.Title.Render("Logged out"),
		"",
		st.App.Render("This device is no longer linked to your Signal account."),
		st.Subtitle.Render("Run `warte link` to link it again."),
		"",
		st.RenderKeyHints("q", "quit"),
	}
	if a.loggedOut != nil && a.loggedOut.Error() != "" {
		msg = append(msg[:4], append([]string{st.ErrorText.Render(clean(a.loggedOut.Error()))}, msg[4:]...)...)
	}
	out := make([]string, H)
	top := (H - len(msg)) / 2
	for i := range out {
		out[i] = spaces(W)
		if j := i - top; j >= 0 && j < len(msg) {
			out[i] = centerText(msg[j], W)
		}
	}
	return out
}
