package info

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// KeyContextDetails is the keymap overlay context of the message details
// overlay.
const KeyContextDetails = "info.details"

// detailsChatLoaded delivers the chat (for member names) to the overlay.
type detailsChatLoaded struct {
	id   core.MessageID
	chat core.Chat
}

// detailsMsgLoaded delivers a fresh copy of the message.
type detailsMsgLoaded struct{ msg core.Message }

// MessageDetails is the "K" overlay: timestamps, sender, delivery status
// (per recipient in groups), disappearing timer, attachment metadata,
// reactions and the edit history with word diffs between revisions.
type MessageDetails struct {
	ctx  ui.Context
	msg  core.Message
	chat *core.Chat

	lines  []string
	builtW int
	dirty  bool
	offset int
	height int
}

// NewMessageDetails returns the details overlay for m. It refreshes itself
// when the message changes (ui.EventMsg with core.MessageUpdated).
func NewMessageDetails(ctx ui.Context, m core.Message) *MessageDetails {
	return &MessageDetails{ctx: ctx, msg: m, dirty: true}
}

// NewMessageDetailsForSelected returns the details overlay for the message
// selected in the conversation (ctx.Selected), or nil when none is.
func NewMessageDetailsForSelected(ctx ui.Context) *MessageDetails {
	if ctx.Selected == nil {
		return nil
	}
	m := ctx.Selected()
	if m == nil {
		return nil
	}
	return NewMessageDetails(ctx, *m)
}

// Message returns the message shown.
func (o *MessageDetails) Message() core.Message { return o.msg }

func (o *MessageDetails) Init() tea.Cmd {
	svc := o.ctx.Svc
	if svc == nil {
		return nil
	}
	id := o.msg.ID
	return tea.Batch(
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, err := svc.Chat(ctx, id.Chat)
			if err != nil {
				return nil
			}
			return detailsChatLoaded{id: id, chat: c}
		},
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m, err := svc.Message(ctx, id)
			if err != nil {
				return nil
			}
			return detailsMsgLoaded{msg: m}
		},
	)
}

func (o *MessageDetails) Title() string      { return "Message details" }
func (o *MessageDetails) KeyContext() string { return KeyContextDetails }

func (o *MessageDetails) Size(maxW, maxH int) (int, int) {
	w := min(72, maxW)
	o.build(w)
	return w, min(len(o.lines), maxH)
}

func (o *MessageDetails) Footer() string {
	return o.ctx.Styles.RenderKeyHints("j/k", "scroll", "enter", "jump", "esc", "close")
}

func (o *MessageDetails) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case detailsChatLoaded:
		if msg.id == o.msg.ID {
			c := msg.chat
			o.chat, o.dirty = &c, true
		}
	case detailsMsgLoaded:
		if msg.msg.ID == o.msg.ID {
			o.msg, o.dirty = msg.msg, true
		}
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.MessageUpdated:
			if ev.Message.ID == o.msg.ID {
				o.msg, o.dirty = ev.Message, true
			}
		case core.MessageRemoved:
			if ev.ID == o.msg.ID {
				return nil, ui.Toast(ui.ToastInfo, "Message was deleted")
			}
		case core.ChatUpdated:
			if ev.Chat.ID == o.msg.ID.Chat {
				c := ev.Chat
				o.chat, o.dirty = &c, true
			}
		}
	case ui.ActionMsg:
		return o.act(msg.Action, msg.Count)
	case tea.KeyPressMsg:
		if a, ok := keyAction(msg.String()); ok {
			return o.act(a, 1)
		}
	}
	return o, nil
}

func (o *MessageDetails) act(a keymap.Action, count int) (ui.Overlay, tea.Cmd) {
	switch a {
	case keymap.ActMessageInfo:
		return nil, nil
	case keymap.ActItemSelect:
		id := o.msg.ID
		return nil, func() tea.Msg { return ui.OpenChatMsg{Chat: id.Chat, Focus: &id} }
	}
	o.offset, _ = scrollAction(a, count, o.offset, max(len(o.lines)-o.height, 0), o.height)
	return o, nil
}

