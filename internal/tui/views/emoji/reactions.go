package emoji

import (
	"context"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"warte/internal/core"
	"warte/internal/format"
	"warte/internal/tui/keymap"
	"warte/internal/tui/theme"
	"warte/internal/tui/ui"
)

// Reactions is the reaction detail overlay: who reacted with what on one
// message, grouped by emoji in tabs ("All" first), with the option to remove
// your own reaction or add another one.
type Reactions struct {
	ctx ui.Context
	id  core.MessageID

	loaded bool
	err    error
	gone   bool
	groups []reactionGroup

	tab, sel, off int
	rows          int
	spans         []tabSpan
	listTop       int
}

type reactionGroup struct {
	emoji string
	list  []core.Reaction
}

type reactionsLoadedMsg struct {
	id  core.MessageID
	msg core.Message
	err error
}

// reactionRemoveFailedMsg reports that removing the own reaction failed, so
// the optimistic update must be rolled back.
type reactionRemoveFailedMsg struct {
	id  core.MessageID
	err error
}

// NewReactions returns the reaction detail overlay for message id.
func NewReactions(ctx ui.Context, id core.MessageID) *Reactions {
	return &Reactions{ctx: ctx, id: id, rows: 8}
}

// NewReactionsFor returns the overlay pre-filled from m (it still refreshes
// from the Service on Init).
func NewReactionsFor(ctx ui.Context, m core.Message) *Reactions {
	r := NewReactions(ctx, m.ID)
	r.set(m)
	return r
}

func (r *Reactions) self() core.ACI {
	if r.ctx.Self.ACI != "" {
		return r.ctx.Self.ACI
	}
	if r.ctx.Svc != nil {
		return r.ctx.Svc.Self().ACI
	}
	return ""
}

func (r *Reactions) set(m core.Message) {
	r.loaded = true
	byEmoji := map[string]*reactionGroup{}
	var order []*reactionGroup
	for _, x := range m.Reactions {
		if x.Emoji == "" {
			continue
		}
		g := byEmoji[x.Emoji]
		if g == nil {
			g = &reactionGroup{emoji: x.Emoji}
			byEmoji[x.Emoji] = g
			order = append(order, g)
		}
		g.list = append(g.list, x)
	}
	self := r.self()
	r.groups = r.groups[:0]
	for _, g := range order {
		sort.SliceStable(g.list, func(i, j int) bool {
			a, b := g.list[i], g.list[j]
			if (a.From == self) != (b.From == self) {
				return a.From == self
			}
			return a.At.After(b.At)
		})
		r.groups = append(r.groups, *g)
	}
	sort.SliceStable(r.groups, func(i, j int) bool { return len(r.groups[i].list) > len(r.groups[j].list) })
	r.tab = min(r.tab, len(r.groups))
	r.sel = min(r.sel, max(len(r.current())-1, 0))
}

// current returns the reactions listed in the selected tab.
func (r *Reactions) current() []core.Reaction {
	if r.tab == 0 {
		var all []core.Reaction
		for _, g := range r.groups {
			all = append(all, g.list...)
		}
		return all
	}
	if r.tab-1 < len(r.groups) {
		return r.groups[r.tab-1].list
	}
	return nil
}

// Total returns the number of reactions.
func (r *Reactions) Total() int {
	n := 0
	for _, g := range r.groups {
		n += len(g.list)
	}
	return n
}

// Init implements ui.Overlay.
func (r *Reactions) Init() tea.Cmd { return r.load() }

func (r *Reactions) load() tea.Cmd {
	svc, id := r.ctx.Svc, r.id
	if svc == nil {
		return nil
	}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m, err := svc.Message(c, id)
		return reactionsLoadedMsg{id: id, msg: m, err: err}
	}
}

// Title implements ui.Overlay.
func (r *Reactions) Title() string { return "Reactions" }

// KeyContext implements ui.Overlay.
func (r *Reactions) KeyContext() string { return KeyContextReactions }

// Size implements ui.Overlay.
func (r *Reactions) Size(maxW, maxH int) (int, int) {
	return min(maxW, 52), min(maxH, max(len(r.current())+3, 7))
}

