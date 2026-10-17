package forms

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/atotto/clipboard"

	"warte/internal/core"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// GroupSettings is the group settings page: title, description,
// disappearing timer, mute, announcement-only, invite link (on/off, admin
// approval, copy, reset, QR), members with roles (promote / demote /
// remove), pending invites, join requests (approve / deny) and leave.
//
// Admin-only rows are shown dimmed for non-admins and explain why when
// activated. Every change goes through Service.UpdateGroup (or UpdateChat,
// LeaveGroup); destructive ones ask for confirmation first.
type GroupSettings struct {
	ctx    ui.Context
	id     core.ChatID
	chat   core.Chat
	names  map[core.ACI]string
	loaded bool
	err    string
	status string
	page   page
	// leaveAsked opens the leave confirmation once loaded (:group leave).
	leaveAsked bool
}

// leaveRequestMsg asks the top GroupSettings to confirm leaving.
type leaveRequestMsg struct{}

// NewGroupSettings returns the settings overlay for a group chat.
func NewGroupSettings(ctx ui.Context, id core.ChatID) *GroupSettings {
	return &GroupSettings{ctx: ctx, id: id, names: map[core.ACI]string{}}
}

type groupLoadedMsg struct {
	id       core.ChatID
	chat     core.Chat
	contacts []core.Contact
	err      error
}

func (g *GroupSettings) load() tea.Cmd {
	svc, id := g.ctx.Svc, g.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		chat, err := svc.Chat(ctx, id)
		var contacts []core.Contact
		if err == nil {
			contacts, _ = svc.Contacts(ctx)
		}
		return groupLoadedMsg{id: id, chat: chat, contacts: contacts, err: err}
	}
}

func (g *GroupSettings) Init() tea.Cmd      { return g.load() }
func (g *GroupSettings) KeyContext() string { return ContextGroupSettings }
func (g *GroupSettings) Title() string {
	if g.chat.Title == "" {
		return "Group settings"
	}
	return clean(g.chat.Title)
}

func (g *GroupSettings) Size(maxW, maxH int) (int, int) {
	return min(64, maxW), min(max(len(g.page.rows)+g.headerHeight(min(64, maxW)), 10), maxH)
}

func (g *GroupSettings) Footer() string {
	st := g.ctx.Styles
	if r := g.page.current(); r != nil && len(r.hints) > 0 {
		return st.RenderKeyHints(append(append([]string{}, r.hints...), "esc", "close")...)
	}
	return st.RenderKeyHints("j/k", "move", "enter", "select", "esc", "close")
}

// name resolves a member's display name.
func (g *GroupSettings) name(m core.Member) string {
	if m.ACI == selfACI(g.ctx) {
		return "You"
	}
	if m.Name != "" {
		return clean(m.Name)
	}
	if n := g.names[m.ACI]; n != "" {
		return n
	}
	return "Unknown"
}

func (g *GroupSettings) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case groupLoadedMsg:
		if msg.id != g.id {
			return g, nil
		}
		g.loaded = true
		if msg.err != nil {
			g.err = msg.err.Error()
			return g, nil
		}
		g.err = ""
		g.chat = msg.chat
		for _, c := range msg.contacts {
			g.names[c.ACI] = clean(c.DisplayName())
		}
		g.rebuild()
		if g.leaveAsked {
			g.leaveAsked = false
			return g, g.leave()
		}
		return g, nil
	case leaveRequestMsg:
		if g.loaded && g.err == "" {
			return g, g.leave()
		}
		g.leaveAsked = true
		return g, nil
	case opDoneMsg:
		g.status = ""
		return g, tea.Batch(toastFor(msg), g.load())
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.ChatUpdated:
			if ev.Chat.ID == g.id {
				g.chat = ev.Chat
				g.rebuild()
			}
		case core.ChatRemoved:
			if ev.ID == g.id {
				return nil, nil
			}
		}
		return g, nil
	}
	if !g.loaded || g.err != "" {
		return g, nil
	}
	if d, ok := navDelta(msg, g.page.height); ok {
		g.page.move(d)
		g.status = ""
		return g, nil
	}
	r := g.page.current()
	if r == nil {
		return g, nil
	}
	if r.disabled && (isSelect(msg) || leftRight(msg) != 0 || r.keys[keyOf(msg)] != nil) {
		g.status = r.reason
		return g, nil
	}
	if isSelect(msg) && r.activate != nil {
		return g, r.activate()
	}
	if d := leftRight(msg); d != 0 && r.cycle != nil {
		return g, r.cycle(d)
	}
	if f := r.keys[keyOf(msg)]; f != nil {
		return g, f()
	}
	return g, nil
}

