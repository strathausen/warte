package info

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/ui"
)

// KeyContextPinned is the keymap overlay context of the pinned messages
// overlay.
const KeyContextPinned = "info.pinned"

// pinnedScan is how many recent messages the overlay scans for pins.
const pinnedScan = 2000

// PinnedFrom returns the pinned, not deleted messages of msgs, newest
// first.
func PinnedFrom(msgs []core.Message) []core.Message {
	var out []core.Message
	for _, m := range msgs {
		if m.Pinned && !m.RemoteDeleted {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID.Timestamp > out[j].ID.Timestamp })
	return out
}

// pinnedSnippet is a one-line preview of a pinned message.
func pinnedSnippet(ctx ui.Context, chat *core.Chat, m core.Message, maxCells int) string {
	if m.Body != "" {
		return format.Snippet(m.Body, m.Ranges, func(a core.ACI) string { return memberName(chat, ctx.Self, a) }, maxCells)
	}
	switch {
	case len(m.Attachments) > 0:
		i, l := attachmentKind(ctx.Icons, m.Attachments[0])
		return trunc(i+" "+l, maxCells)
	case m.Sticker != nil:
		return trunc(ctx.Icons.Sticker+" Sticker", maxCells)
	case m.Poll != nil:
		return trunc(ctx.Icons.Poll+" "+clean(m.Poll.Question), maxCells)
	}
	return "Message"
}

func senderLabel(ctx ui.Context, m core.Message) string {
	if m.Outgoing {
		return "You"
	}
	if n := clean(m.SenderName); n != "" {
		return n
	}
	return "Unknown"
}

// RenderPinnedStrip renders the one-line header strip shown above a
// conversation with pinned messages: "📌 2/3 Alice: On-call rotation…".
// current is the index (into pinned) of the pin shown; chat (optional)
// resolves mention names. It returns "" when pinned is empty. The result is
// exactly width cells wide.
func RenderPinnedStrip(ctx ui.Context, chat *core.Chat, pinned []core.Message, current, w int) string {
	if len(pinned) == 0 || w <= 0 {
		return ""
	}
	st := ctx.Styles
	current = clamp(current, 0, len(pinned)-1)
	m := pinned[current]
	head := " " + st.PinnedIcon.Render(ctx.Icons.Pinned) + " "
	if len(pinned) > 1 {
		head += st.Subtitle.Render(itoa(current+1)+"/"+itoa(len(pinned))) + " "
	}
	who := senderLabel(ctx, m)
	sender := st.SenderStyle(string(m.ID.Author)).Render(trunc(who, 20)) + st.Subtitle.Render(": ")
	rest := max(w-width(head)-width(sender)-1, 1)
	return fit(head+sender+st.App.Render(pinnedSnippet(ctx, chat, m, rest)), w)
}

type pinnedLoaded struct {
	msgs []core.Message
	err  error
}

type pinnedChat struct{ chat core.Chat }

type pinnedUnpinned struct {
	id  core.MessageID
	err error
}

// PinnedMessages lists the pinned messages of a chat; enter jumps to the
// selected one, p (or x) unpins it.
type PinnedMessages struct {
	ctx      ui.Context
	chat     core.ChatID
	chatInfo *core.Chat
	list     []core.Message
	loaded   bool
	err      error
	sel      int
	offset   int
	height   int
}

// NewPinnedMessages returns the overlay for chat. loaded may hold the
// messages the conversation already has (shown immediately); Init then
// scans recent history through the Service for pins outside that window.
func NewPinnedMessages(ctx ui.Context, chat core.ChatID, loaded []core.Message) *PinnedMessages {
	o := &PinnedMessages{ctx: ctx, chat: chat}
	if loaded != nil {
		o.list = PinnedFrom(loaded)
		o.loaded = true
	}
	return o
}

func (o *PinnedMessages) load() tea.Cmd {
	svc, chat := o.ctx.Svc, o.chat
	if svc == nil || chat == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		msgs, err := svc.Messages(ctx, chat, core.MessageID{}, pinnedScan)
		return pinnedLoaded{msgs, err}
	}
}

func (o *PinnedMessages) Init() tea.Cmd {
	svc, chat := o.ctx.Svc, o.chat
	if svc == nil || chat == "" {
		return nil
	}
	return tea.Batch(o.load(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, err := svc.Chat(ctx, chat)
		if err != nil {
			return nil
		}
		return pinnedChat{c}
	})
}
func (o *PinnedMessages) Title() string      { return "Pinned messages" }
func (o *PinnedMessages) KeyContext() string { return KeyContextPinned }
func (o *PinnedMessages) Size(maxW, maxH int) (int, int) {
	return min(64, maxW), min(max(3*len(o.list)+1, 6), maxH)
}
func (o *PinnedMessages) Footer() string {
	return o.ctx.Styles.RenderKeyHints("enter", "jump", "p", "unpin", "esc", "close")
}

// Pinned returns the listed messages, newest first.
func (o *PinnedMessages) Pinned() []core.Message { return o.list }

