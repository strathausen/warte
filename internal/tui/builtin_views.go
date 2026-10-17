package tui

import (
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/sahilm/fuzzy"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// ---- help ----------------------------------------------------------------------------

type helpOverlay struct {
	a *App
	// view is set when help was opened over an overlay: its footer hints
	// are listed first.
	view    ui.Overlay
	mode    keymap.Mode
	ctx     keymap.Context
	lines   []string
	offset  int
	filter  textinput.Model
	editing bool
	height  int
	width   int // content width of the last View; 0 before the first
}

// newHelpOverlay lists the bindings of the current mode and pane.
func newHelpOverlay(a *App) *helpOverlay {
	h := &helpOverlay{a: a, mode: a.modeBeforeHelp(), ctx: a.focusContext()}
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter"
	styleInput(&ti, a.styles)
	h.filter = ti
	h.build()
	return h
}

// newOverlayHelp lists the bindings that apply inside overlay o.
func newOverlayHelp(a *App, o ui.Overlay) *helpOverlay {
	h := &helpOverlay{a: a, mode: keymap.Normal, ctx: keymap.Overlay(o.KeyContext()), view: o}
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter"
	styleInput(&ti, a.styles)
	h.filter = ti
	h.build()
	return h
}

func (a *App) modeBeforeHelp() keymap.Mode {
	if len(a.overlays) == 0 {
		m := a.keys.Mode()
		if m == keymap.Command || m == keymap.Search {
			return keymap.Normal
		}
		return m
	}
	return a.modeBeforeOver
}

func (h *helpOverlay) build() {
	st := h.a.styles
	q := strings.ToLower(strings.TrimSpace(h.filter.Value()))
	var out []string
	if h.view != nil {
		if f, ok := h.view.(ui.Footer); ok && q == "" {
			out = append(out, st.Title.Render(clean(h.view.Title())), "  "+f.Footer(), "")
		}
		out = append(out, st.Subtitle.Render("Keys inside this view; views with a text field take keys directly."), "")
	}
	groups := h.a.keys.Help(h.mode, h.ctx)
	if h.mode != keymap.Insert && h.view == nil {
		// Composer keys are useful to know from Normal mode too.
		for _, g := range h.a.keys.Help(keymap.Insert, keymap.Global) {
			if g.Group == keymap.GroupComposer {
				groups = append(groups, g)
			}
		}
	}
	keyW := 0
	for _, g := range groups {
		for _, e := range g.Entries {
			keyW = max(keyW, width(e.KeyLabel()))
		}
	}
	keyW = min(keyW, 18)
	if h.width > 0 {
		// Narrow dialogs: give the descriptions at least two thirds.
		keyW = min(keyW, max((h.width-4)/3, 6))
	}
	for _, g := range groups {
		var rows []string
		for _, e := range g.Entries {
			label := shortKeyLabel(e.KeyLabel(), keyW)
			if q != "" && !strings.Contains(strings.ToLower(e.Help), q) && !strings.Contains(strings.ToLower(label), q) {
				continue
			}
			rows = append(rows, "  "+st.KeyHintKey.Render(fit(label, keyW))+"  "+st.KeyHintDesc.Render(e.Help))
		}
		if len(rows) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, st.Title.Render(string(g.Group)))
		out = append(out, rows...)
	}
	if q == "" && h.view == nil {
		out = append(out, "", st.Title.Render("Commands"))
		for _, n := range h.a.cmdNames {
			c := h.a.commands[n]
			desc := c.Help
			if c.Args != "" {
				desc = c.Args + "  " + desc
			}
			out = append(out, "  "+st.KeyHintKey.Render(fit(":"+n, keyW))+"  "+st.KeyHintDesc.Render(clean(desc)))
		}
		if v := h.a.opts.Version; v != "" {
			out = append(out, "", st.Subtitle.Render("warte "+clean(v)))
		}
	}
	if len(out) == 0 {
		out = []string{st.Subtitle.Render("No bindings match")}
	}
	h.lines = out
	h.offset = clamp(h.offset, 0, max(len(h.lines)-1, 0))
}