// headerHeight is the number of lines above the rows.
func (g *GroupSettings) headerHeight(w int) int {
	return len(g.header(w)) + 1
}

func (g *GroupSettings) header(w int) []string {
	st := g.ctx.Styles
	c := g.chat
	av := st.RenderAvatar(c.Color, string(c.ID), clean(c.Title))
	title := st.Title.Render(trunc(clean(c.Title), max(w-6, 1)))
	sub := itoa(len(c.Members)) + " members"
	if c.SelfIsAdmin {
		sub += " · you are an admin"
	}
	lines := []string{av + " " + title, muted(st, trunc(sub, w))}
	if d := strings.TrimSpace(strings.ReplaceAll(theme.Sanitize(c.Description), "\t", " ")); d != "" {
		wrapped := wrapText(d, w)
		if len(wrapped) > 2 {
			wrapped = wrapped[:2]
			wrapped[1] = trunc(wrapped[1], max(w-2, 1)) + " …"
		}
		for _, l := range wrapped {
			lines = append(lines, muted(st, l))
		}
	}
	return lines
}

func (g *GroupSettings) View(w, h int) string {
	st := g.ctx.Styles
	if !g.loaded {
		return muted(st, "Loading…")
	}
	if g.err != "" {
		return clampLines([]string{statusLine(st, g.err, true, w)}, w, h)
	}
	lines := g.header(w)
	lines = append(lines, "")
	if len(lines) > h/2 {
		lines = lines[:max(h/2, 1)]
	}
	listH := h - len(lines)
	if g.status != "" {
		listH--
	}
	lines = append(lines, g.page.render(st, w, listH)...)
	if g.status != "" {
		lines = append(lines, statusLine(st, g.status, false, w))
	}
	return clampLines(lines, w, h)
}

// ---- rows ------------------------------------------------------------------

const adminOnly = "Only admins can change this."

// update applies u now, showing "Saving…" until it is done.
func (g *GroupSettings) update(op, ok string, u core.GroupUpdate) tea.Cmd {
	g.status = "Saving…"
	return g.op(op, ok, u)
}

// op returns the command applying u, without touching the view state (for
// commands that only run after a confirmation, which may be cancelled).
func (g *GroupSettings) op(op, ok string, u core.GroupUpdate) tea.Cmd {
	id := g.id
	return runOp(g.ctx.Svc, op, ok, func(ctx context.Context, svc core.Service) error {
		return svc.UpdateGroup(ctx, id, u)
	})
}

func (g *GroupSettings) confirm(title, text, yes string, cmd tea.Cmd) tea.Cmd {
	return ui.Push(NewConfirm(g.ctx, title, text, yes, true, cmd))
}

func linkOn(c core.Chat) bool {
	return c.InviteLink != "" && c.AddFromLinkMode != core.AccessUnsatisfiable && c.AddFromLinkMode != core.AccessUnknown
}