// Footer implements ui.Footer.
func (r *Reactions) Footer() string {
	return r.ctx.Styles.RenderKeyHints("tab", "emoji", "x", "remove mine", "+", "add", "esc", "close")
}

// Update implements ui.Overlay.
func (r *Reactions) Update(msg tea.Msg) (ui.Overlay, tea.Cmd) {
	switch msg := msg.(type) {
	case reactionsLoadedMsg:
		if msg.id != r.id {
			return r, nil
		}
		if msg.err != nil {
			if r.loaded && r.err == nil && !r.gone && len(r.groups) > 0 {
				// Keep the pre-filled list; just report the failed refresh.
				return r, ui.Toast(ui.ToastError, "Couldn't refresh reactions: "+clean(msg.err.Error()))
			}
			r.loaded, r.err = true, msg.err
			return r, nil
		}
		r.err = nil
		r.set(msg.msg)
	case reactionRemoveFailedMsg:
		if msg.id != r.id {
			return r, nil
		}
		// Undo the optimistic removal by reloading the real state.
		return r, r.load()
	case ui.EventMsg:
		switch ev := msg.Event.(type) {
		case core.MessageUpdated:
			if ev.Message.ID == r.id {
				r.set(ev.Message)
			}
		case core.MessageRemoved:
			if ev.ID == r.id {
				r.gone = true
				r.groups = nil
			}
		}
	case tea.KeyPressMsg:
		switch msg.String() {
		case "x", "d", "delete":
			return r, r.removeOwn(false)
		case "+", "a", "R":
			return r, ui.Push(NewReactionPicker(r.ctx, r.id))
		case "tab":
			r.setTab(r.tab + 1)
		case "shift+tab":
			r.setTab(r.tab - 1)
		}
	case ui.ActionMsg:
		n := max(msg.Count, 1)
		list := r.current()
		switch msg.Action {
		case keymap.ActCursorDown:
			r.sel = min(r.sel+n, max(len(list)-1, 0))
		case keymap.ActCursorUp:
			r.sel = max(r.sel-n, 0)
		case keymap.ActCursorTop:
			r.sel = 0
		case keymap.ActCursorBottom:
			r.sel = max(len(list)-1, 0)
		case keymap.ActHalfPageDown, keymap.ActPageDown:
			r.sel = min(r.sel+max(r.rows/2, 1), max(len(list)-1, 0))
		case keymap.ActHalfPageUp, keymap.ActPageUp:
			r.sel = max(r.sel-max(r.rows/2, 1), 0)
		case keymap.ActCursorRight, keymap.ActFocusNext, keymap.ActFocusRight, keymap.ActFolderNext:
			r.setTab(r.tab + n)
		case keymap.ActCursorLeft, keymap.ActFocusPrev, keymap.ActFocusLeft, keymap.ActFolderPrev:
			r.setTab(r.tab - n)
		case keymap.ActItemSelect:
			return r, r.removeOwn(true)
		}
	case tea.MouseClickMsg:
		if msg.Y == 0 {
			for _, s := range r.spans {
				if msg.X >= s.x && msg.X < s.x+s.w {
					r.setTab(s.index)
				}
			}
		} else if i := r.off + msg.Y - r.listTop; msg.Y >= r.listTop && i < len(r.current()) {
			r.sel = i
		}
	case tea.MouseWheelMsg:
		list := r.current()
		if msg.Button == tea.MouseWheelUp {
			r.sel = max(r.sel-1, 0)
		} else if msg.Button == tea.MouseWheelDown {
			r.sel = min(r.sel+1, max(len(list)-1, 0))
		}
	}
	return r, nil
}

func (r *Reactions) setTab(t int) {
	n := len(r.groups) + 1
	r.tab = ((t % n) + n) % n
	r.sel, r.off = 0, 0
}