// shortKeyLabel drops alternative keys ("a/b/c") that don't fit in w cells,
// so a label is never cut in the middle of a key.
func shortKeyLabel(label string, w int) string {
	if width(label) <= w {
		return label
	}
	out := ""
	for _, p := range strings.Split(label, "/") {
		if p == "" { // the "/" key itself
			return fitEllipsisRaw(label, w)
		}
		next := p
		if out != "" {
			next = out + "/" + p
		}
		if width(next) > w {
			break
		}
		out = next
	}
	if out == "" {
		return fitEllipsisRaw(label, w)
	}
	return out
}

func (h *helpOverlay) Init() tea.Cmd      { return nil }
func (h *helpOverlay) KeyContext() string { return keymap.OverlayHelp }
func (h *helpOverlay) Title() string {
	return "Keys · " + strings.ToLower(h.mode.Label()) + " · " + h.ctx.String()
}
func (h *helpOverlay) CaptureKeys() bool { return h.editing }
func (h *helpOverlay) Size(w, hh int) (int, int) {
	return min(w, 72), min(hh, max(len(h.lines)+2, 10))
}
func (h *helpOverlay) Footer() string {
	return h.a.styles.RenderKeyHints("j/k", "scroll", "/", "filter", "esc", "close")
}

func (h *helpOverlay) HandleBack() (bool, tea.Cmd) {
	if h.filter.Value() != "" {
		h.filter.SetValue("")
		h.build()
		return true, nil
	}
	return false, nil
}

func (h *helpOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if h.editing {
			switch msg.String() {
			case "esc":
				h.editing = false
				h.filter.Blur()
				if h.filter.Value() == "" {
					return h, nil
				}
				h.filter.SetValue("")
				h.build()
				return h, nil
			case "enter":
				h.editing = false
				h.filter.Blur()
				return h, nil
			}
			var cmd tea.Cmd
			h.filter, cmd = h.filter.Update(msg)
			h.offset = 0
			h.build()
			return h, cmd
		}
	case ui.ActionMsg:
		page := max(h.height-2, 1)
		switch msg.Action {
		case keymap.ActCursorDown:
			h.offset += msg.Count
		case keymap.ActCursorUp:
			h.offset -= msg.Count
		case keymap.ActHalfPageDown, keymap.ActPageDown:
			h.offset += page
		case keymap.ActHalfPageUp, keymap.ActPageUp:
			h.offset -= page
		case keymap.ActCursorTop:
			h.offset = 0
		case keymap.ActCursorBottom:
			h.offset = len(h.lines)
		case keymap.ActSearchForward, keymap.ActChatFilter:
			h.editing = true
			return h, h.filter.Focus()
		case keymap.ActHelp:
			return nil, nil
		}
		h.offset = clamp(h.offset, 0, max(len(h.lines)-h.height+1, 0))
	case tea.MouseWheelMsg:
		if msg.Button == tea.MouseWheelDown {
			h.offset = clamp(h.offset+3, 0, max(len(h.lines)-h.height+1, 0))
		} else {
			h.offset = clamp(h.offset-3, 0, max(len(h.lines)-h.height+1, 0))
		}
	}
	return h, nil
}

func (h *helpOverlay) View(w, hh int) string {
	h.height = hh
	if w != h.width {
		h.width = w
		h.build()
	}
	top := ""
	if h.editing || h.filter.Value() != "" {
		h.filter.SetWidth(w - 4)
		top = h.filter.View()
	}
	body := hh
	if top != "" {
		body--
	}
	h.offset = clamp(h.offset, 0, max(len(h.lines)-body, 0))
	end := min(h.offset+body, len(h.lines))
	vis := append([]string(nil), h.lines[h.offset:end]...)
	if len(h.lines) > body {
		sb := splitLines(h.a.styles.RenderScrollbar(body, len(h.lines), body, h.offset))
		vis = fitLines(vis, w-1, body)
		for i := range vis {
			if i < len(sb) {
				vis[i] += sb[i]
			}
		}
	}
	if top != "" {
		vis = append([]string{top}, vis...)
	}
	return strings.Join(vis, "\n")
}