func (g *GroupSettings) rebuild() {
	c := g.chat
	admin := c.SelfIsAdmin
	ic := g.ctx.Icons
	var rows []row
	add := func(r row) { rows = append(rows, r) }
	adminRow := func(r row) row {
		if !admin {
			r.disabled = true
			r.reason = adminOnly
		}
		return r
	}

	add(row{kind: rowHeader, label: "Group"})
	add(row{id: "title", kind: rowAction, label: "Name", value: muted(g.ctx.Styles, trunc(clean(c.Title), 28)),
		hints:    []string{"enter", "edit"},
		activate: g.editTitle})
	desc := "Add a description"
	if c.Description != "" {
		desc = trunc(clean(c.Description), 28)
	}
	add(row{id: "desc", kind: rowAction, label: "Description", value: muted(g.ctx.Styles, desc),
		hints: []string{"enter", "edit"}, activate: g.editDescription})
	add(row{id: "timer", kind: rowChoice, label: "Disappearing messages", value: timerLabel(c.ExpireTimer),
		hints: []string{"enter", "choose"}, activate: g.chooseTimer})
	add(row{id: "mute", kind: rowChoice, label: "Mute notifications", value: muteLabel(c.MutedUntil, nowFn(g.ctx)()),
		hints:    []string{"h/l", "change", "enter", "choose"},
		activate: func() tea.Cmd { return ui.Push(muteDialog(g.ctx, g.id)) },
		cycle:    func(d int) tea.Cmd { return g.cycleMute(d) }})
	add(adminRow(row{id: "announce", kind: rowToggle, label: "Only admins can send", on: c.OnlyAdminsSend,
		hints:    []string{"enter", "toggle"},
		activate: g.toggleAnnounce, cycle: func(int) tea.Cmd { return g.toggleAnnounce() }}))

	add(row{kind: rowBlank})
	add(row{kind: rowHeader, label: "Group link"})
	on := linkOn(c)
	add(adminRow(row{id: "link", kind: rowToggle, label: "Group link", icon: ic.Link, on: on,
		hints:    []string{"enter", "toggle"},
		activate: g.toggleLink, cycle: func(int) tea.Cmd { return g.toggleLink() }}))
	if on {
		approval := c.AddFromLinkMode == core.AccessAdmin
		add(adminRow(row{id: "approval", kind: rowToggle, label: "Approve new members", on: approval,
			hints:    []string{"enter", "toggle"},
			activate: g.toggleApproval, cycle: func(int) tea.Cmd { return g.toggleApproval() }}))
		add(row{kind: rowInfo, label: subtle(g.ctx.Styles, clean(c.InviteLink))})
		add(row{id: "copy", kind: rowAction, label: "Copy link", hints: []string{"enter", "copy"}, activate: g.copyLink})
		add(row{id: "qr", kind: rowAction, label: "Show QR code", hints: []string{"enter", "show"}, activate: g.showLinkQR})
		add(adminRow(row{id: "reset", kind: rowAction, label: "Reset link", danger: true,
			hints: []string{"enter", "reset"}, activate: g.resetLink}))
	}

	add(row{kind: rowBlank})
	add(row{kind: rowHeader, label: "Members · " + itoa(len(c.Members))})
	add(adminRow(row{id: "add", kind: rowAction, label: "Add members", icon: "+",
		hints: []string{"enter", "add"}, activate: g.addMembers}))
	members := append([]core.Member(nil), c.Members...)
	// Self first, then admins, then by name.
	self := selfACI(g.ctx)
	sortMembers(members, self, g.name)
	for _, m := range members {
		add(g.memberRow(m))
	}
	if len(c.PendingMembers) > 0 {
		add(row{kind: rowBlank})
		add(row{kind: rowHeader, label: "Pending invites · " + itoa(len(c.PendingMembers))})
		for _, m := range c.PendingMembers {
			add(row{kind: rowInfo, label: muted(g.ctx.Styles, g.name(m)+" · invited")})
		}
	}
	if len(c.RequestingJoin) > 0 {
		add(row{kind: rowBlank})
		add(row{kind: rowHeader, label: "Requests to join · " + itoa(len(c.RequestingJoin))})
		for _, m := range c.RequestingJoin {
			add(g.requestRow(m))
		}
	}
	add(row{kind: rowBlank})
	add(row{id: "leave", kind: rowAction, label: "Leave group", danger: true,
		hints: []string{"enter", "leave"}, activate: g.leave})
	g.page.setRows(rows)
}

func sortMembers(ms []core.Member, self core.ACI, name func(core.Member) string) {
	rank := func(m core.Member) int {
		switch {
		case m.ACI == self:
			return 0
		case m.Admin:
			return 1
		}
		return 2
	}
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0; j-- {
			a, b := ms[j-1], ms[j]
			ra, rb := rank(a), rank(b)
			if ra < rb || (ra == rb && strings.ToLower(name(a)) <= strings.ToLower(name(b))) {
				break
			}
			ms[j-1], ms[j] = b, a
		}
	}
}

func (g *GroupSettings) memberRow(m core.Member) row {
	st := g.ctx.Styles
	name := g.name(m)
	value := ""
	if m.Admin {
		value = badge(st, "Admin", pal(st).AccentAlt)
	}
	r := row{id: "m:" + string(m.ACI), kind: rowEntity, label: name, value: value}
	if m.ACI == selfACI(g.ctx) {
		r.hints = []string{"enter", "actions"}
		r.activate = func() tea.Cmd {
			return ui.Push(newActionMenu(g.ctx, "You", []menuAction{
				{key: "l", label: "Leave group", danger: true, run: g.leave},
			}))
		}
		return r
	}
	promote := func() tea.Cmd { return g.toggleAdmin(m) }
	remove := func() tea.Cmd { return g.removeMember(m) }
	r.hints = []string{"enter", "actions", "p", "admin", "x", "remove"}
	r.keys = map[string]func() tea.Cmd{"p": promote, "x": remove}
	r.activate = func() tea.Cmd {
		acts := []menuAction{
			{key: "m", label: "Message", run: func() tea.Cmd {
				chat := core.DirectChatID(m.ACI)
				return tea.Batch(ui.CloseOverlay, func() tea.Msg { return ui.OpenChatMsg{Chat: chat} })
			}},
			{key: "s", label: "Safety number", run: func() tea.Cmd { return ui.Push(NewSafetyNumber(g.ctx, m.ACI)) }},
		}
		if g.chat.SelfIsAdmin {
			label := "Make admin"
			if m.Admin {
				label = "Remove as admin"
			}
			acts = append(acts,
				menuAction{key: "p", label: label, run: promote},
				menuAction{key: "x", label: "Remove from group", danger: true, run: remove})
		}
		return ui.Push(newActionMenu(g.ctx, name, acts))
	}
	if !g.chat.SelfIsAdmin {
		r.hints = []string{"enter", "actions"}
		r.keys = nil
	}
	return r
}