func (o *MessageDetails) View(w, h int) string {
	o.build(w)
	o.height = h
	o.offset = clamp(o.offset, 0, max(len(o.lines)-h, 0))
	return fitLines(o.lines[min(o.offset, len(o.lines)):], w, h)
}

func (o *MessageDetails) resolve(aci core.ACI) string {
	return memberName(o.chat, o.ctx.Self, aci)
}

func (o *MessageDetails) build(w int) {
	if !o.dirty && o.builtW == w {
		return
	}
	o.dirty, o.builtW = false, w
	o.lines = o.render(w)
}

func (o *MessageDetails) render(w int) []string {
	st := o.ctx.Styles
	ic := o.ctx.Icons
	m := o.msg
	t := now(o.ctx)
	tf := timeFormatter(o.ctx)
	const kw = 11
	var out []string
	add := func(l ...string) { out = append(out, l...) }
	row := func(k, v string) { add(kv(st, k, v, kw, w)) }

	// Header: avatar, sender, kind.
	sender := clean(m.SenderName)
	if m.Outgoing {
		sender = "You"
	}
	if sender == "" {
		sender = o.resolve(m.ID.Author)
	}
	id := string(m.ID.Author)
	avatarName := sender
	if m.Outgoing {
		avatarName = clean(o.ctx.Self.DisplayName())
	}
	head := " " + st.RenderAvatar("", id, avatarName) + " " + st.SenderStyle(id).Bold(true).Render(trunc(sender, w-24))
	kind := "Incoming"
	if m.Outgoing {
		kind = "Outgoing"
	}
	switch {
	case m.Kind == core.KindSystem:
		kind = "System event"
	case m.Kind == core.KindCall:
		kind = "Call event"
	case m.Kind == core.KindStoryReply:
		kind += " story reply"
	case m.ViewOnce:
		kind += " · view once"
	}
	tag := st.Subtitle.Render(kind)
	add("", fit(head+spaces(w-width(head)-width(tag)-1)+tag, w))

	// Body preview.
	bodyW := max(w-6, 1)
	var body []string
	switch {
	case m.RemoteDeleted:
		body = []string{st.Subtitle.Italic(true).Render("This message was deleted")}
	case m.Kind == core.KindSystem:
		body = format.Wrap(clean(m.SystemText), bodyW)
	case m.Body != "":
		body = format.WrapSpans(format.Spans(m.Body, m.Ranges, o.resolve), fmtStyles(o.ctx), bodyW)
	case len(m.Attachments) > 0:
		i, l := attachmentKind(ic, m.Attachments[0])
		body = []string{st.Subtitle.Render(i + " " + l)}
	case m.Sticker != nil:
		body = []string{st.Subtitle.Render(ic.Sticker + " Sticker " + clean(m.Sticker.Emoji))}
	}
	if len(body) > 6 {
		body = append(body[:5], st.Subtitle.Render(ic.Ellipsis))
	}
	if len(body) > 0 {
		add("")
		for _, l := range body {
			add(fit("   "+st.KeyHintSep.Render("│")+" "+l, w))
		}
	}

	// Timeline.
	add("", section(st, "Timeline", "", w))
	row("Sent", st.App.Render(fullTime(m.SentAt, tf)))
	if !m.Outgoing && !m.ReceivedAt.IsZero() {
		v := fullTime(m.ReceivedAt, tf)
		if d := m.ReceivedAt.Sub(m.SentAt); d >= time.Second {
			v += st.Subtitle.Render(" (+" + callDuration(d) + ")")
		}
		row("Received", st.App.Render(v))
	}
	if m.ServerTimestamp > 0 {
		srv := time.UnixMilli(int64(m.ServerTimestamp)).In(t.Location())
		row("Server", st.App.Render(fullTime(srv, tf)))
	}
	if m.Edited && len(m.EditHistory) > 0 {
		last := m.EditHistory[len(m.EditHistory)-1].At
		row("Edited", st.App.Render(tf.RelativeTime(last, t))+st.Subtitle.Render(" · "+plural(len(m.EditHistory), "earlier version")))
	}
	if m.Pinned {
		row("Pinned", st.App.Render(ic.Pinned+" Yes"))
	}

	// Delivery.
	if m.Outgoing {
		if len(m.PerRecipient) > 0 {
			add(o.recipients(w)...)
		} else {
			add("", section(st, "Delivery", "", w))
			row("Status", st.RenderDelivery(ic, m.Status)+" "+st.App.Render(statusText(m.Status)))
		}
	}

	// Disappearing.
	if m.ExpiresIn > 0 {
		add("", section(st, "Disappearing", "", w))
		row("Timer", st.App.Render(ic.Clock+" "+format.FormatDurationLong(m.ExpiresIn)))
		if exp := m.ExpiresAt(); !exp.IsZero() {
			row("Started", st.App.Render(fullTime(m.ExpireStartedAt, tf)))
			row("Expires", st.App.Render(fullTime(exp, tf)))
			left := exp.Sub(t)
			frac := 1 - float64(left)/float64(m.ExpiresIn)
			barW := max(w-kw-12, 4)
			add(fit(" "+spaces(kw+1)+progressBar(st, frac, barW)+" "+st.Subtitle.Render(format.FormatRemaining(left)+" left"), w))
		} else {
			row("Started", st.Subtitle.Render("not yet — starts when read"))
		}
	}

	// Attachments.
	if len(m.Attachments) > 0 || m.Sticker != nil {
		add("", section(st, "Attachments", itoa(len(m.Attachments)), w))
		atts := m.Attachments
		if m.Sticker != nil {
			atts = append(append([]core.Attachment(nil), atts...), m.Sticker.Attachment)
		}
		for i, a := range atts {
			if i > 0 {
				add("")
			}
			add(o.attachment(a, w, kw)...)
		}
	}

	// Reactions.
	if len(m.Reactions) > 0 {
		add("", section(st, "Reactions", itoa(len(m.Reactions)), w))
		rs := append([]core.Reaction(nil), m.Reactions...)
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].At.Before(rs[j].At) })
		for _, r := range rs {
			name := clean(r.FromName)
			if r.From == o.ctx.Self.ACI {
				name = "You"
			}
			if name == "" {
				name = o.resolve(r.From)
			}
			when := ""
			if !r.At.IsZero() {
				when = st.Timestamp.Render(tf.RelativeTime(r.At, t))
			}
			left := "   " + clean(r.Emoji) + "  " + st.SenderStyle(string(r.From)).Render(trunc(name, w-18))
			add(fit(left+spaces(w-width(left)-width(when)-1)+when, w))
		}
	}

	// Edit history.
	if len(m.EditHistory) > 0 {
		add(o.history(w)...)
	}

	add("", fit(" "+st.KeyHintSep.Render(trunc("id "+clean(m.ID.String()), w-2)), w))
	return out
}