// ---- quick reaction bar ------------------------------------------------------------------

type reactionBar struct {
	a      *App
	msg    core.Message
	emojis []string
	sel    int
}

// openReactionBar shows Signal's quick reactions for m.
func (a *App) openReactionBar(m *core.Message) tea.Cmd {
	if m.Kind != core.KindNormal || m.RemoteDeleted {
		return a.toast(ui.ToastWarning, "Can't react to this message")
	}
	rb := &reactionBar{a: a, msg: *m, emojis: format.DefaultReactions()}
	for i, e := range rb.emojis {
		if a.hasReacted(m, e) {
			rb.sel = i
		}
	}
	return a.pushOverlay(rb)
}

func (a *App) hasReacted(m *core.Message, emoji string) bool {
	for _, r := range m.Reactions {
		if r.From == a.self.ACI && r.Emoji == emoji {
			return true
		}
	}
	return false
}

func (r *reactionBar) Init() tea.Cmd      { return nil }
func (r *reactionBar) Title() string      { return "React" }
func (r *reactionBar) KeyContext() string { return "reaction" }
func (r *reactionBar) CaptureKeys() bool  { return true }

func (r *reactionBar) anchorToSelection() bool { return true }
func (r *reactionBar) Size(w, h int) (int, int) {
	return min(w, len(r.emojis)*5+5), 2
}
func (r *reactionBar) Footer() string {
	return r.a.styles.RenderKeyHints("1-6", "pick", "h/l", "move", "enter", "react")
}

func (r *reactionBar) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	n := len(r.emojis) + 1 // + "…"
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		k := msg.String()
		switch k {
		case "esc", "q", "ctrl+c":
			return nil, nil
		case "h", "left", "shift+tab":
			r.sel = (r.sel - 1 + n) % n
		case "l", "right", "tab":
			r.sel = (r.sel + 1) % n
		case "enter", "space":
			return r.pick(r.sel)
		default:
			if len(k) == 1 && k[0] >= '1' && k[0] <= '9' {
				i := int(k[0] - '1')
				if i < n {
					return r.pick(i)
				}
			}
		}
	case tea.MouseClickMsg:
		i := (msg.X - 1) / 5
		if msg.Y == 0 && i >= 0 && i < n {
			return r.pick(i)
		}
	}
	return r, nil
}

func (r *reactionBar) pick(i int) (ui.Overlay, tea.Cmd) {
	if i >= len(r.emojis) {
		if h, ok := ui.ActionHandlerFor(ui.ActReactionPicker); ok {
			return nil, h(r.a.uictx)
		}
		return nil, r.a.openCmdLine("react ")
	}
	e := r.emojis[i]
	return nil, func() tea.Msg { return ui.ReactMsg{ID: r.msg.ID, Emoji: e, Toggle: true} }
}

func (r *reactionBar) View(w, h int) string {
	st := r.a.styles
	p := st.Theme.Palette
	var cells []string
	for i, e := range append(append([]string(nil), r.emojis...), "…") {
		label := " " + e + " "
		if e == "…" {
			label = " … "
		}
		s := lipgloss.NewStyle()
		if i < len(r.emojis) && r.a.hasReacted(&r.msg, e) {
			s = s.Background(p.MentionSelfBg)
		}
		if i == r.sel {
			s = s.Background(p.BgSelected).Bold(true)
			if st.Theme.Mono {
				s = lipgloss.NewStyle().Reverse(true)
			}
		}
		cells = append(cells, s.Render(fit(label, 4)))
	}
	nums := ""
	for i := range r.emojis {
		nums += st.KeyHintSep.Render(centerText(itoa(i+1), 4)) + " "
	}
	return " " + strings.Join(cells, " ") + "\n " + nums
}

