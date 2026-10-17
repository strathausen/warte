package tui

import (
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"

	"warte/internal/config"
	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// chatItemHeight is the number of rows one chat occupies in the list.
const chatItemHeight = 2

// folderTab is one tab above the chat list.
type folderTab struct {
	name   string
	filter core.ChatFilter
	cfg    *config.FolderConfig // client-side folder from config.toml
}

// typer is someone typing in a chat.
type typer struct {
	name  string
	until time.Time
}

type chatList struct {
	chats    []core.Chat
	sel      int
	offset   int // first visible item
	pos, vel float64
	spring   harmonica.Spring
	tabs     []folderTab
	tab      int
	archived bool
	filter   string
	typing   map[core.ChatID]map[core.ACI]typer
	height   int // visible item slots, from the last render
}

func newChatList() *chatList {
	return &chatList{
		typing: map[core.ChatID]map[core.ACI]typer{},
		spring: harmonica.NewSpring(harmonica.FPS(60), 9.0, 1.0),
	}
}

func (l *chatList) current() *core.Chat {
	if l.sel < 0 || l.sel >= len(l.chats) {
		return nil
	}
	return &l.chats[l.sel]
}

func (l *chatList) indexOf(id core.ChatID) int {
	for i := range l.chats {
		if l.chats[i].ID == id {
			return i
		}
	}
	return -1
}

func (l *chatList) springActive() bool {
	return math.Abs(l.pos-float64(l.offset)) > 0.01 || math.Abs(l.vel) > 0.01
}

func (l *chatList) animate(a *App) {
	if a.cfg.ReduceMotion {
		l.pos, l.vel = float64(l.offset), 0
		return
	}
	l.pos, l.vel = l.spring.Update(l.pos, l.vel, float64(l.offset))
	if math.Abs(l.pos-float64(l.offset)) < 0.05 && math.Abs(l.vel) < 0.05 {
		l.pos, l.vel = float64(l.offset), 0
	}
}

// visibleOffset is the (animated) first visible item.
func (l *chatList) visibleOffset() int {
	return int(math.Round(l.pos))
}

// ensureVisible scrolls so the selection is on screen.
func (l *chatList) ensureVisible(reduceMotion bool) {
	h := max(l.height, 1)
	if l.sel < l.offset {
		l.offset = l.sel
	}
	if l.sel >= l.offset+h {
		l.offset = l.sel - h + 1
	}
	l.offset = clamp(l.offset, 0, max(len(l.chats)-h, 0))
	if reduceMotion {
		l.pos, l.vel = float64(l.offset), 0
	}
}

// buildTabs assembles the folder tabs: All, Unread, Groups, service folders
// and config folders. Service folders with the same name replace built-ins.
func (a *App) buildTabs() {
	tabs := []folderTab{
		{name: "All"},
		{name: "Unread", filter: core.ChatFilter{UnreadOnly: true}},
		{name: "Groups", filter: core.ChatFilter{GroupsOnly: true}},
	}
	ctx, cancel := a.queryCtx()
	defer cancel()
	if fs, err := a.svc.Folders(ctx); err == nil {
		for _, f := range fs {
			name := clean(f.Name)
			replaced := false
			for i := range tabs {
				if strings.EqualFold(tabs[i].name, name) {
					tabs[i].filter = f.Filter
					replaced = true
				}
			}
			if !replaced && name != "" {
				tabs = append(tabs, folderTab{name: name, filter: f.Filter})
			}
		}
	}
	for i := range a.cfg.Folders {
		f := a.cfg.Folders[i]
		tabs = append(tabs, folderTab{name: clean(f.Name), cfg: &f})
	}
	a.list.tabs = tabs
	if a.list.tab >= len(tabs) {
		a.list.tab = 0
	}
}

// rebuildTabs re-reads the folders (after a storage sync), keeping the
// current tab selected by name.
func (a *App) rebuildTabs() {
	cur := ""
	if a.list.tab < len(a.list.tabs) {
		cur = a.list.tabs[a.list.tab].name
	}
	a.buildTabs()
	a.list.tab = 0
	for i, t := range a.list.tabs {
		if t.name == cur {
			a.list.tab = i
			break
		}
	}
	a.refreshChats()
}

// refreshChats re-queries the chat list, keeping the selection on the same
// chat when it moves.
func (a *App) refreshChats() {
	if len(a.list.tabs) == 0 {
		a.buildTabs()
	}
	l := a.list
	var selID core.ChatID
	if c := l.current(); c != nil {
		selID = c.ID
	}
	tab := l.tabs[l.tab]
	f := tab.filter
	f.Archived = l.archived
	f.Query = l.filter
	if tab.cfg != nil {
		f = core.ChatFilter{Archived: l.archived || tab.cfg.ShowArchived, Query: l.filter}
	}
	ctx, cancel := a.queryCtx()
	defer cancel()
	chats, err := a.svc.Chats(ctx, f)
	if err != nil {
		return
	}
	if tab.cfg != nil {
		now := a.now()
		kept := chats[:0]
		for _, c := range chats {
			if tab.cfg.Match(c, now) {
				kept = append(kept, c)
			}
		}
		chats = kept
	}
	l.chats = chats
	l.sel = clamp(l.sel, 0, max(len(chats)-1, 0))
	if selID != "" {
		if i := l.indexOf(selID); i >= 0 {
			l.sel = i
		}
	}
	l.ensureVisible(a.cfg.ReduceMotion)

	// Unread total for the window title (all non-archived, unmuted chats).
	if all, err := a.svc.Chats(ctx, core.ChatFilter{}); err == nil {
		n := 0
		now := a.now()
		for _, c := range all {
			if !c.Muted(now) {
				n += c.Unread
			}
		}
		a.unreadTotal = n
	}
}

// ---- actions ------------------------------------------------------------------

func (a *App) listMove(delta int) {
	l := a.list
	if len(l.chats) == 0 {
		return
	}
	l.sel = clamp(l.sel+delta, 0, len(l.chats)-1)
	l.ensureVisible(a.cfg.ReduceMotion)
}

func (a *App) listSelect(i int) {
	l := a.list
	if len(l.chats) == 0 {
		return
	}
	l.sel = clamp(i, 0, len(l.chats)-1)
	l.ensureVisible(a.cfg.ReduceMotion)
}

// listNextUnread selects (and opens) the next/previous chat with unread
// messages, wrapping around.
func (a *App) listNextUnread(dir int) tea.Cmd {
	l := a.list
	n := len(l.chats)
	if n == 0 {
		return nil
	}
	for k := 1; k <= n; k++ {
		i := ((l.sel+dir*k)%n + n) % n
		c := l.chats[i]
		if c.Unread > 0 || c.MarkedUnread {
			a.listSelect(i)
			return a.openChat(c.ID, nil, false)
		}
	}
	return a.toast(ui.ToastInfo, "No unread chats")
}

func (a *App) switchTab(delta int) {
	l := a.list
	if len(l.tabs) == 0 {
		return
	}
	l.archived = false
	l.tab = ((l.tab+delta)%len(l.tabs) + len(l.tabs)) % len(l.tabs)
	l.offset = 0
	l.pos, l.vel = 0, 0
	// Select the open chat if the new folder has it, else keep the selected
	// chat (refreshChats keeps it by id), else the top.
	var sel core.ChatID
	if c := l.current(); c != nil {
		sel = c.ID
	}
	a.refreshChats()
	switch i := l.indexOf(a.conv.chat.ID); {
	case a.conv.open && i >= 0:
		l.sel = i
	case l.indexOf(sel) < 0:
		l.sel = 0
	}
	l.ensureVisible(a.cfg.ReduceMotion)
}

// targetChat is the chat that chat actions (za, zm, ...) apply to: the
// selection in the list when it has focus, otherwise the open chat.
func (a *App) targetChat() *core.Chat {
	if a.focus == FocusChatList {
		return a.list.current()
	}
	if a.conv.open {
		c := a.conv.chat
		return &c
	}
	return a.list.current()
}

func (a *App) chatUpdate(c *core.Chat, u core.ChatUpdate, ok string) tea.Cmd {
	if c == nil {
		return nil
	}
	id := c.ID
	return a.do("update chat", func(ctx contextT) error { return a.svc.UpdateChat(ctx, id, u) }, ok)
}

func (a *App) toggleArchive() tea.Cmd {
	c := a.targetChat()
	if c == nil {
		return nil
	}
	v := !c.Archived
	msg := "Archived " + clean(c.Title)
	if !v {
		msg = "Unarchived " + clean(c.Title)
	}
	return a.chatUpdate(c, core.ChatUpdate{Archived: &v}, msg)
}

func (a *App) togglePin() tea.Cmd {
	c := a.targetChat()
	if c == nil {
		return nil
	}
	v := !c.Pinned
	msg := "Pinned " + clean(c.Title)
	if !v {
		msg = "Unpinned " + clean(c.Title)
	}
	return a.chatUpdate(c, core.ChatUpdate{Pinned: &v}, msg)
}

func (a *App) toggleMute(d time.Duration, set bool) tea.Cmd {
	c := a.targetChat()
	if c == nil {
		return nil
	}
	var until time.Time
	var msg string
	muted := c.Muted(a.now())
	switch {
	case set && d == 0:
		msg = "Unmuted " + clean(c.Title)
	case set:
		until = a.now().Add(d)
		msg = "Muted " + clean(c.Title) + " for " + format.FormatDurationLong(d)
	case muted:
		msg = "Unmuted " + clean(c.Title)
	default:
		until = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
		msg = "Muted " + clean(c.Title)
	}
	return a.chatUpdate(c, core.ChatUpdate{MutedUntil: &until}, msg)
}

func (a *App) toggleMarkUnread() tea.Cmd {
	c := a.targetChat()
	if c == nil {
		return nil
	}
	v := !(c.MarkedUnread || c.Unread > 0)
	if !v {
		id := c.ID
		return tea.Batch(
			a.chatUpdate(c, core.ChatUpdate{MarkedUnread: &v}, ""),
			a.do("mark read", func(ctx contextT) error { return a.svc.MarkRead(ctx, id, core.MessageID{}) }, "Marked as read"),
		)
	}
	return a.chatUpdate(c, core.ChatUpdate{MarkedUnread: &v}, "Marked as unread")
}

// ---- typing -------------------------------------------------------------------

func (a *App) setTyping(ev core.TypingChanged) {
	if ev.Who == a.self.ACI && a.self.ACI != "" {
		return
	}
	m := a.list.typing[ev.Chat]
	if !ev.Typing {
		if m != nil {
			delete(m, ev.Who)
			if len(m) == 0 {
				delete(a.list.typing, ev.Chat)
			}
		}
		return
	}
	if m == nil {
		m = map[core.ACI]typer{}
		a.list.typing[ev.Chat] = m
	}
	m[ev.Who] = typer{name: clean(ev.Name), until: a.now().Add(15 * time.Second)}
}

func (a *App) expireTyping() {
	now := a.now()
	for chat, m := range a.list.typing {
		for who, t := range m {
			if now.After(t.until) {
				delete(m, who)
			}
		}
		if len(m) == 0 {
			delete(a.list.typing, chat)
		}
	}
}

// typingNames lists who is typing in chat.
func (a *App) typingNames(chat core.ChatID) []string {
	m := a.list.typing[chat]
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for _, t := range m {
		names = append(names, t.name)
	}
	sortStrings(names)
	return names
}

// ---- rendering ----------------------------------------------------------------

// renderChatList renders the inner area (without border) of the chat list.
func (a *App) renderChatList(w, h int, x0, y0 int) []string {
	st := a.styles
	l := a.list
	lines := make([]string, 0, h)

	lines = append(lines, a.renderTabs(w, x0, y0))
	lines = append(lines, hrule(st, w))
	filtering := a.keys.Mode() == keymap.Search && a.search.scope == scopeList
	if l.archived || l.filter != "" || filtering {
		var s string
		switch {
		case filtering:
			s = st.KeyHintKey.Render(" / ") + st.App.Render(clean(a.search.input.Value()))
		case l.filter != "":
			s = st.KeyHintKey.Render(" / ") + st.App.Render(clean(l.filter)) + st.Subtitle.Render("  esc clears")
		}
		if l.archived {
			s = st.WarningText.Render(" "+a.icons.Archived+" Archived") + s
		}
		lines = append(lines, fit(s, w))
	}
	header := len(lines)
	slots := max((h-header)/chatItemHeight, 0)
	l.height = max(slots, 1)
	l.ensureVisibleNoAnim()

	if len(l.chats) == 0 {
		msg := "No chats"
		switch {
		case l.filter != "":
			msg = "No chats match"
		case l.archived:
			msg = "No archived chats"
		}
		lines = append(lines, "", fit(lipgloss.PlaceHorizontal(w, lipgloss.Center, st.Subtitle.Render(msg)), w))
		return fitLines(lines, w, h)
	}

	off := clamp(l.visibleOffset(), 0, max(len(l.chats)-1, 0))
	focused := a.focus == FocusChatList
	for i := off; i < len(l.chats) && len(lines)+chatItemHeight <= h; i++ {
		y := y0 + len(lines)
		item := a.renderChatItem(&l.chats[i], w, i == l.sel, focused)
		a.zones.add(zone{x: x0, y: y, w: w, h: chatItemHeight, kind: zoneChat, chat: l.chats[i].ID, index: i})
		lines = append(lines, item...)
	}
	// Scroll position hint at the bottom right when there is more below.
	return fitLines(lines, w, h)
}

func (l *chatList) ensureVisibleNoAnim() {
	h := max(l.height, 1)
	if l.sel < l.offset {
		l.offset = l.sel
	}
	if l.sel >= l.offset+h {
		l.offset = l.sel - h + 1
	}
	l.offset = clamp(l.offset, 0, max(len(l.chats)-h, 0))
}

// renderTabs renders the folder tabs and records a click zone for each
// visible tab (the "‹" marker selects the previous folder).
func (a *App) renderTabs(w, x0, y0 int) string {
	st := a.styles
	p := st.Theme.Palette
	active := lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Underline(true)
	inactive := lipgloss.NewStyle().Foreground(p.FgMuted)
	parts := make([]string, 0, len(a.list.tabs))
	for i, t := range a.list.tabs {
		if i == a.list.tab && !a.list.archived {
			parts = append(parts, active.Render(t.name))
		} else {
			parts = append(parts, inactive.Render(t.name))
		}
	}
	s := " " + strings.Join(parts, "  ")
	start := 0
	if width(s) > w {
		// Keep the active tab visible on narrow lists.
		start = a.list.tab
		s = " "
		if start > 0 {
			s += inactive.Render("‹ ")
			a.zones.add(zone{x: x0 + 1, y: y0, w: 1, h: 1, kind: zoneFolder, index: start - 1})
		}
		s += strings.Join(parts[start:], "  ")
	}
	x := width(s) - width(strings.Join(parts[start:], "  "))
	for i := start; i < len(parts) && x < w; i++ {
		tw := min(width(parts[i]), w-x)
		a.zones.add(zone{x: x0 + x, y: y0, w: tw, h: 1, kind: zoneFolder, index: i})
		x += width(parts[i]) + 2
	}
	return fitEllipsis(s, w)
}

// renderChatItem renders one chat as two rows.
func (a *App) renderChatItem(c *core.Chat, w int, selected, focused bool) []string {
	st := a.styles
	p := st.Theme.Palette
	now := a.now()
	mono := st.Theme.Mono

	bg := func(s lipgloss.Style) lipgloss.Style {
		if selected {
			if mono {
				return s.Reverse(true)
			}
			return s.Background(p.BgSelected)
		}
		return s
	}
	base := bg(lipgloss.NewStyle().Foreground(p.Fg))

	unread := c.Unread > 0 || c.MarkedUnread
	title := clean(c.Title)
	if c.Kind == core.ChatNoteToSelf {
		title = "Note to Self"
	}

	// Gutter: selection bar.
	gutter := base.Render(" ")
	if selected {
		gs := lipgloss.NewStyle().Foreground(p.Accent)
		if !focused {
			gs = lipgloss.NewStyle().Foreground(p.FgSubtle)
		}
		gutter = bg(gs).Render(a.icons.Cursor)
	}

	// Avatar: 4x2 tile.
	av1, av2 := a.avatarTile(c, selected)

	inner := w - 1 - 4 - 1 // gutter, avatar, space
	if inner < 4 {
		inner = max(w-1, 1)
		av1, av2 = "", ""
	}

	// Row 1: title ... [icons] time
	ts := ""
	if c.LastMessage != nil && !c.LastMessage.At.IsZero() {
		ts = a.tf.RelativeTime(c.LastMessage.At, now)
	}
	tstyle := bg(st.Timestamp)
	if unread && !mono {
		tstyle = bg(lipgloss.NewStyle().Foreground(p.Accent))
	}
	right := ""
	if c.Pinned {
		right += bg(st.PinnedIcon).Render(a.icons.Pinned) + base.Render(" ")
	}
	if c.Muted(now) {
		right += bg(st.MutedIcon).Render(a.icons.Muted) + base.Render(" ")
	}
	right += tstyle.Render(ts) + base.Render(" ")
	tst := bg(lipgloss.NewStyle().Foreground(p.Fg))
	if unread || selected {
		tst = tst.Bold(true)
	}
	icon := ""
	if c.Kind == core.ChatGroup {
		icon = bg(lipgloss.NewStyle().Foreground(p.FgSubtle)).Render(a.icons.Group) + base.Render(" ")
	}
	if c.Contact != nil && c.Contact.Verified {
		title += " "
	}
	t1 := icon + tst.Render(fitEllipsisRaw(title, max(inner-width(right)-width(icon)-1, 1)))
	if c.Contact != nil && c.Contact.Verified {
		t1 += bg(st.SuccessText).Render(a.icons.Verified)
	}
	row1 := padStyled(t1, inner-width(right), base) + right

	// Row 2: snippet ... badges
	badges := ""
	if c.MessageRequest {
		badges += bg(st.WarningText).Render("request") + base.Render(" ")
	}
	if c.MentionsUnread > 0 {
		badges += st.MentionBadge.Render("@") + base.Render(" ")
	}
	switch {
	case c.Unread > 0:
		n := itoa(c.Unread)
		if c.Unread > 999 {
			n = "999+"
		}
		bs := st.UnreadBadge
		if c.Muted(now) && !mono {
			bs = bs.Background(p.FgSubtle)
		}
		badges += bs.Render(n) + base.Render(" ")
	case c.MarkedUnread:
		badges += bg(lipgloss.NewStyle().Foreground(p.Accent)).Render(a.icons.Unread) + base.Render(" ")
	}
	snipW := max(inner-width(badges)-1, 1)
	snip := a.chatSnippet(c, snipW, bg)
	row2 := padStyled(snip, inner-width(badges), base) + badges

	sp := base.Render(" ")
	return []string{
		fit(gutter+av1+sp+row1, w),
		fit(gutter+av2+sp+row2, w),
	}
}

// avatarTile renders a 4x2 avatar: a colored tile with initials, or a small
// image when the terminal draws real images in cells.
func (a *App) avatarTile(c *core.Chat, selected bool) (string, string) {
	st := a.styles
	if a.cfg.ShowAvatars && c.AvatarID != "" && a.images.avatarsAsImages() {
		rows, ok := a.images.Rows(ui.ImageRef{Kind: ui.ImageAvatar, AvatarID: c.AvatarID}, 4, 2)
		if ok && len(rows) == 2 {
			return rows[0], rows[1]
		}
	}
	name := c.Title
	id := string(c.ID)
	if c.Kind == core.ChatNoteToSelf {
		name = "Note Self"
	}
	if c.Contact != nil {
		id = string(c.Contact.ACI)
	}
	av := st.Avatar(c.Color, id)
	top := av.Render(theme.Initials(clean(name)))
	var bottom string
	if st.Theme.Mono {
		// A one-row chip: a second reversed row would merge the avatars
		// of consecutive chats into one solid column.
		bottom = spaces(width(top))
		if selected {
			bottom = lipgloss.NewStyle().Reverse(true).Render(bottom)
		}
	} else {
		col := st.Theme.Palette.AvatarColor(c.Color, id)
		bs := lipgloss.NewStyle().Foreground(col)
		if selected {
			bs = bs.Background(st.Theme.Palette.BgSelected)
		}
		bottom = bs.Render("▀▀▀▀")
	}
	return top, bottom
}

// chatSnippet renders the preview line: typing indicator, draft, or the last
// message with sender / delivery status.
func (a *App) chatSnippet(c *core.Chat, w int, bg func(lipgloss.Style) lipgloss.Style) string {
	st := a.styles
	p := st.Theme.Palette
	muted := bg(st.ChatListPreview)
	if names := a.typingNames(c.ID); len(names) > 0 {
		who := "typing "
		if c.Kind == core.ChatGroup {
			who = firstWord(names[0]) + " is typing "
			if len(names) > 1 {
				who = itoa(len(names)) + " typing "
			}
		}
		frame := a.icons.TypingFrame(a.tick)
		if a.cfg.ReduceMotion {
			frame = a.icons.Typing[0]
		}
		return bg(st.Typing).Render(fitEllipsisRaw(who, max(w-width(frame), 1))) + bg(st.Typing).Render(frame)
	}
	if d := strings.TrimSpace(c.Draft); d != "" && !(a.conv.open && a.conv.chat.ID == c.ID) {
		pre := bg(st.ErrorText).Render("Draft: ")
		return pre + muted.Render(format.Snippet(d, nil, nil, max(w-7, 1)))
	}
	lm := c.LastMessage
	if lm == nil {
		if c.MessageRequest {
			return muted.Render("Message request")
		}
		return muted.Render("No messages yet")
	}
	prefix := ""
	switch {
	case lm.Outgoing:
		if ic := a.icons.Delivery(lm.Status); ic != "" {
			ds := bg(st.Delivery(lm.Status))
			prefix = ds.Render(ic) + muted.Render(" ")
		}
		prefix += bg(lipgloss.NewStyle().Foreground(p.FgMuted)).Render("You: ")
	case c.Kind == core.ChatGroup && lm.Sender != "":
		prefix = bg(lipgloss.NewStyle().Foreground(p.FgMuted).Bold(true)).Render(clean(firstWord(lm.Sender)) + ": ")
	}
	text := format.Snippet(lm.Text, nil, nil, max(w-width(prefix), 1))
	ts := muted
	if (c.Unread > 0) && !st.Theme.Mono {
		ts = bg(lipgloss.NewStyle().Foreground(p.Fg))
	}
	return prefix + ts.Render(clean(text))
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