// removeOwn removes the user's reaction. With onlySelected it acts only when
// the selected row is the user's own reaction.
func (r *Reactions) removeOwn(onlySelected bool) tea.Cmd {
	self := r.self()
	emoji := ""
	list := r.current()
	if r.sel < len(list) && list[r.sel].From == self {
		emoji = list[r.sel].Emoji
	} else if !onlySelected {
		for _, g := range r.groups {
			for _, x := range g.list {
				if x.From == self {
					emoji = x.Emoji
				}
			}
		}
	}
	if emoji == "" {
		if onlySelected {
			return nil
		}
		return ui.Toast(ui.ToastInfo, "You haven't reacted to this message")
	}
	// Optimistic local update; the MessageUpdated event confirms it.
	var kept []core.Reaction
	for _, g := range r.groups {
		for _, x := range g.list {
			if x.From != self {
				kept = append(kept, x)
			}
		}
	}
	r.set(core.Message{ID: r.id, Reactions: kept})
	svc, id := r.ctx.Svc, r.id
	if svc == nil {
		return nil
	}
	return func() tea.Msg {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := svc.React(c, id, emoji, true); err != nil {
			// The toast goes to the App (it always shows); the failure
			// message reaches this overlay only while it is on top.
			failed := reactionRemoveFailedMsg{id: id, err: err}
			return tea.BatchMsg{
				ui.Toast(ui.ToastError, "Couldn't remove reaction: "+clean(err.Error())),
				func() tea.Msg { return failed },
			}
		}
		return nil
	}
}

func (r *Reactions) tabLabels() []string {
	labels := []string{"All " + itoa(r.Total())}
	for _, g := range r.groups {
		labels = append(labels, clean(g.emoji)+" "+itoa(len(g.list)))
	}
	return labels
}

// View implements ui.Overlay.
func (r *Reactions) View(w, h int) string {
	st := r.ctx.Styles
	if w <= 0 || h <= 0 {
		return ""
	}
	var lines []string
	bar, rule, spans := renderTabs(st, r.tabLabels(), r.tab, w)
	r.spans = spans
	lines = append(lines, bar)
	if h >= 4 {
		lines = append(lines, rule)
	}
	r.listTop = len(lines)
	r.rows = max(h-len(lines), 0)
	list := r.current()
	switch {
	case r.gone:
		lines = append(lines, "", muted(st).Render(center("This message was deleted", w)))
	case r.err != nil:
		lines = append(lines, "", st.ErrorText.Render(fitEllipsis(" "+clean(r.err.Error()), w)))
	case !r.loaded:
		lines = append(lines, "", muted(st).Render(center("Loading…", w)))
	case len(list) == 0:
		lines = append(lines, "", muted(st).Render(center("No reactions yet", w)))
	default:
		r.off = scrollTo(r.off, r.sel, r.rows, len(list))
		self := r.self()
		now := time.Now
		if r.ctx.Now != nil {
			now = r.ctx.Now
		}
		for i := r.off; i < len(list) && len(lines) < h; i++ {
			lines = append(lines, r.row(list[i], i == r.sel, list[i].From == self, now(), w))
		}
	}
	return padLines(lines, w, h)
}

func (r *Reactions) row(x core.Reaction, selected, own bool, now time.Time, w int) string {
	st := r.ctx.Styles
	name := clean(x.FromName)
	if name == "" {
		name = "Unknown"
	}
	avName := name
	if own {
		name = "You"
		if n := clean(r.ctx.Self.DisplayName()); n != "" {
			avName = n
		}
	}
	emoji := center(clean(x.Emoji), 4) + " "
	var right string
	if own {
		right = "enter remove"
	} else if !x.At.IsZero() {
		right = format.RelativeTime(x.At, now)
	}
	showAvatar := w >= 30
	avW := 0
	if showAvatar {
		avW = 5
	}
	nameW := w - width(emoji) - avW - width(right) - 2
	if nameW < 4 {
		right = ""
		nameW = w - width(emoji) - avW - 1
	}
	nameW = max(nameW, 0)
	name = strings.TrimRight(fitEllipsis(name, nameW), " ")
	gap := strings.Repeat(" ", max(nameW-width(name), 0)+1)
	if selected {
		av := ""
		if showAvatar {
			av = fit(" "+theme.Initials(avName), 4) + " "
		}
		plain := fit(ansi.Strip(emoji)+av+name+gap+right, w-1)
		return accent(st).Render("▌") + selStyle(st).Render(plain)
	}
	av := ""
	if showAvatar {
		av = st.RenderAvatar("", string(x.From), avName) + " "
	}
	ns := st.SenderStyle(string(x.From))
	if own {
		ns = accent(st)
	}
	return fit(emoji+av+ns.Render(name)+gap+muted(st).Render(right), w)
}