// react toggles a reaction (from the bar, a picker or :react).
func (a *App) react(id core.MessageID, emoji string, toggle bool) tea.Cmd {
	remove := false
	if toggle {
		if i, ok := a.conv.index[id]; ok && a.hasReacted(&a.conv.msgs[i].m, emoji) {
			remove = true
		}
	}
	a.lastEmoji = emoji
	a.keys.SetLastChange(keymap.Result{Action: keymap.ActMessageReact, Count: 1})
	return a.do("react", func(ctx contextT) error { return a.svc.React(ctx, id, emoji, remove) }, "")
}

// ---- chat picker (forward, :open, finder fallback) -----------------------------------------

type chatPicker struct {
	a        *App
	title    string
	input    textinput.Model
	chats    []core.Chat
	matches  []int
	sel      int
	onSelect func(core.ChatID) tea.Cmd
}

func newChatPicker(a *App, title string, onSelect func(core.ChatID) tea.Cmd) *chatPicker {
	ti := textinput.New()
	ti.Prompt = a.icons.Search + " "
	ti.Placeholder = "type to filter"
	styleInput(&ti, a.styles)
	ctx, cancel := a.queryCtx()
	defer cancel()
	chats, _ := a.svc.Chats(ctx, core.ChatFilter{IncludeEmpty: true})
	p := &chatPicker{a: a, title: title, input: ti, chats: chats, onSelect: onSelect}
	p.filter()
	return p
}

func (p *chatPicker) filter() {
	q := strings.TrimSpace(p.input.Value())
	p.matches = p.matches[:0]
	if q == "" {
		for i := range p.chats {
			p.matches = append(p.matches, i)
		}
	} else {
		names := make([]string, len(p.chats))
		for i, c := range p.chats {
			names[i] = clean(c.Title)
		}
		for _, m := range fuzzy.Find(q, names) {
			p.matches = append(p.matches, m.Index)
		}
	}
	p.sel = clamp(p.sel, 0, max(len(p.matches)-1, 0))
}

func (p *chatPicker) Init() tea.Cmd      { return p.input.Focus() }
func (p *chatPicker) Title() string      { return p.title }
func (p *chatPicker) KeyContext() string { return "picker" }
func (p *chatPicker) CaptureKeys() bool  { return true }
func (p *chatPicker) Size(w, h int) (int, int) {
	return min(w, 56), min(h, 18)
}
func (p *chatPicker) Footer() string {
	return p.a.styles.RenderKeyHints("↑/↓", "move", "enter", "select", "esc", "close")
}

func (p *chatPicker) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			return nil, nil
		case "enter":
			if len(p.matches) == 0 {
				return p, nil
			}
			id := p.chats[p.matches[p.sel]].ID
			return nil, p.onSelect(id)
		case "up", "ctrl+p", "ctrl+k", "shift+tab":
			p.sel = max(p.sel-1, 0)
			return p, nil
		case "down", "ctrl+n", "ctrl+j", "tab":
			p.sel = min(p.sel+1, max(len(p.matches)-1, 0))
			return p, nil
		}
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		p.filter()
		return p, cmd
	case tea.MouseClickMsg:
		i := msg.Y - 2
		if i >= 0 && i < len(p.matches) {
			p.sel = i
			id := p.chats[p.matches[i]].ID
			return nil, p.onSelect(id)
		}
	}
	return p, nil
}

func (p *chatPicker) View(w, h int) string {
	st := p.a.styles
	pal := st.Theme.Palette
	p.input.SetWidth(w - 3)
	lines := []string{p.input.View(), hrule(st, w)}
	rows := h - 2
	start := clamp(p.sel-rows+1, 0, max(len(p.matches)-rows, 0))
	for k := start; k < len(p.matches) && len(lines) < h; k++ {
		c := p.chats[p.matches[k]]
		title := clean(c.Title)
		av := st.Avatar(c.Color, string(c.ID)).Render(theme.Initials(title))
		line := av + " " + title
		if k == p.sel {
			sel := lipgloss.NewStyle().Background(pal.BgSelected).Foreground(pal.Fg).Bold(true)
			if st.Theme.Mono {
				sel = lipgloss.NewStyle().Reverse(true)
			}
			line = av + sel.Render(fit(" "+title, w-4))
		}
		lines = append(lines, fit(line, w))
	}
	if len(p.matches) == 0 {
		lines = append(lines, st.Subtitle.Render("  no chats match"))
	}
	return strings.Join(lines, "\n")
}