// recipients renders the per-recipient delivery table grouped by status.
func (o *MessageDetails) recipients(w int) []string {
	st := o.ctx.Styles
	ic := o.ctx.Icons
	type rec struct {
		aci  core.ACI
		name string
	}
	groups := map[core.DeliveryStatus][]rec{}
	for aci, s := range o.msg.PerRecipient {
		groups[s] = append(groups[s], rec{aci, o.resolve(aci)})
	}
	order := []core.DeliveryStatus{core.StatusViewed, core.StatusRead, core.StatusDelivered, core.StatusSent, core.StatusPending, core.StatusFailed, core.StatusNone}
	var out []string
	for _, s := range order {
		list := groups[s]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].name) < strings.ToLower(list[j].name) })
		title := statusText(s)
		if s == core.StatusNone {
			title = "Unknown"
		}
		out = append(out, "", section(st, title, itoa(len(list)), w))
		for _, r := range list {
			icon := st.RenderDelivery(ic, s)
			if icon == "" {
				icon = " "
			}
			out = append(out, fit("   "+icon+" "+st.SenderStyle(string(r.aci)).Render(trunc(r.name, w-8)), w))
		}
	}
	return out
}

func (o *MessageDetails) attachment(a core.Attachment, w, kw int) []string {
	st := o.ctx.Styles
	ic := o.ctx.Icons
	icon, label := attachmentKind(ic, a)
	name := clean(a.FileName)
	if name == "" {
		name = label
	}
	out := []string{fit("   "+icon+"  "+st.App.Bold(true).Render(trunc(name, w-8)), w)}
	row := func(k, v string) { out = append(out, kv(st, "  "+k, v, kw+2, w)) }
	typ := clean(a.ContentType)
	if typ == "" {
		typ = "unknown"
	}
	row("Type", st.App.Render(label)+st.Subtitle.Render(" · "+typ))
	row("Size", st.App.Render(humanSize(a.Size)))
	if a.Width > 0 && a.Height > 0 {
		row("Dimensions", st.App.Render(itoa(a.Width)+" × "+itoa(a.Height)+" px"))
	}
	var flags []string
	if a.VoiceNote {
		flags = append(flags, "voice note")
	}
	if a.GIF {
		flags = append(flags, "GIF")
	}
	if a.Borderless {
		flags = append(flags, "borderless")
	}
	if len(flags) > 0 {
		row("Flags", st.App.Render(strings.Join(flags, ", ")))
	}
	switch {
	case a.Failed:
		row("Download", st.ErrorText.Render(ic.Failed+" failed"))
	case a.Downloaded:
		row("Download", st.SuccessText.Render(ic.Check+" on this device"))
	case a.Progress > 0:
		row("Download", progressBar(st, a.Progress, 12)+st.Subtitle.Render(" "+itoa(int(a.Progress*100))+"%"))
	default:
		row("Download", st.Subtitle.Render("not downloaded"))
	}
	if c := strings.TrimSpace(clean(a.Caption)); c != "" {
		row("Caption", st.App.Render(c))
	}
	return out
}

