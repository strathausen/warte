package info

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// MaxMedia is how many recent photos the sidebar's media strip shows.
const MaxMedia = 6

// mediaScan is how many recent messages are scanned for the media strip.
const mediaScan = 400

// Ex commands the sidebar emits (as ui.RunCommandMsg) for actions the App
// or other view packages implement. They are variables so the integrator
// can point them at whatever command a sibling package registers.
var (
	SearchCommand        = "search"
	MuteCommand          = "mute"
	UnmuteCommand        = "unmute"
	DisappearCommand     = "disappear"
	BlockCommand         = "block"
	UnblockCommand       = "unblock"
	SafetyNumberCommand  = "safety"
	GroupSettingsCommand = "group settings"
)

// sidebarAction identifies one entry of the sidebar's action list.
type sidebarAction int

const (
	actSearch sidebarAction = iota
	actMute
	actDisappear
	actSafety
	actBlock
	actGroupSettings
)

type itemKind int

const (
	itemMedia itemKind = iota
	itemAction
	itemGroup
	itemMember
)

type sideItem struct {
	kind   itemKind
	action sidebarAction
	chat   core.ChatID // group (itemGroup) or direct chat (itemMember)
}

type mediaItem struct {
	msg core.MessageID
	att core.Attachment
}

// sidebarLoaded carries the result of Sidebar.load.
type sidebarLoaded struct {
	gen    int
	chat   core.Chat
	err    error
	shared []core.Chat
	media  []mediaItem
	total  int
}

// Sidebar is the chat info pane shown to the right of the conversation. It
// is a pane component, not an overlay: the App owns its border and feeds it
// messages while it is visible.
//
//   - SetChat(id) selects the chat and returns the command loading it.
//   - Update handles ui.ActionMsg (cursor movement, ActItemSelect),
//     tea.KeyPressMsg (j/k/h/l/G/enter as a fallback), ui.EventMsg (keeps
//     itself fresh) and its own load results.
//   - View(width, height) renders exactly height lines of width cells.
//
// Selectable items are the media strip (h/l pick a thumbnail), the actions,
// the groups in common (direct chats) and the members (groups).
type Sidebar struct {
	ctx     ui.Context
	chatID  core.ChatID
	chat    *core.Chat
	err     error
	loading bool
	shared  []core.Chat
	media   []mediaItem
	total   int
	gen     int
	focused bool

	items  []sideItem
	sel    int
	thumb  int
	offset int
	height int
}

// NewSidebar returns an empty sidebar; call SetChat to fill it.
func NewSidebar(ctx ui.Context) *Sidebar {
	return &Sidebar{ctx: ctx}
}

// KeyContext is the keymap context to feed keys in while the sidebar has
// focus.
func (s *Sidebar) KeyContext() keymap.Context { return keymap.Sidebar }

// Focus gives the sidebar keyboard focus (shows the selection highlight).
func (s *Sidebar) Focus() { s.focused = true }

// Blur removes focus.
func (s *Sidebar) Blur() { s.focused = false }

// Focused reports whether the sidebar has focus.
func (s *Sidebar) Focused() bool { return s.focused }

// ChatID returns the chat shown ("" when none).
func (s *Sidebar) ChatID() core.ChatID { return s.chatID }

// Chat returns the loaded chat, or nil while loading.
func (s *Sidebar) Chat() *core.Chat { return s.chat }

// SetChat shows id and returns the command that loads it. Setting the same
// chat again just refreshes it; "" clears the sidebar.
func (s *Sidebar) SetChat(id core.ChatID) tea.Cmd {
	if id != s.chatID {
		s.chat, s.shared, s.media, s.total, s.err = nil, nil, nil, 0, nil
		s.sel, s.thumb, s.offset = 0, 0, 0
		s.items = nil
	}
	s.chatID = id
	if id == "" {
		s.gen++
		s.loading = false
		return nil
	}
	return s.Refresh()
}