// ---- message info (K) -------------------------------------------------------------------------

type infoOverlay struct {
	a      *App
	lines  []string
	offset int
	height int
}

func newInfoOverlay(a *App, m core.Message) *infoOverlay {
	st := a.styles
	o := &infoOverlay{a: a}
	row := func(k, v string) {
		o.lines = append(o.lines, st.Subtitle.Render(fit(k, 12))+" "+st.App.Render(clean(v)))
	}
	full := func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Local().Format("Mon, Jan 2 2006 15:04:05")
	}
	from := clean(m.SenderName)
	if m.Outgoing {
		from = "You"
	}
	row("From", from)
	row("Sent", full(m.SentAt))
	if !m.Outgoing {
		row("Received", full(m.ReceivedAt))
	}
	if m.Outgoing {
		row("Status", statusText(m.Status))
	}
	if m.ExpiresIn > 0 {
		v := format.FormatDurationLong(m.ExpiresIn)
		if e := m.ExpiresAt(); !e.IsZero() {
			v += " (" + format.FormatRemaining(e.Sub(a.now())) + " left)"
		}
		row("Disappears", v)
	}
	for _, att := range m.Attachments {
		row("Attachment", attachmentLabel(a.icons, att)+" · "+humanSize(att.Size)+" · "+clean(att.ContentType))
	}
	if len(m.PerRecipient) > 0 {
		o.lines = append(o.lines, "", st.Title.Render("Delivery"))
		type rs struct {
			name string
			st   core.DeliveryStatus
		}
		var list []rs
		for aci, s := range m.PerRecipient {
			name := string(aci)
			for _, mem := range a.conv.chat.Members {
				if mem.ACI == aci {
					name = mem.Name
				}
			}
			list = append(list, rs{clean(name), s})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
		for _, r := range list {
			o.lines = append(o.lines, "  "+st.RenderDelivery(a.icons, r.st)+" "+fit(r.name, 20)+" "+st.Subtitle.Render(statusText(r.st)))
		}
	}
	if len(m.Reactions) > 0 {
		o.lines = append(o.lines, "", st.Title.Render("Reactions"))
		for _, r := range m.Reactions {
			name := clean(r.FromName)
			if r.From == a.self.ACI {
				name = "You"
			}
			o.lines = append(o.lines, "  "+clean(r.Emoji)+" "+name)
		}
	}
	if len(m.EditHistory) > 0 {
		o.lines = append(o.lines, "", st.Title.Render("Edit history"))
		for _, ed := range m.EditHistory {
			o.lines = append(o.lines, "  "+st.Timestamp.Render(full(ed.At)))
			for _, l := range format.Wrap(clean(format.PlainText(format.Spans(ed.Body, ed.Ranges, a.mentionName))), 56) {
				o.lines = append(o.lines, "    "+st.Subtitle.Render(l))
			}
		}
	}
	o.lines = append(o.lines, "", st.KeyHintSep.Render("id "+clean(m.ID.String())))
	return o
}

func statusText(s core.DeliveryStatus) string {
	switch s {
	case core.StatusPending:
		return "sending"
	case core.StatusSent:
		return "sent"
	case core.StatusDelivered:
		return "delivered"
	case core.StatusRead:
		return "read"
	case core.StatusViewed:
		return "viewed"
	case core.StatusFailed:
		return "failed"
	}
	return "—"
}

