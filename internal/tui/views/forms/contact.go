package forms

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/tui/ui"
)

// ContactSettings is the settings page of a direct chat: nickname, mute,
// disappearing timer, safety number, block / unblock and, for message
// requests, accept / report spam & block.
type ContactSettings struct {
	ctx     ui.Context
	id      core.ChatID
	chat    core.Chat
	contact core.Contact
	loaded  bool
	err     string
	status  string
	page    page
}

// NewChatSettings returns the settings overlay for any chat: group settings
// for groups, contact settings for direct chats (and Note to Self).
func NewChatSettings(ctx ui.Context, id core.ChatID) ui.Overlay {
	if id.IsGroup() {
		return NewGroupSettings(ctx, id)
	}
	return NewContactSettings(ctx, id)
}

// NewContactSettings returns the settings overlay for a direct chat.
func NewContactSettings(ctx ui.Context, id core.ChatID) *ContactSettings {
	return &ContactSettings{ctx: ctx, id: id}
}

type contactLoadedMsg struct {
	id      core.ChatID
	chat    core.Chat
	contact core.Contact
	err     error
}

func (c *ContactSettings) load() tea.Cmd {
	svc, id := c.ctx.Svc, c.id
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		chat, err := svc.Chat(ctx, id)
		var ct core.Contact
		if err == nil {
			if chat.Contact != nil {
				ct = *chat.Contact
			}
			if full, cerr := svc.Contact(ctx, id.ACI()); cerr == nil {
				ct = full
			}
		}
		return contactLoadedMsg{id: id, chat: chat, contact: ct, err: err}
	}
}

func (c *ContactSettings) Init() tea.Cmd      { return c.load() }
func (c *ContactSettings) KeyContext() string { return ContextChatSettings }
func (c *ContactSettings) Title() string {
	if !c.loaded || c.err != "" {
		return "Chat settings"
	}
	return clean(c.displayName())
}

func (c *ContactSettings) displayName() string {
	if c.chat.Kind == core.ChatNoteToSelf || c.contact.IsSelf {
		return "Note to Self"
	}
	if n := c.contact.DisplayName(); n != "" {
		return n
	}
	return c.chat.Title
}

func (c *ContactSettings) Size(maxW, maxH int) (int, int) {
	w := min(60, maxW)
	return w, min(max(len(c.page.rows)+len(c.header(w))+1, 10), maxH)
}

func (c *ContactSettings) Footer() string {
	st := c.ctx.Styles
	if r := c.page.current(); r != nil && len(r.hints) > 0 {
		return st.RenderKeyHints(append(append([]string{}, r.hints...), "esc", "close")...)
	}
	return st.RenderKeyHints("j/k", "move", "enter", "select", "esc", "close")
}

func (c *ContactSettings) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case contactLoadedMsg:
		if msg.id != c.id {
			return c, nil
		}
		c.loaded = true
		if msg.err != nil {
			c.err = msg.err.Error()
			return c, nil
		}
		c.err = ""
		c.chat, c.contact = msg.chat, msg.contact
		c.rebuild()
		return c, nil
	case opDoneMsg:
		c.status = ""
		return c, tea.Batch(toastFor(msg), c.load())
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.ChatUpdated:
			if ev.Chat.ID == c.id {
				c.chat = ev.Chat
				c.rebuild()
			}
		case core.ContactUpdated:
			if ev.Contact.ACI == c.id.ACI() {
				c.contact = ev.Contact
				c.rebuild()
			}
		case core.ChatRemoved:
			if ev.ID == c.id {
				return nil, nil
			}
		}
		return c, nil
	}
	if !c.loaded || c.err != "" {
		return c, nil
	}
	if d, ok := navDelta(msg, c.page.height); ok {
		c.page.move(d)
		c.status = ""
		return c, nil
	}
	r := c.page.current()
	if r == nil {
		return c, nil
	}
	if isSelect(msg) && r.activate != nil {
		return c, r.activate()
	}
	if d := leftRight(msg); d != 0 && r.cycle != nil {
		return c, r.cycle(d)
	}
	if f := r.keys[keyOf(msg)]; f != nil {
		return c, f()
	}
	return c, nil
}

func (c *ContactSettings) header(w int) []string {
	st := c.ctx.Styles
	ct := c.contact
	name := clean(c.displayName())
	av := st.RenderAvatar(c.chat.Color, string(ct.ACI), name)
	title := st.Title.Render(trunc(name, max(w-8, 1)))
	if ct.Verified {
		title += " " + st.SuccessText.Render(c.ctx.Icons.Verified)
	}
	lines := []string{av + " " + title}
	var sub []string
	if ct.Username != "" {
		sub = append(sub, clean(ct.Username))
	}
	if ct.E164 != "" {
		sub = append(sub, clean(ct.E164))
	}
	if ct.Nickname != "" && ct.ProfileName != "" && ct.ProfileName != ct.Nickname {
		sub = append(sub, "profile: "+clean(ct.ProfileName))
	}
	if len(sub) > 0 {
		lines = append(lines, muted(st, trunc(strings.Join(sub, " · "), w)))
	}
	if about := strings.TrimSpace(clean(strings.TrimSpace(ct.AboutEmoji + " " + ct.About))); about != "" {
		lines = append(lines, muted(st, trunc(about, w)))
	}
	switch {
	case c.chat.MessageRequest:
		lines = append(lines, st.WarningText.Render(trunc("Message request: they can't see that you read their messages until you accept.", w)))
	case c.chat.Blocked || ct.Blocked:
		lines = append(lines, st.ErrorText.Render(trunc("Blocked", w)))
	}
	return lines
}