// history renders every revision, oldest first, each diffed against the
// one before it, ending with the current text.
func (o *MessageDetails) history(w int) []string {
	st := o.ctx.Styles
	t := now(o.ctx)
	tf := timeFormatter(o.ctx)
	m := o.msg
	type rev struct {
		body   string
		ranges []core.BodyRange
		at     time.Time
	}
	revs := make([]rev, 0, len(m.EditHistory)+1)
	for _, r := range m.EditHistory {
		revs = append(revs, rev{r.Body, r.Ranges, r.At})
	}
	revs = append(revs, rev{m.Body, m.Ranges, time.Time{}})
	out := []string{"", section(st, "Edit history", plural(len(revs), "version"), w)}
	ins := lipgloss.NewStyle().Foreground(st.Theme.Palette.Success).Underline(true)
	del := lipgloss.NewStyle().Foreground(st.Theme.Palette.Error).Strikethrough(true)
	if st.Theme.Mono {
		ins = lipgloss.NewStyle().Underline(true)
		del = lipgloss.NewStyle().Strikethrough(true).Faint(true)
	}
	prev := ""
	textW := max(w-7, 1)
	for i, r := range revs {
		text := maskedPlain(format.Spans(r.body, r.ranges, o.resolve))
		label := "v" + itoa(i+1)
		var when string
		switch {
		case i == len(revs)-1:
			when = "current"
		case r.at.IsZero():
			when = "—"
		default:
			when = tf.RelativeTime(r.at, t) + " · " + tf.Clock(r.at)
		}
		if i == 0 {
			when += " · original"
		}
		badge := st.KeyHintKey.Render(" " + label + " ")
		out = append(out, "", fit("  "+badge+" "+st.Timestamp.Render(when), w))
		var styled string
		if i == 0 {
			styled = st.App.Render(text)
		} else {
			styled = renderDiff(wordDiff(prev, text), st.App, ins, del)
		}
		if strings.TrimSpace(text) == "" && i > 0 && prev == "" {
			styled = st.Subtitle.Render("(empty)")
		}
		for _, l := range format.Wrap(styled, textW) {
			out = append(out, fit("     "+l, w))
		}
		prev = text
	}
	return out
}