func (o *PinnedMessages) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case pinnedLoaded:
		o.loaded, o.err = true, msg.err
		if msg.err == nil {
			o.list = PinnedFrom(msg.msgs)
		}
		o.sel = clamp(o.sel, 0, max(len(o.list)-1, 0))
	case pinnedChat:
		if msg.chat.ID == o.chat {
			c := msg.chat
			o.chatInfo = &c
		}
	case pinnedUnpinned:
		if msg.err != nil {
			return o, ui.Toast(ui.ToastError, "Unpin failed: "+clean(msg.err.Error()))
		}
		o.remove(msg.id)
		return o, ui.Toast(ui.ToastSuccess, "Message unpinned")
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.MessageUpdated:
			if ev.Message.ID.Chat == o.chat {
				o.upsert(ev.Message)
			}
		case core.MessageAdded:
			if ev.Message.ID.Chat == o.chat && ev.Message.Pinned {
				o.upsert(ev.Message)
			}
		case core.MessageRemoved:
			o.remove(ev.ID)
		}
	case ui.ActionMsg:
		return o.act(msg.Action, msg.Count)
	case tea.KeyPressMsg:
		switch msg.String() {
		case "p", "x":
			return o.act(keymap.ActMessagePin, 1)
		}
		if a, ok := keyAction(msg.String()); ok {
			return o.act(a, 1)
		}
	}
	return o, nil
}

func (o *PinnedMessages) upsert(m core.Message) {
	for i := range o.list {
		if o.list[i].ID == m.ID {
			o.list[i] = m
			o.list = PinnedFrom(o.list)
			o.sel = clamp(o.sel, 0, max(len(o.list)-1, 0))
			return
		}
	}
	if m.Pinned {
		o.list = PinnedFrom(append(o.list, m))
	}
}

func (o *PinnedMessages) remove(id core.MessageID) {
	for i := range o.list {
		if o.list[i].ID == id {
			o.list = append(o.list[:i:i], o.list[i+1:]...)
			break
		}
	}
	o.sel = clamp(o.sel, 0, max(len(o.list)-1, 0))
}

func (o *PinnedMessages) act(a keymap.Action, count int) (ui.Overlay, tea.Cmd) {
	switch a {
	case keymap.ActItemSelect:
		if o.sel >= len(o.list) {
			return o, nil
		}
		id := o.list[o.sel].ID
		return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }
	case keymap.ActMessagePin:
		if o.sel >= len(o.list) || o.ctx.Svc == nil {
			return o, nil
		}
		id, svc := o.list[o.sel].ID, o.ctx.Svc
		return o, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return pinnedUnpinned{id, svc.PinMessage(ctx, id, false, 0)}
		}
	}
	o.sel, _ = moveAction(a, count, o.sel, len(o.list), max(o.height/3, 1))
	return o, nil
}

func (o *PinnedMessages) View(w, h int) string {
	o.height = h
	if w <= 0 || h <= 0 {
		return ""
	}
	st := o.ctx.Styles
	ic := o.ctx.Icons
	t := now(o.ctx)
	tf := timeFormatter(o.ctx)
	switch {
	case !o.loaded:
		return fitLines([]string{"", center(st.Subtitle.Render("Loading…"), w)}, w, h)
	case o.err != nil && len(o.list) == 0:
		return fitLines([]string{"", center(st.ErrorText.Render(trunc("Couldn't load: "+clean(o.err.Error()), w-2)), w)}, w, h)
	case len(o.list) == 0:
		return fitLines([]string{"", center(st.Subtitle.Render(ic.Pinned+" No pinned messages"), w), center(st.Subtitle.Render("Press p on a message to pin it"), w)}, w, h)
	}
	var lines []string
	spans := make([][2]int, len(o.list))
	rowW := w - 2 - width(ic.Cursor)
	for i, m := range o.list {
		if i > 0 {
			lines = append(lines, "")
		}
		who := senderLabel(o.ctx, m)
		when := st.Timestamp.Render(tf.RelativeTime(m.SentAt, t))
		left := st.PinnedIcon.Render(ic.Pinned) + " " + st.SenderStyle(string(m.ID.Author)).Bold(true).Render(trunc(who, max(rowW-width(when)-4, 1)))
		l1 := left + spaces(rowW-width(left)-width(when)) + when
		snip := pinnedSnippet(o.ctx, o.chatInfo, m, max(rowW-3, 1))
		l2 := "   " + st.App.Render(snip)
		start := len(lines)
		if i == o.sel {
			lines = append(lines, selectedRow(st, ic, l1, w, true), selectedCont(st, ic, l2, w))
		} else {
			lines = append(lines, listRow(ic, l1, w), listRow(ic, l2, w))
		}
		spans[i] = [2]int{start, len(lines) - 1}
	}
	if o.sel < len(spans) {
		a, b := spans[o.sel][0], spans[o.sel][1]
		if a < o.offset {
			o.offset = a
		}
		if b >= o.offset+h {
			o.offset = b - h + 1
		}
	}
	o.offset = clamp(o.offset, 0, max(len(lines)-h, 0))
	return fitLines(lines[o.offset:], w, h)
}