// Refresh reloads the current chat.
func (s *Sidebar) Refresh() tea.Cmd {
	if s.chatID == "" || s.ctx.Svc == nil {
		return nil
	}
	s.gen++
	s.loading = true
	gen, id, svc := s.gen, s.chatID, s.ctx.Svc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res := sidebarLoaded{gen: gen}
		c, err := svc.Chat(ctx, id)
		if err != nil {
			res.err = err
			return res
		}
		res.chat = c
		if c.Kind == core.ChatDirect && c.Contact != nil {
			res.shared = sharedGroups(ctx, svc, c.Contact.ACI)
		}
		if msgs, err := svc.Messages(ctx, id, core.MessageID{}, mediaScan); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				m := msgs[i]
				if m.RemoteDeleted || m.ViewOnce || m.Kind != core.KindNormal {
					continue
				}
				for _, a := range m.Attachments {
					if !a.IsImage() || a.Borderless {
						continue
					}
					res.total++
					if len(res.media) < MaxMedia {
						res.media = append(res.media, mediaItem{msg: m.ID, att: a})
					}
				}
			}
		}
		return res
	}
}

// sharedGroups lists the groups (archived or not) that aci is a member of.
func sharedGroups(ctx context.Context, svc core.Service, aci core.ACI) []core.Chat {
	var out []core.Chat
	seen := map[core.ChatID]bool{}
	for _, archived := range []bool{false, true} {
		list, err := svc.Chats(ctx, core.ChatFilter{GroupsOnly: true, Archived: archived, IncludeEmpty: true, IncludeBlocked: true})
		if err != nil {
			continue
		}
		for _, c := range list {
			if seen[c.ID] {
				continue
			}
			for _, m := range c.Members {
				if m.ACI == aci {
					out = append(out, c)
					seen[c.ID] = true
					break
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
	return out
}

// Update handles a message and returns the sidebar and a command.
func (s *Sidebar) Update(msg tea.Msg) (*Sidebar, tea.Cmd) {
	switch msg := msg.(type) {
	case sidebarLoaded:
		if msg.gen != s.gen {
			return s, nil
		}
		s.loading = false
		if msg.err != nil {
			s.err = msg.err
			return s, nil
		}
		c := msg.chat
		s.chat, s.err = &c, nil
		s.shared, s.media, s.total = msg.shared, msg.media, msg.total
		s.rebuild()
	case ui.EventMsg:
		return s, s.handleEvent(msg.Event)
	case ui.ActionMsg:
		return s, s.act(msg.Action, msg.Count)
	case tea.KeyPressMsg:
		if a, ok := keyAction(msg.String()); ok {
			return s, s.act(a, 1)
		}
	}
	return s, nil
}

func (s *Sidebar) handleEvent(ev core.Event) tea.Cmd {
	if s.chatID == "" {
		return nil
	}
	switch ev := ev.(type) {
	case core.ChatUpdated:
		if ev.Chat.ID == s.chatID {
			c := ev.Chat
			s.chat = &c
			s.rebuild()
		} else if s.chat != nil && s.chat.Kind == core.ChatDirect && s.chat.Contact != nil && ev.Chat.Kind == core.ChatGroup {
			// Membership in a shared group may have changed. Groups are
			// updated on every message, so patch the list locally instead
			// of reloading (and rescanning media) each time.
			s.patchShared(ev.Chat)
		}
	case core.ChatRemoved:
		if ev.ID == s.chatID {
			return s.SetChat("")
		}
		for i := range s.shared {
			if s.shared[i].ID == ev.ID {
				s.shared = append(s.shared[:i:i], s.shared[i+1:]...)
				s.rebuild()
				break
			}
		}
	case core.ContactUpdated:
		if s.chat != nil && (s.chatID.ACI() == ev.Contact.ACI || s.hasMember(ev.Contact.ACI)) {
			return s.Refresh()
		}
	case core.MessageAdded:
		if ev.Message.ID.Chat == s.chatID && hasImage(ev.Message) {
			return s.Refresh()
		}
	case core.MessageUpdated:
		if ev.Message.ID.Chat == s.chatID && (hasImage(ev.Message) || ev.Message.RemoteDeleted) && s.inMedia(ev.Message.ID) {
			return s.Refresh()
		}
	case core.MessageRemoved:
		if s.inMedia(ev.ID) {
			return s.Refresh()
		}
	}
	return nil
}

// patchShared adds, updates or removes group g in the groups-in-common
// list depending on whether the direct chat's contact is a member.
func (s *Sidebar) patchShared(g core.Chat) {
	if len(g.Members) == 0 {
		// A group always has members; an empty list means the event
		// carries no membership information.
		return
	}
	aci := s.chat.Contact.ACI
	member := false
	for _, m := range g.Members {
		if m.ACI == aci {
			member = true
			break
		}
	}
	idx := -1
	for i := range s.shared {
		if s.shared[i].ID == g.ID {
			idx = i
			break
		}
	}
	switch {
	case member && idx >= 0:
		s.shared[idx] = g
	case member:
		s.shared = append(s.shared, g)
	case idx >= 0:
		s.shared = append(s.shared[:idx:idx], s.shared[idx+1:]...)
	default:
		return
	}
	sort.SliceStable(s.shared, func(i, j int) bool {
		return strings.ToLower(s.shared[i].Title) < strings.ToLower(s.shared[j].Title)
	})
	s.rebuild()
}

func hasImage(m core.Message) bool {
	for _, a := range m.Attachments {
		if a.IsImage() {
			return true
		}
	}
	return false
}

func (s *Sidebar) inMedia(id core.MessageID) bool {
	for _, m := range s.media {
		if m.msg == id {
			return true
		}
	}
	return false
}

func (s *Sidebar) hasMember(aci core.ACI) bool {
	for _, m := range s.chat.Members {
		if m.ACI == aci {
			return true
		}
	}
	return false
}

// actions returns the action list for the loaded chat.
func (s *Sidebar) actions() []sidebarAction {
	if s.chat == nil {
		return nil
	}
	switch s.chat.Kind {
	case core.ChatNoteToSelf:
		return []sidebarAction{actSearch, actDisappear}
	case core.ChatGroup:
		return []sidebarAction{actSearch, actMute, actDisappear, actGroupSettings, actBlock}
	}
	return []sidebarAction{actSearch, actMute, actDisappear, actSafety, actBlock}
}

// rebuild recomputes the selectable items, keeping the selection on the
// same item where possible.
func (s *Sidebar) rebuild() {
	var prev *sideItem
	if s.sel < len(s.items) {
		p := s.items[s.sel]
		prev = &p
	}
	s.items = s.items[:0]
	if len(s.media) > 0 {
		s.items = append(s.items, sideItem{kind: itemMedia})
	}
	firstAction := len(s.items)
	for _, a := range s.actions() {
		s.items = append(s.items, sideItem{kind: itemAction, action: a})
	}
	if s.chat != nil {
		switch s.chat.Kind {
		case core.ChatDirect:
			for _, g := range s.shared {
				s.items = append(s.items, sideItem{kind: itemGroup, chat: g.ID})
			}
		case core.ChatGroup:
			for _, m := range s.sortedMembers() {
				s.items = append(s.items, sideItem{kind: itemMember, chat: core.DirectChatID(m.ACI)})
			}
		}
	}
	s.thumb = clamp(s.thumb, 0, max(len(s.media)-1, 0))
	if prev != nil {
		for i, it := range s.items {
			if it == *prev {
				s.sel = i
				return
			}
		}
	}
	if len(s.items) == 0 {
		s.sel = 0
		return
	}
	if prev == nil {
		s.sel = min(firstAction, len(s.items)-1)
		return
	}
	s.sel = clamp(s.sel, 0, len(s.items)-1)
}

// sortedMembers orders group members like Signal: you, admins, then the
// rest alphabetically.
func (s *Sidebar) sortedMembers() []core.Member {
	if s.chat == nil {
		return nil
	}
	ms := append([]core.Member(nil), s.chat.Members...)
	self := s.ctx.Self.ACI
	rank := func(m core.Member) int {
		switch {
		case m.ACI == self && self != "":
			return 0
		case m.Admin:
			return 1
		}
		return 2
	}
	sort.SliceStable(ms, func(i, j int) bool {
		ri, rj := rank(ms[i]), rank(ms[j])
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(ms[i].Name) < strings.ToLower(ms[j].Name)
	})
	return ms
}

func (s *Sidebar) act(a keymap.Action, count int) tea.Cmd {
	if len(s.items) == 0 {
		return nil
	}
	cur := s.items[s.sel]
	switch a {
	case keymap.ActCursorLeft, keymap.ActCursorRight:
		if cur.kind == itemMedia {
			d := max(count, 1)
			if a == keymap.ActCursorLeft {
				d = -d
			}
			s.thumb = clamp(s.thumb+d, 0, len(s.media)-1)
		}
		return nil
	case keymap.ActItemSelect, keymap.ActChatOpen:
		return s.activate(cur)
	}
	s.sel, _ = moveAction(a, count, s.sel, len(s.items), s.height)
	return nil
}

func runCmd(line string) tea.Cmd {
	return func() tea.Msg { return ui.RunCommandMsg{Line: line} }
}

func (s *Sidebar) activate(it sideItem) tea.Cmd {
	switch it.kind {
	case itemMedia:
		if s.thumb < len(s.media) {
			id := s.media[s.thumb].msg
			return func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }
		}
	case itemGroup, itemMember:
		id := it.chat
		if it.kind == itemMember && id.ACI() == s.ctx.Self.ACI {
			return nil
		}
		return func() tea.Msg { return ui.OpenChatMsg{Chat: id} }
	case itemAction:
		c := s.chat
		if c == nil {
			return nil
		}
		switch it.action {
		case actSearch:
			return runCmd(SearchCommand)
		case actMute:
			if c.Muted(now(s.ctx)) {
				return runCmd(UnmuteCommand)
			}
			return ui.Push(NewMutePicker(s.ctx))
		case actDisappear:
			return ui.Push(NewTimerPicker(s.ctx, c.ExpireTimer))
		case actSafety:
			return runCmd(SafetyNumberCommand)
		case actBlock:
			if s.blocked() {
				return runCmd(UnblockCommand)
			}
			return runCmd(BlockCommand)
		case actGroupSettings:
			return runCmd(GroupSettingsCommand)
		}
	}
	return nil
}

func (s *Sidebar) blocked() bool {
	if s.chat == nil {
		return false
	}
	return s.chat.Blocked || (s.chat.Contact != nil && s.chat.Contact.Blocked)
}

// View renders exactly height lines of width cells.
func (s *Sidebar) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	s.height = height
	lines, spans := s.render(width)
	// Keep the selected item visible.
	if s.sel < len(spans) {
		top, bot := spans[s.sel][0], spans[s.sel][1]
		if top < s.offset {
			s.offset = top
		}
		if bot >= s.offset+height {
			s.offset = bot - height + 1
		}
		if s.sel == 0 {
			// Show the header when the first item is selected.
			s.offset = min(s.offset, max(bot-height+1, 0))
		}
	}
	s.offset = clamp(s.offset, 0, max(len(lines)-height, 0))
	return fitLines(lines[min(s.offset, len(lines)):], width, height)
}

// render builds all content lines and, for each item, its [first, last]
// line.
func (s *Sidebar) render(w int) ([]string, [][2]int) {
	st := s.ctx.Styles
	if s.chat == nil {
		var msg string
		switch {
		case s.chatID == "":
			msg = "No chat selected"
		case s.err != nil:
			return []string{"", center(st.ErrorText.Render("Couldn't load chat"), w), center(st.Subtitle.Render(trunc(clean(s.err.Error()), w-2)), w)}, nil
		default:
			msg = "Loading…"
		}
		return []string{"", center(st.Subtitle.Render(msg), w)}, nil
	}
	c := s.chat
	spans := make([][2]int, len(s.items))
	var lines []string
	add := func(l ...string) { lines = append(lines, l...) }

	add(s.header(w)...)
	add("")
	add(s.details(w)...)

	idx := 0
	if len(s.media) > 0 {
		add("", section(st, "Media", itoa(s.total), w))
		start := len(lines)
		add(s.mediaStrip(w)...)
		spans[idx] = [2]int{start, len(lines) - 1}
		idx++
	}

	add("", section(st, "Actions", "", w))
	for _, a := range s.actions() {
		spans[idx] = [2]int{len(lines), len(lines)}
		add(s.itemRow(idx, s.actionLabel(a), w))
		idx++
	}

	switch c.Kind {
	case core.ChatDirect:
		add("", section(st, "Groups in common", itoa(len(s.shared)), w))
		if len(s.shared) == 0 {
			add(fit("   "+st.Subtitle.Render("None"), w))
		}
		for _, g := range s.shared {
			row := st.RenderAvatar(g.Color, string(g.ID), clean(g.Title)) + " " + trunc(clean(g.Title), w-9)
			spans[idx] = [2]int{len(lines), len(lines)}
			add(s.itemRow(idx, row, w))
			idx++
		}
	case core.ChatGroup:
		add("", section(st, "Members", itoa(len(c.Members)), w))
		for _, m := range s.sortedMembers() {
			spans[idx] = [2]int{len(lines), len(lines)}
			add(s.itemRow(idx, s.memberRow(m, w-3), w))
			idx++
		}
		if len(c.RequestingJoin) > 0 {
			add("", section(st, "Requests", itoa(len(c.RequestingJoin)), w))
			for _, m := range c.RequestingJoin {
				add(fit("   "+st.Subtitle.Render(trunc(clean(m.Name), w-4)), w))
			}
		}
		if len(c.PendingMembers) > 0 {
			add("", section(st, "Invited", itoa(len(c.PendingMembers)), w))
			for _, m := range c.PendingMembers {
				add(fit("   "+st.Subtitle.Render(trunc(clean(m.Name), w-4)), w))
			}
		}
	}
	add("")
	return lines, spans
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *Sidebar) itemRow(i int, content string, w int) string {
	if i == s.sel && s.focused {
		return selectedRow(s.ctx.Styles, s.ctx.Icons, content, w, true)
	}
	return listRow(s.ctx.Icons, content, w)
}

func (s *Sidebar) title() string {
	c := s.chat
	if c.Kind == core.ChatNoteToSelf {
		return "Note to Self"
	}
	if c.Contact != nil {
		if n := c.Contact.DisplayName(); n != "" {
			return clean(n)
		}
	}
	if t := clean(c.Title); t != "" {
		return t
	}
	return "Unknown"
}

// header renders the avatar, name and subtitle.
func (s *Sidebar) header(w int) []string {
	st := s.ctx.Styles
	p := st.Theme.Palette
	c := s.chat
	title := s.title()
	id := string(c.ID)
	if c.Contact != nil {
		id = string(c.Contact.ACI)
	}
	lines := []string{""}
	shown := false
	if img := s.ctx.Images; c.AvatarID != "" && img != nil && img.Inline() {
		ref := ui.ImageRef{Kind: ui.ImageAvatar, AvatarID: c.AvatarID, Attachment: core.Attachment{Width: 1, Height: 1}, Label: theme.Initials(title)}
		cols, rows := img.Size(ref, min(12, w-2), 6)
		if cols > 0 && rows > 0 {
			rs, _ := img.Rows(ref, cols, rows)
			for _, r := range rs {
				lines = append(lines, center(r, w))
			}
			shown = true
		}
	}
	if !shown {
		tw, th := min(12, max(w-2, 4)), 5
		if w < 30 {
			tw, th = min(8, max(w-2, 4)), 3
		}
		for _, r := range avatarTile(st, c.Color, id, title, tw, th) {
			lines = append(lines, center(r, w))
		}
	}
	lines = append(lines, "")
	name := lipgloss.NewStyle().Bold(true).Foreground(p.Fg).Render(trunc(title, w-4))
	if c.Contact != nil && c.Contact.Verified && !c.Contact.IsSelf {
		name += " " + st.SuccessText.Render(s.ctx.Icons.Verified)
	}
	lines = append(lines, center(name, w))

	var sub []string
	switch c.Kind {
	case core.ChatNoteToSelf:
		sub = []string{"Only you can see this chat"}
	case core.ChatGroup:
		sub = []string{"Group · " + plural(len(c.Members), "member")}
	default:
		if c.Contact != nil {
			about := strings.TrimSpace(clean(c.Contact.AboutEmoji) + " " + clean(c.Contact.About))
			if about != "" {
				sub = format.Wrap(about, max(w-4, 1))
				if len(sub) > 3 {
					sub = append(sub[:2], trunc(sub[2]+" …", w-4))
				}
			}
		}
	}
	for _, l := range sub {
		lines = append(lines, center(st.Subtitle.Render(l), w))
	}
	if c.Kind == core.ChatGroup {
		if d := strings.TrimSpace(theme.Sanitize(c.Description)); d != "" {
			lines = append(lines, "")
			for _, l := range format.Wrap(d, max(w-4, 1)) {
				lines = append(lines, fit("  "+st.App.Render(l), w))
			}
		}
	}
	if c.MessageRequest {
		lines = append(lines, "", center(st.WarningText.Render("Message request"), w))
	}
	return lines
}

// details renders the key/value block.
func (s *Sidebar) details(w int) []string {
	st := s.ctx.Styles
	ic := s.ctx.Icons
	c := s.chat
	t := now(s.ctx)
	tf := timeFormatter(s.ctx)
	const kw = 11
	var out []string
	row := func(k, v string) { out = append(out, kv(st, k, v, kw, w)) }

	if ct := c.Contact; ct != nil {
		if ct.Username != "" {
			row("Username", st.App.Render(clean(ct.Username)))
		}
		if ct.E164 != "" {
			row("Phone", st.App.Render(clean(ct.E164)))
		} else if !ct.IsSelf {
			row("Phone", st.Subtitle.Render("hidden"))
		}
		if !ct.IsSelf {
			if ct.Verified {
				row("Safety", st.SuccessText.Render(ic.Verified+" Verified"))
			} else {
				row("Safety", st.Subtitle.Render("Not verified"))
			}
		}
	}
	if c.Kind == core.ChatGroup {
		m := plural(len(c.Members), "member")
		if n := len(c.PendingMembers); n > 0 {
			m += st.Subtitle.Render(" +" + itoa(n) + " invited")
		}
		row("Members", st.App.Render(m))
		link := st.Subtitle.Render("Off")
		if c.InviteLink != "" && c.AddFromLinkMode != core.AccessUnsatisfiable && c.AddFromLinkMode != core.AccessUnknown {
			link = st.App.Render(ic.Link + " On")
			if c.AddFromLinkMode == core.AccessAdmin {
				link += st.Subtitle.Render(" · approval")
			}
		}
		row("Invite link", link)
		if c.OnlyAdminsSend {
			row("Posting", st.App.Render("Admins only"))
		}
		if c.SelfIsAdmin {
			row("Your role", st.InfoText.Render("Admin"))
		}
	}
	if c.Kind != core.ChatNoteToSelf {
		mute := st.Subtitle.Render("Off")
		if c.Muted(t) {
			if c.MutedUntil.Year() >= 9000 {
				mute = st.WarningText.Render(ic.Muted + " Always")
			} else {
				mute = st.WarningText.Render(ic.Muted + " Until " + tf.RelativeTime(c.MutedUntil, t))
			}
		}
		row("Muted", mute)
	}
	timer := st.Subtitle.Render("Off")
	if c.ExpireTimer > 0 {
		timer = st.App.Render(ic.Clock + " " + format.FormatDurationLong(c.ExpireTimer))
	}
	row("Disappear", timer)
	if s.blocked() {
		row("Blocked", st.ErrorText.Render("Yes"))
	}
	return out
}

func (s *Sidebar) actionLabel(a sidebarAction) string {
	st := s.ctx.Styles
	ic := s.ctx.Icons
	c := s.chat
	switch a {
	case actSearch:
		return ic.Search + "  Search in chat"
	case actMute:
		if c.Muted(now(s.ctx)) {
			return ic.Muted + "  Unmute"
		}
		return ic.Muted + "  Mute notifications"
	case actDisappear:
		l := ic.Clock + "  Disappearing messages"
		if c.ExpireTimer > 0 {
			l += st.Subtitle.Render(" · " + format.FormatDuration(c.ExpireTimer))
		}
		return l
	case actSafety:
		return ic.Lock + "  Safety number"
	case actBlock:
		if s.blocked() {
			return ic.Failed + "  Unblock"
		}
		return st.ErrorText.Render(ic.Failed + "  Block")
	case actGroupSettings:
		return ic.Group + "  Group settings"
	}
	return ""
}

func (s *Sidebar) memberRow(m core.Member, w int) string {
	st := s.ctx.Styles
	name := clean(m.Name)
	if name == "" {
		name = "Unknown"
	}
	label := name
	if m.ACI == s.ctx.Self.ACI && m.ACI != "" {
		label = "You"
	}
	badge := ""
	if m.Admin {
		badge = st.InfoText.Render("admin")
	}
	nameW := max(w-5-width(badge)-1, 1)
	row := st.RenderAvatar("", string(m.ACI), name) + " " + st.SenderStyle(string(m.ACI)).Render(trunc(label, nameW))
	if badge != "" {
		gap := w - width(row) - width(badge)
		row += spaces(max(gap, 1)) + badge
	}
	return row
}

// mediaStrip renders up to MaxMedia thumbnails in rows of 3 (2 when
// narrow), with a marker under the selected one.
func (s *Sidebar) mediaStrip(w int) []string {
	st := s.ctx.Styles
	per := 3
	if w < 30 {
		per = 2
	}
	inner := w - 2
	tw := max((inner-(per-1))/per, 2)
	th := max(min(tw/2, 6), 2)
	selected := s.focused && s.sel < len(s.items) && s.items[s.sel].kind == itemMedia
	var out []string
	for start := 0; start < len(s.media); start += per {
		end := min(start+per, len(s.media))
		cells := make([][]string, 0, per)
		for i := start; i < end; i++ {
			cells = append(cells, s.thumbnail(s.media[i], tw, th))
		}
		for r := 0; r < th; r++ {
			line := " "
			for i, cell := range cells {
				if i > 0 {
					line += " "
				}
				line += cell[r]
			}
			out = append(out, fit(line, w))
		}
		if selected && s.thumb >= start && s.thumb < end {
			pos := 1 + (s.thumb-start)*(tw+1)
			mark := st.ScrollbarThumb.Render(strings.Repeat("▔", tw))
			if st.Theme.Mono {
				mark = strings.Repeat("▔", tw)
			}
			out = append(out, fit(spaces(pos)+mark, w))
		} else if end < len(s.media) {
			out = append(out, "")
		}
	}
	return out
}

// thumbnail returns exactly th lines of tw cells.
func (s *Sidebar) thumbnail(m mediaItem, tw, th int) []string {
	st := s.ctx.Styles
	if img := s.ctx.Images; img != nil && img.Enabled() {
		ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: m.msg, Attachment: m.att, Label: "Photo"}
		cols, rows := img.Size(ref, tw, th)
		if cols > 0 && rows > 0 {
			rs, _ := img.Rows(ref, cols, rows)
			top := (th - rows) / 2
			out := make([]string, th)
			for i := range out {
				if i >= top && i-top < len(rs) {
					out[i] = center(rs[i-top], tw)
				} else {
					out[i] = spaces(tw)
				}
			}
			return out
		}
	}
	box := lipgloss.NewStyle().Background(st.Theme.Palette.BgAlt).Foreground(st.Theme.Palette.FgSubtle)
	if st.Theme.Mono {
		box = lipgloss.NewStyle().Faint(true)
	}
	out := make([]string, th)
	for i := range out {
		txt := spaces(tw)
		if i == th/2 {
			txt = center(s.ctx.Icons.Image, tw)
		}
		out[i] = box.Render(txt)
	}
	return out
}