// maskedPlain returns the plain text of spans with spoilers hidden and
// control characters removed.
func maskedPlain(spans []format.Span) string {
	var b strings.Builder
	for _, s := range spans {
		if s.Spoiler {
			for k, line := range strings.Split(s.Text, "\n") {
				if k > 0 {
					b.WriteString("\n")
				}
				b.WriteString(strings.Repeat(string(format.SpoilerRune), ansi.StringWidth(line)))
			}
			continue
		}
		b.WriteString(s.Text)
	}
	return theme.Sanitize(b.String())
}

// progressBar renders frac (0..1) as a bar of w cells.
func progressBar(st *theme.Styles, frac float64, w int) string {
	frac = max(0, min(frac, 1))
	full := int(frac*float64(w) + 0.5)
	if st.Theme.Mono {
		return strings.Repeat("█", full) + strings.Repeat("░", w-full)
	}
	return st.ScrollbarThumb.Render(strings.Repeat("━", full)) + st.ScrollbarTrack.Render(strings.Repeat("━", w-full))
}

// ---- word diff ----------------------------------------------------------------------------------

type diffOp int

const (
	opEq diffOp = iota
	opIns
	opDel
)

type diffTok struct {
	op   diffOp
	text string
}

// tokenize splits s into alternating runs of whitespace and non-whitespace.
func tokenize(s string) []string {
	var out []string
	start := 0
	var prevSpace bool
	for i, r := range s {
		sp := unicode.IsSpace(r)
		if i > 0 && sp != prevSpace {
			out = append(out, s[start:i])
			start = i
		}
		prevSpace = sp
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// maxDiffCells bounds the LCS table; larger inputs degrade to a full
// replacement.
const maxDiffCells = 400_000

// wordDiff computes a word-level diff from a to b.
func wordDiff(a, b string) []diffTok {
	ta, tb := tokenize(a), tokenize(b)
	n, m := len(ta), len(tb)
	if n*m > maxDiffCells {
		return []diffTok{{opDel, a}, {opIns, b}}
	}
	// lcs[i][j] = LCS length of ta[i:], tb[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if ta[i] == tb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffTok
	push := func(op diffOp, s string) {
		if k := len(out); k > 0 && out[k-1].op == op {
			out[k-1].text += s
			return
		}
		out = append(out, diffTok{op, s})
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case ta[i] == tb[j]:
			push(opEq, ta[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			push(opDel, ta[i])
			i++
		default:
			push(opIns, tb[j])
			j++
		}
	}
	for ; i < n; i++ {
		push(opDel, ta[i])
	}
	for ; j < m; j++ {
		push(opIns, tb[j])
	}
	return out
}

// renderDiff styles a diff. Whitespace-only changes are shown plainly; a
// deletion directly followed by an insertion is separated by a space so
// the two stay readable without colors.
func renderDiff(d []diffTok, eq, ins, del lipgloss.Style) string {
	var b strings.Builder
	for i, t := range d {
		if i > 0 && t.op == opIns && d[i-1].op == opDel && !strings.HasSuffix(d[i-1].text, " ") && !strings.HasPrefix(t.text, " ") {
			b.WriteString(" ")
		}
		if t.op != opEq && strings.TrimSpace(t.text) == "" {
			if t.op == opIns {
				b.WriteString(t.text)
			}
			continue
		}
		// Style line by line so wrapping and newlines stay clean.
		parts := strings.Split(t.text, "\n")
		for k, p := range parts {
			if k > 0 {
				b.WriteString("\n")
			}
			if p == "" {
				continue
			}
			switch t.op {
			case opEq:
				b.WriteString(eq.Render(p))
			case opIns:
				b.WriteString(ins.Render(p))
			case opDel:
				b.WriteString(del.Render(p))
			}
		}
	}
	return b.String()
}