func (o *infoOverlay) Init() tea.Cmd      { return nil }
func (o *infoOverlay) Title() string      { return "Message details" }
func (o *infoOverlay) KeyContext() string { return "info" }
func (o *infoOverlay) Size(w, h int) (int, int) {
	return min(w, 64), min(h, len(o.lines))
}
func (o *infoOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if am, ok := msg.(ui.ActionMsg); ok {
		switch am.Action {
		case keymap.ActCursorDown:
			o.offset++
		case keymap.ActCursorUp:
			o.offset--
		case keymap.ActMessageInfo:
			return nil, nil
		}
		o.offset = clamp(o.offset, 0, max(len(o.lines)-o.height, 0))
	}
	return o, nil
}
func (o *infoOverlay) View(w, h int) string {
	o.height = h
	o.offset = clamp(o.offset, 0, max(len(o.lines)-h, 0))
	return strings.Join(o.lines[o.offset:min(len(o.lines), o.offset+h)], "\n")
}

// ---- image viewer / view-once -------------------------------------------------------------------

type imageOverlay struct {
	a        *App
	msg      core.Message
	att      core.Attachment
	viewOnce bool
	title    string
}

func newImageOverlay(a *App, m core.Message, att core.Attachment, viewOnce bool) *imageOverlay {
	t := "Image"
	if viewOnce {
		t = "View once"
	}
	if att.FileName != "" {
		t += " · " + clean(att.FileName)
	}
	return &imageOverlay{a: a, msg: m, att: att, viewOnce: viewOnce, title: t}
}

// Init marks view-once media as viewed as soon as it is shown, like
// Signal does; closing the viewer then makes it unavailable.
func (o *imageOverlay) Init() tea.Cmd {
	if !o.viewOnce {
		return nil
	}
	id := o.msg.ID
	return o.a.do("view once", func(ctx contextT) error { return o.a.svc.MarkViewOnceOpened(ctx, id) }, "")
}
func (o *imageOverlay) Title() string      { return o.title }
func (o *imageOverlay) KeyContext() string { return keymap.OverlayImage }
func (o *imageOverlay) Size(w, h int) (int, int) {
	ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: o.msg.ID, Attachment: o.att}
	cols, rows := o.a.images.Size(ref, w, h-1)
	return max(cols, 30), rows + 1
}
func (o *imageOverlay) Footer() string {
	if o.viewOnce {
		return o.a.styles.RenderKeyHints("esc", "close")
	}
	return o.a.styles.RenderKeyHints("o", "open externally", "esc", "close")
}

func (o *imageOverlay) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	if am, ok := msg.(ui.ActionMsg); ok {
		switch am.Action {
		case keymap.ActMessageOpenAttachment:
			if o.viewOnce {
				return o, nil
			}
			return o, o.a.openAttachment(o.msg, o.att)
		case keymap.ActItemSelect:
			return nil, nil
		}
	}
	if km, ok := msg.(tea.KeyPressMsg); ok && km.String() == "o" && !o.viewOnce {
		return o, o.a.openAttachment(o.msg, o.att)
	}
	return o, nil
}

func (o *imageOverlay) View(w, h int) string {
	ref := ui.ImageRef{Kind: ui.ImageAttachment, Message: o.msg.ID, Attachment: o.att}
	if !o.att.IsImage() {
		return o.a.styles.Subtitle.Render(attachmentLabel(o.a.icons, o.att) + " — press o to open")
	}
	cols, rows := o.a.images.Size(ref, w, h-1)
	lines, ready := o.a.images.Rows(ref, cols, rows)
	var out []string
	pad := (w - cols) / 2
	for _, l := range lines {
		out = append(out, spaces(pad)+l)
	}
	status := ""
	if !ready {
		status = o.a.styles.Subtitle.Render("loading…")
	} else if c := strings.TrimSpace(o.att.Caption); c != "" {
		status = o.a.styles.App.Render(fitEllipsisRaw(clean(c), w))
	}
	out = append(out, status)
	return strings.Join(out, "\n")
}