func (g *GroupSettings) requestRow(m core.Member) row {
	name := g.name(m)
	approve := func() tea.Cmd {
		return g.update("Approve", "Approved "+name, core.GroupUpdate{Approve: []core.ACI{m.ACI}})
	}
	deny := func() tea.Cmd {
		return g.confirm("Deny request", "Deny "+name+"'s request to join?", "Deny",
			g.op("Deny", "Denied "+name, core.GroupUpdate{Deny: []core.ACI{m.ACI}}))
	}
	r := row{id: "r:" + string(m.ACI), kind: rowEntity, label: name, value: muted(g.ctx.Styles, "wants to join"),
		hints: []string{"a", "approve", "x", "deny"},
		keys:  map[string]func() tea.Cmd{"a": approve, "x": deny},
		activate: func() tea.Cmd {
			return ui.Push(newActionMenu(g.ctx, name, []menuAction{
				{key: "a", label: "Approve", run: approve},
				{key: "x", label: "Deny", danger: true, run: deny},
			}))
		}}
	if !g.chat.SelfIsAdmin {
		r.disabled = true
		r.reason = "Only admins can approve requests."
	}
	return r
}

// ---- actions ----------------------------------------------------------------

func (g *GroupSettings) editTitle() tea.Cmd {
	id := g.id
	return ui.Push(textDialog(g.ctx, "Group name", "Name", "", g.chat.Title, 1, 32, nonEmpty("A name"),
		func(ctx context.Context, svc core.Service, v string) error {
			v = strings.TrimSpace(v)
			return svc.UpdateGroup(ctx, id, core.GroupUpdate{Title: &v})
		}, "Rename", "Group renamed"))
}

func (g *GroupSettings) editDescription() tea.Cmd {
	id := g.id
	return ui.Push(textDialog(g.ctx, "Group description", "Description", "", g.chat.Description, 4, 480, nil,
		func(ctx context.Context, svc core.Service, v string) error {
			v = strings.TrimSpace(v)
			return svc.UpdateGroup(ctx, id, core.GroupUpdate{Description: &v})
		}, "Description", "Description updated"))
}

func (g *GroupSettings) chooseTimer() tea.Cmd {
	id := g.id
	return ui.Push(timerDialog(g.ctx, g.chat.ExpireTimer, func(ctx context.Context, svc core.Service, d time.Duration) error {
		return svc.UpdateGroup(ctx, id, core.GroupUpdate{ExpireTimer: &d})
	}))
}

func (g *GroupSettings) cycleMute(d int) tea.Cmd {
	return cycleMuteCmd(g.ctx, g.id, g.chat.MutedUntil, d, func(s string) { g.status = s })
}

// cycleMuteCmd steps the mute state of chat through MuteOptions.
func cycleMuteCmd(ctx ui.Context, id core.ChatID, until time.Time, d int, status func(string)) tea.Cmd {
	now := nowFn(ctx)()
	cur := 0
	if now.Before(until) {
		cur = len(MuteOptions) - 1
		if until.Year() < 9000 {
			// The nearest preset not shorter than what is left.
			left := until.Sub(now)
			for i := 1; i < len(MuteOptions)-1; i++ {
				if MuteOptions[i].For >= left-time.Minute {
					cur = i
					break
				}
			}
		}
	}
	idx := ((cur+d)%len(MuteOptions) + len(MuteOptions)) % len(MuteOptions)
	opt := MuteOptions[idx]
	next := MuteUntil(opt, now)
	status("Mute: " + opt.Label)
	return runOp(ctx.Svc, "Mute", "", func(c context.Context, svc core.Service) error {
		return svc.UpdateChat(c, id, core.ChatUpdate{MutedUntil: &next})
	})
}