func (c *ContactSettings) View(w, h int) string {
	st := c.ctx.Styles
	if !c.loaded {
		return muted(st, "Loading…")
	}
	if c.err != "" {
		return clampLines([]string{statusLine(st, c.err, true, w)}, w, h)
	}
	lines := append(c.header(w), "")
	if len(lines) > h/2 {
		lines = lines[:max(h/2, 1)]
	}
	listH := h - len(lines)
	if c.status != "" {
		listH--
	}
	lines = append(lines, c.page.render(st, w, listH)...)
	if c.status != "" {
		lines = append(lines, statusLine(st, c.status, false, w))
	}
	return clampLines(lines, w, h)
}

func (c *ContactSettings) rebuild() {
	ch, ct := c.chat, c.contact
	self := ch.Kind == core.ChatNoteToSelf || ct.IsSelf || ct.ACI == selfACI(c.ctx)
	blocked := ch.Blocked || ct.Blocked
	st := c.ctx.Styles
	var rows []row
	add := func(r row) { rows = append(rows, r) }

	if ch.MessageRequest {
		add(row{kind: rowHeader, label: "Message request"})
		add(row{id: "accept", kind: rowAction, label: "Accept", icon: c.ctx.Icons.Check,
			hints: []string{"enter", "accept"}, activate: c.accept})
		add(row{id: "spam", kind: rowAction, label: "Report spam & block", danger: true,
			hints: []string{"enter", "report"}, activate: c.reportSpam})
		add(row{kind: rowBlank})
	}

	add(row{kind: rowHeader, label: "Chat"})
	if !self {
		nick := ct.Nickname
		if nick == "" {
			nick = "Add a nickname"
		}
		add(row{id: "nick", kind: rowAction, label: "Nickname", value: muted(st, trunc(clean(nick), 26)),
			hints: []string{"enter", "edit"}, activate: c.editNickname})
	}
	add(row{id: "mute", kind: rowChoice, label: "Mute notifications", value: muteLabel(ch.MutedUntil, nowFn(c.ctx)()),
		hints:    []string{"h/l", "change", "enter", "choose"},
		activate: func() tea.Cmd { return ui.Push(muteDialog(c.ctx, c.id)) },
		cycle: func(d int) tea.Cmd {
			return cycleMuteCmd(c.ctx, c.id, c.chat.MutedUntil, d, func(s string) { c.status = s })
		}})
	add(row{id: "timer", kind: rowChoice, label: "Disappearing messages", value: timerLabel(ch.ExpireTimer),
		hints: []string{"enter", "choose"}, activate: c.chooseTimer})

	if !self {
		add(row{kind: rowBlank})
		add(row{kind: rowHeader, label: "Privacy"})
		sn := "Not verified"
		if ct.Verified {
			sn = "Verified"
		}
		add(row{id: "safety", kind: rowAction, label: "Safety number", icon: c.ctx.Icons.Lock, value: muted(st, sn),
			hints:    []string{"enter", "view"},
			activate: func() tea.Cmd { return ui.Push(NewSafetyNumber(c.ctx, c.id.ACI())) }})
		if blocked {
			add(row{id: "block", kind: rowAction, label: "Unblock", hints: []string{"enter", "unblock"}, activate: c.unblock})
		} else {
			add(row{id: "block", kind: rowAction, label: "Block", danger: true, hints: []string{"enter", "block"}, activate: c.block})
		}
	}
	c.page.setRows(rows)
}

// ---- actions --------------------------------------------------------------

func (c *ContactSettings) editNickname() tea.Cmd {
	aci := c.id.ACI()
	return ui.Push(textDialog(c.ctx, "Nickname", "Nickname",
		"Only you can see it. Leave empty to remove.", c.contact.Nickname, 1, 64, nil,
		func(ctx context.Context, svc core.Service, v string) error {
			return svc.SetNickname(ctx, aci, strings.TrimSpace(v))
		}, "Nickname", "Nickname saved"))
}

func (c *ContactSettings) chooseTimer() tea.Cmd {
	id := c.id
	return ui.Push(timerDialog(c.ctx, c.chat.ExpireTimer, func(ctx context.Context, svc core.Service, d time.Duration) error {
		return svc.UpdateChat(ctx, id, core.ChatUpdate{ExpireTimer: &d})
	}))
}

func (c *ContactSettings) accept() tea.Cmd {
	id := c.id
	c.status = "Accepting…"
	return runOp(c.ctx.Svc, "Accept", "Message request accepted", func(ctx context.Context, svc core.Service) error {
		return svc.AcceptMessageRequest(ctx, id)
	})
}

func (c *ContactSettings) reportSpam() tea.Cmd {
	id := c.id
	name := clean(c.displayName())
	return ui.Push(NewConfirm(c.ctx, "Report spam", "Report "+name+" as spam and block them? Signal will be notified that this account sent you spam.", "Report & block", true,
		runOp(c.ctx.Svc, "Report spam", "Reported and blocked", func(ctx context.Context, svc core.Service) error {
			if err := svc.ReportSpam(ctx, id); err != nil {
				return err
			}
			return svc.Block(ctx, id, true)
		})))
}

func (c *ContactSettings) block() tea.Cmd {
	id := c.id
	name := clean(c.displayName())
	return ui.Push(NewConfirm(c.ctx, "Block", "Block "+name+"? Blocked people can't call you or send you messages.", "Block", true,
		runOp(c.ctx.Svc, "Block", "Blocked "+name, func(ctx context.Context, svc core.Service) error {
			return svc.Block(ctx, id, true)
		})))
}

func (c *ContactSettings) unblock() tea.Cmd {
	id := c.id
	name := clean(c.displayName())
	c.status = "Unblocking…"
	return runOp(c.ctx.Svc, "Unblock", "Unblocked "+name, func(ctx context.Context, svc core.Service) error {
		return svc.Block(ctx, id, false)
	})
}