func (g *GroupSettings) toggleAnnounce() tea.Cmd {
	v := !g.chat.OnlyAdminsSend
	ok := "All members can send messages"
	if v {
		ok = "Only admins can send messages"
	}
	return g.update("Announcement only", ok, core.GroupUpdate{OnlyAdminsSend: &v})
}

func (g *GroupSettings) toggleLink() tea.Cmd {
	if linkOn(g.chat) {
		mode := core.AccessUnsatisfiable
		return g.confirm("Turn off group link", "Turn off the group link? The current link will stop working.", "Turn off",
			g.op("Group link", "Group link turned off", core.GroupUpdate{LinkMode: &mode}))
	}
	mode := core.AccessAny
	return g.update("Group link", "Group link turned on", core.GroupUpdate{LinkMode: &mode})
}

func (g *GroupSettings) toggleApproval() tea.Cmd {
	mode := core.AccessAdmin
	ok := "New members need approval"
	if g.chat.AddFromLinkMode == core.AccessAdmin {
		mode = core.AccessAny
		ok = "Anyone with the link can join"
	}
	return g.update("Admin approval", ok, core.GroupUpdate{LinkMode: &mode})
}

func (g *GroupSettings) copyLink() tea.Cmd {
	link := g.chat.InviteLink
	if link == "" {
		return nil
	}
	return tea.Batch(tea.SetClipboard(link), func() tea.Msg {
		_ = clipboard.WriteAll(link) // best effort; OSC 52 covers remote terminals
		return nil
	}, ui.Toast(ui.ToastSuccess, "Group link copied"))
}

func (g *GroupSettings) showLinkQR() tea.Cmd {
	return ui.Push(NewQRView(g.ctx, "Group link", g.chat.InviteLink, "Scan with a phone camera to join "+clean(g.chat.Title)))
}

func (g *GroupSettings) resetLink() tea.Cmd {
	return g.confirm("Reset group link", "Reset the group link? People will no longer be able to join with the current link.", "Reset",
		g.op("Reset link", "Group link reset", core.GroupUpdate{ResetLink: true}))
}

func (g *GroupSettings) addMembers() tea.Cmd {
	var exclude []core.ACI
	for _, list := range [][]core.Member{g.chat.Members, g.chat.PendingMembers} {
		for _, m := range list {
			exclude = append(exclude, m.ACI)
		}
	}
	return ui.Push(NewMemberPicker(g.ctx, "Add members", exclude, func(acis []core.ACI) tea.Cmd {
		n := len(acis)
		ok := "Added " + itoa(n) + " members"
		if n == 1 {
			ok = "Added 1 member"
		}
		return g.update("Add members", ok, core.GroupUpdate{AddMembers: acis})
	}))
}

func (g *GroupSettings) toggleAdmin(m core.Member) tea.Cmd {
	name := g.name(m)
	if m.Admin {
		return g.confirm("Remove admin", "Remove "+name+" as group admin?", "Remove admin",
			g.op("Demote", name+" is no longer an admin", core.GroupUpdate{Demote: []core.ACI{m.ACI}}))
	}
	return g.confirm("Make admin", "Make "+name+" a group admin? Admins can edit the group and its members.", "Make admin",
		g.op("Promote", name+" is now an admin", core.GroupUpdate{Promote: []core.ACI{m.ACI}}))
}

func (g *GroupSettings) removeMember(m core.Member) tea.Cmd {
	name := g.name(m)
	return g.confirm("Remove member", "Remove "+name+" from the group?", "Remove",
		g.op("Remove", "Removed "+name, core.GroupUpdate{RemoveMembers: []core.ACI{m.ACI}}))
}

func (g *GroupSettings) leave() tea.Cmd {
	id := g.id
	if g.chat.SelfIsAdmin && countAdmins(g.chat.Members) == 1 && len(g.chat.Members) > 1 {
		// Signal requires another admin before the last one leaves.
		g.status = "You are the only admin: make another member an admin before leaving."
		return nil
	}
	text := "Leave " + clean(g.chat.Title) + "? You will no longer be able to send or receive messages in this group."
	return g.confirm("Leave group", text, "Leave",
		runOp(g.ctx.Svc, "Leave group", "You left the group", func(ctx context.Context, svc core.Service) error {
			return svc.LeaveGroup(ctx, id)
		}))
}

func countAdmins(ms []core.Member) int {
	n := 0
	for _, m := range ms {
		if m.Admin {
			n++
		}
	}
	return n
}
